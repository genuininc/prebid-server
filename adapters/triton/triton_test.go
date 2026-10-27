package triton

import (
	"testing"

	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters/adapterstest"
	"github.com/prebid/prebid-server/v4/config"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/stretchr/testify/require"
)

// testEndpoint mirrors the production template: dsp-service's RTB endpoint with brand and ad tag in the path.
const testEndpoint = "https://dsp.example.test/goservices/dsp/rtb/{{.AccountID}}/{{.AdUnit}}"

var testServer = config.Server{ExternalUrl: "http://hosturl.com", GvlID: 1, DataCenter: "2"}

func TestJsonSamples(t *testing.T) {
	bidder, buildErr := Builder(openrtb_ext.BidderTriton, config.Adapter{Endpoint: testEndpoint}, testServer)
	require.NoError(t, buildErr)

	adapterstest.RunJSONBidderTest(t, "tritontest", bidder)
}

// TestJsonSamplesExtraInfo runs the harness with the host-level cxr values configured.
func TestJsonSamplesExtraInfo(t *testing.T) {
	bidder, buildErr := Builder(openrtb_ext.BidderTriton, config.Adapter{Endpoint: testEndpoint, ExtraAdapterInfo: `{"giv": 0.4, "ad_slots": 3}`}, testServer)
	require.NoError(t, buildErr)

	adapterstest.RunJSONBidderTest(t, "tritonextrainfotest", bidder)
}

func TestBuilder(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  string
		extraInfo string
		wantErr   string
	}{
		{name: "valid", endpoint: testEndpoint},
		{name: "valid with extra info", endpoint: testEndpoint, extraInfo: `{"giv": 0.2, "ad_slots": 0}`},
		{name: "valid giv bounds", endpoint: testEndpoint, extraInfo: `{"giv": 1}`},
		{name: "empty endpoint", endpoint: "  ", wantErr: "endpoint is empty"},
		{name: "malformed template", endpoint: "{{Malformed}}", wantErr: "unable to parse endpoint url template"},
		{name: "missing tag macro", endpoint: "https://dsp.example.test/rtb/{{.AccountID}}", wantErr: "endpoint path must contain both"},
		{name: "missing brand macro", endpoint: "https://dsp.example.test/rtb/{{.AdUnit}}", wantErr: "endpoint path must contain both"},
		{name: "macro in host", endpoint: "https://{{.AccountID}}.example.test/rtb/{{.AccountID}}/{{.AdUnit}}", wantErr: "endpoint host must not contain a macro"},
		{name: "not http", endpoint: "ftp://dsp.example.test/rtb/{{.AccountID}}/{{.AdUnit}}", wantErr: "endpoint must be an absolute http or https URL"},
		{name: "bad extra info json", endpoint: testEndpoint, extraInfo: `{"giv":`, wantErr: "invalid extra_info"},
		{name: "giv above 1", endpoint: testEndpoint, extraInfo: `{"giv": 1.5}`, wantErr: "giv must be between 0 and 1"},
		{name: "giv negative", endpoint: testEndpoint, extraInfo: `{"giv": -0.1}`, wantErr: "giv must be between 0 and 1"},
		{name: "negative ad_slots", endpoint: testEndpoint, extraInfo: `{"ad_slots": -1}`, wantErr: "ad_slots must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Builder(openrtb_ext.BidderTriton, config.Adapter{Endpoint: tt.endpoint, ExtraAdapterInfo: tt.extraInfo}, testServer)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestBidTypeFor covers the mtype fallback, including the ambiguity the RTB path cannot reach
// through MakeRequests (a banner-only impression is the only one routed to dsp-service).
func TestBidTypeFor(t *testing.T) {
	imps := []openrtb2.Imp{
		{ID: "banner", Banner: &openrtb2.Banner{}},
		{ID: "both", Banner: &openrtb2.Banner{}, Video: &openrtb2.Video{}},
		{ID: "audio", Audio: &openrtb2.Audio{}},
	}
	tests := []struct {
		name    string
		bid     openrtb2.Bid
		want    openrtb_ext.BidType
		wantErr string
	}{
		{name: "mtype banner", bid: openrtb2.Bid{ID: "1", ImpID: "both", MType: openrtb2.MarkupBanner}, want: openrtb_ext.BidTypeBanner},
		{name: "mtype video", bid: openrtb2.Bid{ID: "2", ImpID: "banner", MType: openrtb2.MarkupVideo}, want: openrtb_ext.BidTypeVideo},
		{name: "mtype audio", bid: openrtb2.Bid{ID: "3", ImpID: "banner", MType: openrtb2.MarkupAudio}, want: openrtb_ext.BidTypeAudio},
		{name: "no mtype, single media type", bid: openrtb2.Bid{ID: "4", ImpID: "audio"}, want: openrtb_ext.BidTypeAudio},
		{name: "no mtype, ambiguous imp", bid: openrtb2.Bid{ID: "5", ImpID: "both"}, wantErr: `bid "5" has no mtype and imp "both" has 2 media types`},
		{name: "unknown mtype", bid: openrtb2.Bid{ID: "6", ImpID: "banner", MType: 7}, wantErr: `bid "6" has unsupported mtype 7`},
		{name: "unknown imp", bid: openrtb2.Bid{ID: "7", ImpID: "nope"}, wantErr: `bid "7" refers to unknown imp "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bidTypeFor(&tt.bid, imps)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestTagIDFromExt(t *testing.T) {
	tests := map[string]string{
		`{"data":{"tagId":"6aa25b631b3a25f3c479ef7c"}}`:           "6aa25b631b3a25f3c479ef7c",
		`{"data":{"tagId":["6aa25b631b3a25f3c479ef7c","other"]}}`: "6aa25b631b3a25f3c479ef7c",
		`{"data":{"tagId":[1, "second"]}}`:                        "second",
		`{"data":{"tagId":[]}}`:                                   "",
		`{"data":{"tagId":42}}`:                                   "",
		`{"data":{"placementId":"pl"}}`:                           "",
		`{"bidder":{"tagId":"stored"}}`:                           "",
		``:                                                        "",
	}
	for ext, want := range tests {
		require.Equal(t, want, tagIDFromExt([]byte(ext)), ext)
	}
}

func TestIDsFromAdRequestURL(t *testing.T) {
	tests := map[string][2]string{
		"https://aapi.begenuin.com/goservices/dsp/rtb/3252/6aa25b631b3a25f3c479ef7c":      {"3252", "6aa25b631b3a25f3c479ef7c"},
		"https://aapi.begenuin.com/goservices/dsp/vast/3252/6aa25b631b3a25f3c479ef7c?x=1": {"3252", "6aa25b631b3a25f3c479ef7c"},
		"http://localhost:9096/dsp/rtb/1842/tag-1/":                                       {"1842", "tag-1"},
		"https://od-spy.live.streamtheworld.com/ondemand/ars":                             {"", ""},
		"https://aapi.begenuin.com/goservices/dsp/rtb/3252":                               {"", ""},
		"https://aapi.begenuin.com/goservices/dsp/rtb/32/52/6aa25b631b3a25f3c479ef7c":     {"32", "52"},
		"":       {"", ""},
		"://bad": {"", ""},
	}
	for raw, want := range tests {
		brand, tag := idsFromAdRequestURL(raw)
		require.Equal(t, want[0], brand, raw)
		require.Equal(t, want[1], tag, raw)
	}
}
