package triton

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters"
	"github.com/prebid/prebid-server/v4/config"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRTBRoundTrip drives the banner path over real HTTP: the request the adapter builds is what a
// dsp-service stub receives (path, headers, cxr), and the stub's answer becomes the bid.
func TestRTBRoundTrip(t *testing.T) {
	var got struct {
		path    string
		headers http.Header
		body    map[string]json.RawMessage
	}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.headers = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &got.body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"rt","cur":"USD","seatbid":[{"seat":"tgen","bid":[{"id":"rt-1","impid":"b-1","price":1.05,"mtype":1,"w":300,"h":250,"adm":"<div>${AUCTION_PRICE}</div>","nurl":"` + "http://pixel.test/win?p=${AUCTION_PRICE}" + `"}]}]}`))
	}))
	defer stub.Close()

	bidder, err := Builder(openrtb_ext.BidderTriton, config.Adapter{
		Endpoint:         stub.URL + "/goservices/dsp/rtb/{{.AccountID}}/{{.AdUnit}}",
		ExtraAdapterInfo: `{"giv": 0.2}`,
	}, testServer)
	require.NoError(t, err)

	request := &openrtb2.BidRequest{
		ID:  "rt",
		App: &openrtb2.App{Bundle: "com.begenuin.demo", Publisher: &openrtb2.Publisher{ID: "1842"}},
		Imp: []openrtb2.Imp{{
			ID:     "b-1",
			Banner: &openrtb2.Banner{Format: []openrtb2.Format{{W: 300, H: 250}}},
			Ext:    json.RawMessage(`{"gpid":"/1842/demo","data":{"placementId":"pl-1"},"bidder":{"stid":1443132,"tagId":"6a3aa78ba0daccfd439648b8","hostRender":"client"}}`),
		}},
	}
	before, _ := json.Marshal(request)

	reqs, errs := bidder.MakeRequests(request, &adapters.ExtraRequestInfo{})
	require.Empty(t, errs)
	require.Len(t, reqs, 1)
	after, _ := json.Marshal(request)
	assert.JSONEq(t, string(before), string(after), "MakeRequests must not mutate the incoming request")

	httpReq, err := http.NewRequestWithContext(context.Background(), reqs[0].Method, reqs[0].Uri, bytes.NewReader(reqs[0].Body))
	require.NoError(t, err)
	httpReq.Header = reqs[0].Headers
	resp, err := http.DefaultClient.Do(httpReq)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	assert.Equal(t, "/goservices/dsp/rtb/1842/6a3aa78ba0daccfd439648b8", got.path)
	assert.Equal(t, "application/json;charset=utf-8", got.headers.Get("Content-Type"))
	assert.Equal(t, "application/json", got.headers.Get("Accept"))
	assert.JSONEq(t, `{"cxr":{"giv":0.2,"pa":1,"stid":1443132,"host_render":"client","brand_id":"1842","tag_id":"6a3aa78ba0daccfd439648b8","placement_id":"pl-1"}}`, string(got.body["ext"]))
	var imps []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got.body["imp"], &imps))
	require.Len(t, imps, 1)
	assert.JSONEq(t, `{"gpid":"/1842/demo","data":{"placementId":"pl-1"}}`, string(imps[0]["ext"]), "bidder params stripped, the rest kept")

	bids, errs := bidder.MakeBids(request, reqs[0], &adapters.ResponseData{StatusCode: resp.StatusCode, Body: body, Headers: resp.Header})
	require.Empty(t, errs)
	require.Len(t, bids.Bids, 1)
	assert.Equal(t, openrtb_ext.BidTypeBanner, bids.Bids[0].BidType)
	assert.Equal(t, "<div>1.05</div>", bids.Bids[0].Bid.AdM)
	assert.Equal(t, "http://pixel.test/win?p=1.05", bids.Bids[0].Bid.NURL)
}
