package genuinhb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters"
	"github.com/prebid/prebid-server/v4/errortypes"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestShippedEndpoint checks that the endpoint in static/bidder-info builds the QA URL from the
// sample provider configuration (customerId 3119, tagId 3753).
func TestShippedEndpoint(t *testing.T) {
	raw, err := os.ReadFile("../../static/bidder-info/genuinhb.yaml")
	require.NoError(t, err)
	var info struct {
		Endpoint string `yaml:"endpoint"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &info))

	a := newTestAdapter(t, info.Endpoint, "")
	uri, err := tagSourceOf(t, a).buildEndpointURL("3119", "3753")
	require.NoError(t, err)
	assert.Equal(t, "https://nxs.qa.begenuin.com/tagxml/customer/3119/tag/3753", uri)
}

func TestBuildEndpointURL(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   string
		customerID string
		tagID      string
		want       string
	}{
		{name: "sample-provider", endpoint: testEndpoint, customerID: "3119", tagID: "3753", want: "https://nxs.example.test/tagxml/customer/3119/tag/3753"},
		{name: "other-provider", endpoint: testEndpoint, customerID: "42", tagID: "abc_DEF-1", want: "https://nxs.example.test/tagxml/customer/42/tag/abc_DEF-1"},
		{name: "endpoint-query-preserved", endpoint: testEndpoint + "?env=qa&src=pbs", customerID: "3119", tagID: "3753", want: "https://nxs.example.test/tagxml/customer/3119/tag/3753?env=qa&src=pbs"},
		{name: "no-double-slash", endpoint: "https://nxs.example.test/tagxml/customer/{{.AccountID}}/tag/{{.AdUnit}}/", customerID: "1", tagID: "2", want: "https://nxs.example.test/tagxml/customer/1/tag/2/"},
		// MakeRequests rejects these ids; the builder still keeps each one inside its own path segment.
		{name: "escaped-traversal", endpoint: testEndpoint, customerID: "../x", tagID: "a b/c?d#e", want: "https://nxs.example.test/tagxml/customer/..%2Fx/tag/a%20b%2Fc%3Fd%23e"},
		{name: "escaped-host-swap", endpoint: testEndpoint, customerID: "@evil.test", tagID: "//evil.test", want: "https://nxs.example.test/tagxml/customer/@evil.test/tag/%2F%2Fevil.test"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := newTestAdapter(t, test.endpoint, "")
			got, err := tagSourceOf(t, a).buildEndpointURL(test.customerID, test.tagID)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)

			u, err := url.Parse(got)
			require.NoError(t, err)
			assert.Equal(t, "nxs.example.test", u.Host, "ids must never change the host")
			assert.Empty(t, u.Fragment)
		})
	}
}

func TestMakeBids(t *testing.T) {
	both := videoImp("imp-1", "3119", "3753")
	both.Audio = &openrtb2.Audio{MIMEs: []string{"audio/mpeg"}}

	tests := []struct {
		name      string
		imp       openrtb2.Imp
		extraInfo string
		status    int
		fixture   string
		body      []byte
		impIDs    []string

		wantNoBid    bool
		wantErrType  error
		wantErr      string
		wantPrice    float64
		wantCurrency string
		wantType     openrtb_ext.BidType
		wantMType    openrtb2.MarkupType
		wantCrID     string
		wantDuration int
		wantW, wantH int64
	}{
		{name: "inline-pricing", fixture: "inline-pricing.xml",
			wantPrice: 2.5, wantCurrency: "USD", wantType: openrtb_ext.BidTypeVideo, wantMType: openrtb2.MarkupVideo, wantCrID: "ad-123", wantDuration: 15, wantW: 640, wantH: 480},
		{name: "wrapper-pricing", fixture: "wrapper-pricing.xml",
			wantPrice: 1.75, wantCurrency: "USD", wantType: openrtb_ext.BidTypeVideo, wantMType: openrtb2.MarkupVideo, wantCrID: "wr-1", wantW: 640, wantH: 480},
		{name: "no-pricing-default-price", fixture: "no-pricing.xml", extraInfo: `{"default_price": 0.4}`,
			wantPrice: 0.4, wantCurrency: "USD", wantType: openrtb_ext.BidTypeVideo, wantMType: openrtb2.MarkupVideo, wantCrID: "cr-np", wantDuration: 31, wantW: 640, wantH: 480},
		{name: "no-pricing-no-default", fixture: "no-pricing.xml",
			wantNoBid: true, wantErrType: &errortypes.Warning{}, wantErr: "no usable CPM <Pricing>"},
		{name: "pricing-beats-default", fixture: "inline-pricing.xml", extraInfo: `{"default_price": 0.4, "currency": "EUR"}`,
			wantPrice: 2.5, wantCurrency: "USD", wantType: openrtb_ext.BidTypeVideo, wantMType: openrtb2.MarkupVideo, wantCrID: "ad-123", wantDuration: 15, wantW: 640, wantH: 480},
		{name: "eur-pricing", fixture: "eur-pricing.xml",
			wantPrice: 3.1, wantCurrency: "EUR", wantType: openrtb_ext.BidTypeVideo, wantMType: openrtb2.MarkupVideo, wantCrID: "eur-1", wantW: 640, wantH: 480},
		{name: "cpc-pricing-ignored", fixture: "cpc-pricing.xml",
			wantNoBid: true, wantErrType: &errortypes.Warning{}, wantErr: "no usable CPM <Pricing>"},
		{name: "cpc-pricing-default-price", fixture: "cpc-pricing.xml", extraInfo: `{"default_price": 0.3}`,
			wantPrice: 0.3, wantCurrency: "USD", wantType: openrtb_ext.BidTypeVideo, wantMType: openrtb2.MarkupVideo, wantCrID: "cpc-1", wantW: 640, wantH: 480},
		{name: "bad-pricing-currency", fixture: "bad-currency.xml",
			wantNoBid: true, wantErrType: &errortypes.Warning{}, wantErr: "no usable CPM <Pricing>"},
		{name: "zero-pricing", fixture: "zero-pricing.xml",
			wantNoBid: true, wantErrType: &errortypes.Warning{}, wantErr: "no usable CPM <Pricing>"},
		{name: "nan-pricing", fixture: "nan-pricing.xml",
			wantNoBid: true, wantErrType: &errortypes.Warning{}, wantErr: "no usable CPM <Pricing>"},
		{name: "crid-fallback", fixture: "no-ids.xml",
			wantPrice: 1, wantCurrency: "USD", wantType: openrtb_ext.BidTypeVideo, wantMType: openrtb2.MarkupVideo, wantCrID: "genuinhb-3119-3753", wantDuration: 10, wantW: 640, wantH: 480},
		{name: "latin1-declaration", fixture: "latin1.xml",
			wantPrice: 0.9, wantCurrency: "USD", wantType: openrtb_ext.BidTypeVideo, wantMType: openrtb2.MarkupVideo, wantCrID: "latin-1", wantW: 640, wantH: 480},
		{name: "audio-imp", imp: audioImp("imp-1", "3119", "3753"), fixture: "audio.xml",
			wantPrice: 1.2, wantCurrency: "USD", wantType: openrtb_ext.BidTypeAudio, wantMType: openrtb2.MarkupAudio, wantCrID: "audio-1", wantDuration: 20},
		{name: "video-and-audio-imp-audio-media", imp: both, fixture: "audio.xml",
			wantPrice: 1.2, wantCurrency: "USD", wantType: openrtb_ext.BidTypeAudio, wantMType: openrtb2.MarkupAudio, wantCrID: "audio-1", wantDuration: 20},
		{name: "video-and-audio-imp-video-media", imp: both, fixture: "inline-pricing.xml",
			wantPrice: 2.5, wantCurrency: "USD", wantType: openrtb_ext.BidTypeVideo, wantMType: openrtb2.MarkupVideo, wantCrID: "ad-123", wantDuration: 15, wantW: 640, wantH: 480},
		{name: "empty-vast", fixture: "empty.xml", wantNoBid: true},
		{name: "error-only-vast", fixture: "error-only.xml", wantNoBid: true},
		{name: "no-content", status: http.StatusNoContent, wantNoBid: true},
		{name: "empty-200", body: []byte{}, wantNoBid: true},
		{name: "whitespace-200", body: []byte(" \n\t "), wantNoBid: true},
		{name: "malformed-vast", fixture: "malformed.xml",
			wantNoBid: true, wantErrType: &errortypes.BadServerResponse{}, wantErr: "genuinhb returned invalid VAST"},
		{name: "not-vast", fixture: "not-vast.xml",
			wantNoBid: true, wantErrType: &errortypes.BadServerResponse{}, wantErr: "genuinhb returned invalid VAST"},
		{name: "server-error", status: http.StatusInternalServerError, body: []byte("oops"),
			wantNoBid: true, wantErrType: &errortypes.BadServerResponse{}, wantErr: "Unexpected status code: 500"},
		{name: "bad-request", status: http.StatusBadRequest,
			wantNoBid: true, wantErrType: &errortypes.BadInput{}, wantErr: "Unexpected status code: 400"},
		{name: "unknown-imp", fixture: "inline-pricing.xml", impIDs: []string{"other"},
			wantNoBid: true, wantErrType: &errortypes.BadInput{}, wantErr: "failed to find the impression"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			imp := test.imp
			if imp.ID == "" {
				imp = videoImp("imp-1", "3119", "3753")
			}
			status := test.status
			if status == 0 {
				status = http.StatusOK
			}
			body := test.body
			if test.fixture != "" {
				body = loadFixture(t, test.fixture)
			}
			impIDs := test.impIDs
			if impIDs == nil {
				impIDs = []string{imp.ID}
			}

			a := newTestAdapter(t, testEndpoint, test.extraInfo)
			request := &openrtb2.BidRequest{ID: "req-1", Imp: []openrtb2.Imp{imp}}
			response, errs := a.MakeBids(request,
				&adapters.RequestData{Method: http.MethodGet, ImpIDs: impIDs},
				&adapters.ResponseData{StatusCode: status, Body: body})

			if test.wantErr != "" {
				require.Len(t, errs, 1)
				assert.IsType(t, test.wantErrType, errs[0])
				assert.Contains(t, errs[0].Error(), test.wantErr)
			} else {
				assert.Empty(t, errs)
			}
			if test.wantNoBid {
				assert.Nil(t, response)
				return
			}

			require.NotNil(t, response)
			require.Len(t, response.Bids, 1)
			assert.Equal(t, test.wantCurrency, response.Currency)

			typedBid := response.Bids[0]
			assert.Equal(t, test.wantType, typedBid.BidType)
			bid := typedBid.Bid
			assert.Equal(t, "genuinhb-imp-1", bid.ID)
			assert.Equal(t, "imp-1", bid.ImpID)
			assert.InDelta(t, test.wantPrice, bid.Price, 1e-9)
			assert.Equal(t, test.wantCrID, bid.CrID)
			assert.Equal(t, test.wantMType, bid.MType)
			assert.Equal(t, test.wantW, bid.W)
			assert.Equal(t, test.wantH, bid.H)
			assert.Equal(t, string(body), bid.AdM, "adm must be the VAST exactly as served")
			if test.wantDuration > 0 {
				require.NotNil(t, typedBid.BidVideo)
				assert.Equal(t, test.wantDuration, typedBid.BidVideo.Duration)
			} else {
				assert.Nil(t, typedBid.BidVideo)
			}
		})
	}
}

// TestHTTPRoundTrip sends the adapter's requests to a local tag server, the way the PBS core does,
// and feeds the real responses back to MakeBids.
func TestHTTPRoundTrip(t *testing.T) {
	vast := loadFixture(t, "inline-pricing.xml")
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.EscapedPath() {
		case "/tagxml/customer/3119/tag/3753":
			assert.Equal(t, "test-ua", r.Header.Get("User-Agent"))
			assert.Equal(t, "203.0.113.7", r.Header.Get("X-Forwarded-For"))
			assert.Equal(t, "https://publisher.example/page", r.Header.Get("Referer"))
			assert.Equal(t, "application/xml", r.Header.Get("Accept"))
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write(vast)
		case "/tagxml/customer/3119/tag/nofill":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	a := newTestAdapter(t, server.URL+"/tagxml/customer/{{.AccountID}}/tag/{{.AdUnit}}", "")
	request := &openrtb2.BidRequest{
		ID:     "req-1",
		Imp:    []openrtb2.Imp{videoImp("imp-fill", "3119", "3753"), videoImp("imp-nofill", "3119", "nofill")},
		Site:   &openrtb2.Site{Page: "https://publisher.example/page"},
		Device: &openrtb2.Device{UA: "test-ua", IP: "203.0.113.7"},
	}

	requests, errs := a.MakeRequests(request, &adapters.ExtraRequestInfo{})
	require.Empty(t, errs)
	require.Len(t, requests, 2)

	var bids []*adapters.TypedBid
	for _, reqData := range requests {
		resp, err := doRequest(context.Background(), server.Client(), reqData)
		require.NoError(t, err)
		bidResponse, bidErrs := a.MakeBids(request, reqData, resp)
		require.Empty(t, bidErrs)
		if bidResponse != nil {
			bids = append(bids, bidResponse.Bids...)
		}
	}

	assert.Equal(t, int32(2), hits.Load())
	require.Len(t, bids, 1)
	assert.Equal(t, "imp-fill", bids[0].Bid.ImpID)
	assert.Equal(t, 2.5, bids[0].Bid.Price)
	assert.Equal(t, string(vast), bids[0].Bid.AdM)
}

// TestHTTPContextPropagation shows that the GET built by the adapter honors cancellation and
// deadlines when executed with the auction context, as exchange/bidder.go does via ctxhttp.Do.
func TestHTTPContextPropagation(t *testing.T) {
	release := make(chan struct{})
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	defer close(release)

	a := newTestAdapter(t, server.URL+"/tagxml/customer/{{.AccountID}}/tag/{{.AdUnit}}", "")
	request := &openrtb2.BidRequest{ID: "req-1", Imp: []openrtb2.Imp{videoImp("imp-1", "3119", "3753")}}
	requests, errs := a.MakeRequests(request, &adapters.ExtraRequestInfo{})
	require.Empty(t, errs)
	require.Len(t, requests, 1)

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := doRequest(ctx, server.Client(), requests[0])
		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, int32(0), hits.Load(), "a cancelled auction must not reach the tag server")
	})

	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := doRequest(ctx, server.Client(), requests[0])
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

// TestRaceConcurrentProviders runs many providers through one shared adapter instance at once.
// Each goroutine must get its own URL and creative id back: no state leaks between customers.
func TestRaceConcurrentProviders(t *testing.T) {
	a := newTestAdapter(t, testEndpoint, `{"default_price": 0.5}`)
	vast := loadFixture(t, "no-ids.xml")

	const workers = 64
	var wg sync.WaitGroup
	failures := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			customerID, tagID := fmt.Sprintf("c%d", i), fmt.Sprintf("t%d", i)
			request := &openrtb2.BidRequest{ID: "req", Imp: []openrtb2.Imp{videoImp(fmt.Sprintf("imp-%d", i), customerID, tagID)}}

			requests, errs := a.MakeRequests(request, &adapters.ExtraRequestInfo{})
			if len(errs) > 0 || len(requests) != 1 {
				failures <- fmt.Errorf("worker %d: MakeRequests returned %d requests, errors %v", i, len(requests), errs)
				return
			}
			wantURI := fmt.Sprintf("https://nxs.example.test/tagxml/customer/%s/tag/%s", customerID, tagID)
			if requests[0].Uri != wantURI {
				failures <- fmt.Errorf("worker %d: got URI %s, want %s", i, requests[0].Uri, wantURI)
				return
			}

			response, errs := a.MakeBids(request, requests[0], &adapters.ResponseData{StatusCode: http.StatusOK, Body: vast})
			if len(errs) > 0 || response == nil || len(response.Bids) != 1 {
				failures <- fmt.Errorf("worker %d: MakeBids failed: %v", i, errs)
				return
			}
			wantCrID := fmt.Sprintf("genuinhb-%s-%s", customerID, tagID)
			if got := response.Bids[0].Bid.CrID; got != wantCrID {
				failures <- fmt.Errorf("worker %d: got crid %s, want %s", i, got, wantCrID)
			}
		}(i)
	}
	wg.Wait()
	close(failures)

	var all []error
	for err := range failures {
		all = append(all, err)
	}
	assert.NoError(t, errors.Join(all...))
}
