package genuinhb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters"
	"github.com/prebid/prebid-server/v4/adapters/adapterstest"
	"github.com/prebid/prebid-server/v4/config"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/prebid/prebid-server/v4/util/ptrutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testEndpoint = "https://nxs.example.test/tagxml/customer/{{.AccountID}}/tag/{{.AdUnit}}"

// gamEndpoint is the GAM tag base an alias of type gam-wrapper is configured with; no macros.
const gamEndpoint = "https://pubads.g.doubleclick.net/gampad/ads"

var testServer = config.Server{ExternalUrl: "http://hosturl.com", GvlID: 1, DataCenter: "2"}

func newTestAdapter(t *testing.T, endpoint, extra string) *adapter {
	t.Helper()
	bidder, err := Builder(openrtb_ext.BidderGenuinHB, config.Adapter{Endpoint: endpoint, ExtraAdapterInfo: extra}, testServer)
	require.NoError(t, err)
	return bidder.(*adapter)
}

func TestJsonSamples(t *testing.T) {
	bidder, buildErr := Builder(openrtb_ext.BidderGenuinHB, config.Adapter{Endpoint: testEndpoint}, testServer)
	require.NoError(t, buildErr)

	adapterstest.RunJSONBidderTest(t, "genuinhbtest", bidder)
}

// TestJsonSamplesExplicitVastTag proves that naming the default source type changes nothing:
// the same JSON specs must pass with extra_info {"type": "vast-tag"}.
func TestJsonSamplesExplicitVastTag(t *testing.T) {
	bidder, buildErr := Builder(openrtb_ext.BidderGenuinHB, config.Adapter{Endpoint: testEndpoint, ExtraAdapterInfo: `{"type": "vast-tag"}`}, testServer)
	require.NoError(t, buildErr)

	adapterstest.RunJSONBidderTest(t, "genuinhbtest", bidder)
}

// TestJsonSamplesGAMWrapper drives the gam-wrapper source through the harness with a fixed correlator.
func TestJsonSamplesGAMWrapper(t *testing.T) {
	bidder, buildErr := Builder(openrtb_ext.BidderGenuinHB, config.Adapter{Endpoint: gamEndpoint, ExtraAdapterInfo: `{"type": "gam-wrapper"}`}, testServer)
	require.NoError(t, buildErr)
	gamSourceOf(t, bidder.(*adapter)).newCorrelator = func() string { return "123456" }

	adapterstest.RunJSONBidderTest(t, "genuinhbgamtest", bidder)
}

// TestJsonSamplesGAMWrapperSizes runs the gam-wrapper source with the alias-level default slot sizes.
func TestJsonSamplesGAMWrapperSizes(t *testing.T) {
	bidder, buildErr := Builder(openrtb_ext.BidderGenuinHB, config.Adapter{Endpoint: gamEndpoint, ExtraAdapterInfo: `{"type": "gam-wrapper", "sizes": ["640x480", "400x300"]}`}, testServer)
	require.NoError(t, buildErr)
	gamSourceOf(t, bidder.(*adapter)).newCorrelator = func() string { return "123456" }

	adapterstest.RunJSONBidderTest(t, "genuinhbgamsizestest", bidder)
}

func TestBuilder(t *testing.T) {
	tests := []struct {
		name             string
		endpoint         string
		extraInfo        string
		wantErr          string
		wantDefaultPrice float64
		wantCurrency     string
	}{
		{name: "valid", endpoint: testEndpoint, wantCurrency: "USD"},
		{name: "valid-with-query", endpoint: testEndpoint + "?env=qa", wantCurrency: "USD"},
		{name: "valid-http", endpoint: "http://127.0.0.1:9000/tagxml/customer/{{.AccountID}}/tag/{{.AdUnit}}", wantCurrency: "USD"},
		{name: "blank-extra-info", endpoint: testEndpoint, extraInfo: "   ", wantCurrency: "USD"},
		{name: "default-price", endpoint: testEndpoint, extraInfo: `{"default_price": 0.5}`, wantDefaultPrice: 0.5, wantCurrency: "USD"},
		{name: "default-price-currency", endpoint: testEndpoint, extraInfo: `{"default_price": 0.5, "currency": "eur"}`, wantDefaultPrice: 0.5, wantCurrency: "EUR"},
		{name: "empty-endpoint", endpoint: "", wantErr: "endpoint is empty"},
		{name: "malformed-template", endpoint: "{{Malformed}}", wantErr: "unable to parse endpoint url template"},
		{name: "unknown-macro", endpoint: "https://nxs.example.test/tagxml/customer/{{.CustomerID}}/tag/{{.AdUnit}}", wantErr: "unable to resolve endpoint url template"},
		{name: "missing-tag-macro", endpoint: "https://nxs.example.test/tagxml/customer/{{.AccountID}}", wantErr: "endpoint path must contain"},
		{name: "ids-in-query-only", endpoint: "https://nxs.example.test/tagxml?c={{.AccountID}}&t={{.AdUnit}}", wantErr: "endpoint path must contain"},
		{name: "macro-in-host", endpoint: "https://{{.AdUnit}}.example.test/tagxml/customer/{{.AccountID}}/tag/{{.AdUnit}}", wantErr: "endpoint host must not contain"},
		{name: "relative", endpoint: "/tagxml/customer/{{.AccountID}}/tag/{{.AdUnit}}", wantErr: "absolute http or https URL"},
		{name: "ftp-scheme", endpoint: "ftp://nxs.example.test/tagxml/customer/{{.AccountID}}/tag/{{.AdUnit}}", wantErr: "absolute http or https URL"},
		{name: "no-host", endpoint: "https:///tagxml/customer/{{.AccountID}}/tag/{{.AdUnit}}", wantErr: "endpoint has no host"},
		{name: "bad-extra-info-json", endpoint: testEndpoint, extraInfo: `not json`, wantErr: "invalid extra_info"},
		{name: "negative-default-price", endpoint: testEndpoint, extraInfo: `{"default_price": -1}`, wantErr: "default_price must not be negative"},
		{name: "bad-currency", endpoint: testEndpoint, extraInfo: `{"default_price": 1, "currency": "dollars"}`, wantErr: "is not an ISO 4217 code"},
		{name: "type-vast-tag-explicit", endpoint: testEndpoint, extraInfo: `{"type": "vast-tag", "default_price": 0.5}`, wantDefaultPrice: 0.5, wantCurrency: "USD"},
		{name: "type-gam-wrapper", endpoint: gamEndpoint, extraInfo: `{"type": "gam-wrapper"}`, wantCurrency: "USD"},
		{name: "type-gam-wrapper-needs-absolute-https", endpoint: "pubads.g.doubleclick.net/gampad/ads", extraInfo: `{"type": "gam-wrapper"}`, wantErr: "absolute http or https URL"},
		{name: "type-vast-tag-still-needs-macros", endpoint: gamEndpoint, extraInfo: `{"type": "vast-tag"}`, wantErr: "endpoint path must contain"},
		{name: "type-unknown", endpoint: testEndpoint, extraInfo: `{"type": "bogus"}`, wantErr: `unknown type "bogus"`},
		{name: "gam-default-sizes", endpoint: gamEndpoint, extraInfo: `{"type": "gam-wrapper", "sizes": ["640x480", "400x300"]}`, wantCurrency: "USD"},
		{name: "gam-bad-default-size", endpoint: gamEndpoint, extraInfo: `{"type": "gam-wrapper", "sizes": ["640 x 480"]}`, wantErr: `size "640 x 480" is not WxH`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bidder, err := Builder(openrtb_ext.BidderGenuinHB, config.Adapter{Endpoint: test.endpoint, ExtraAdapterInfo: test.extraInfo}, testServer)
			if test.wantErr != "" {
				assert.ErrorContains(t, err, test.wantErr)
				assert.Nil(t, bidder)
				return
			}
			require.NoError(t, err)
			a := bidder.(*adapter)
			if tag, ok := a.source.(*tagSource); ok {
				assert.Equal(t, test.wantDefaultPrice, tag.defaultPrice)
				assert.Equal(t, test.wantCurrency, tag.defaultPriceCurrency)
			} else {
				assert.IsType(t, &gamSource{}, a.source)
			}
		})
	}
}

func videoImp(id, customerID, tagID string) openrtb2.Imp {
	return openrtb2.Imp{
		ID:    id,
		Video: &openrtb2.Video{MIMEs: []string{"video/mp4"}, W: ptrutil.ToPtr[int64](640), H: ptrutil.ToPtr[int64](480)},
		Ext:   impExt(customerID, tagID),
	}
}

func audioImp(id, customerID, tagID string) openrtb2.Imp {
	return openrtb2.Imp{
		ID:    id,
		Audio: &openrtb2.Audio{MIMEs: []string{"audio/mpeg"}},
		Ext:   impExt(customerID, tagID),
	}
}

func impExt(customerID, tagID string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"bidder":{"customerId":%q,"tagId":%q}}`, customerID, tagID))
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return body
}

func doRequest(ctx context.Context, client *http.Client, reqData *adapters.RequestData) (*adapters.ResponseData, error) {
	httpReq, err := http.NewRequestWithContext(ctx, reqData.Method, reqData.Uri, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header = reqData.Headers.Clone()
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	return &adapters.ResponseData{StatusCode: httpResp.StatusCode, Body: body, Headers: httpResp.Header}, nil
}

// tagSourceOf returns the adapter's vast-tag source or fails the test.
func tagSourceOf(t *testing.T, a *adapter) *tagSource {
	t.Helper()
	s, ok := a.source.(*tagSource)
	require.True(t, ok, "adapter is not a vast-tag source")
	return s
}

// gamSourceOf returns the adapter's gam-wrapper source or fails the test.
func gamSourceOf(t *testing.T, a *adapter) *gamSource {
	t.Helper()
	s, ok := a.source.(*gamSource)
	require.True(t, ok, "adapter is not a gam-wrapper source")
	return s
}
