package triton

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/prebid/openrtb/v20/adcom1"
	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters"
	"github.com/prebid/prebid-server/v4/config"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/prebid/prebid-server/v4/util/ptrutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vastBase is where the default vast endpoint (the RTB endpoint with /rtb/ → /vast/) lands for brand 3250.
const vastBase = "https://dsp.example.test/goservices/dsp/vast/3250/6a3aa78ba0daccfd439648b8"

func testAdapter(t *testing.T) *adapter {
	t.Helper()
	bidder, err := Builder(openrtb_ext.BidderTriton, config.Adapter{Endpoint: testEndpoint}, testServer)
	require.NoError(t, err)
	return bidder.(*adapter)
}

func baseParams() *openrtb_ext.ExtImpTriton {
	return &openrtb_ext.ExtImpTriton{STID: 1443132, TagID: "6a3aa78ba0daccfd439648b8"}
}

func audioImp(startDelay *adcom1.StartDelay) *openrtb2.Imp {
	return &openrtb2.Imp{ID: "a-1", Audio: &openrtb2.Audio{MIMEs: []string{"audio/mpeg"}, StartDelay: startDelay}}
}

func appRequest() *openrtb2.BidRequest {
	return &openrtb2.BidRequest{ID: "r", App: &openrtb2.App{Bundle: "com.begenuin.demo", Publisher: &openrtb2.Publisher{ID: "3250"}}}
}

// query builds the VAST URL and returns its decoded query, so tests assert parameters, not encoding.
func query(t *testing.T, p *openrtb_ext.ExtImpTriton, imp *openrtb2.Imp, req *openrtb2.BidRequest) url.Values {
	t.Helper()
	adRequestURL, err := testAdapter(t).buildDSPVASTURL(p, imp, req)
	require.NoError(t, err)
	u, err := url.Parse(adRequestURL)
	require.NoError(t, err)
	assert.Equal(t, vastBase, u.Scheme+"://"+u.Host+u.Path)
	return u.Query()
}

func TestAdRequestURLIdentity(t *testing.T) {
	t.Run("req_id, imp_id, brand and tag in the path", func(t *testing.T) {
		q := query(t, baseParams(), audioImp(nil), appRequest())
		assert.Equal(t, "r", q.Get("req_id"))
		assert.Equal(t, "a-1", q.Get("imp_id"))
	})

	t.Run("brandId param wins over the publisher id in the path", func(t *testing.T) {
		p := baseParams()
		p.BrandID = "3252"
		adRequestURL, err := testAdapter(t).buildDSPVASTURL(p, audioImp(nil), appRequest())
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(adRequestURL, "https://dsp.example.test/goservices/dsp/vast/3252/6a3aa78ba0daccfd439648b8?"), adRequestURL)
	})

	t.Run("no brand anywhere: no URL", func(t *testing.T) {
		_, err := testAdapter(t).buildDSPVASTURL(baseParams(), audioImp(nil), &openrtb2.BidRequest{ID: "r", App: &openrtb2.App{}})
		require.Error(t, err)
	})

	t.Run("ip and ua are never sent", func(t *testing.T) {
		req := appRequest()
		req.Device = &openrtb2.Device{IP: "203.0.113.7", UA: "GenuinApp/1.0"}
		q := query(t, baseParams(), audioImp(nil), req)
		assert.False(t, q.Has("ip"))
		assert.False(t, q.Has("ua"))
	})
}

func TestAdRequestURLPlayer(t *testing.T) {
	tests := []struct {
		name       string
		startDelay *adcom1.StartDelay
		wantType   string
		wantDelay  string
	}{
		{name: "0 is pre-roll", startDelay: ptrutil.ToPtr(adcom1.StartPreRoll), wantType: "preroll", wantDelay: "0"},
		{name: "-1 is mid-roll", startDelay: ptrutil.ToPtr(adcom1.StartDelay(-1)), wantType: "midroll", wantDelay: "-1"},
		{name: "-2 is post-roll", startDelay: ptrutil.ToPtr(adcom1.StartPostRoll), wantType: "postroll", wantDelay: "-2"},
		{name: "offset is mid-roll", startDelay: ptrutil.ToPtr(adcom1.StartDelay(5)), wantType: "midroll", wantDelay: "5"},
		{name: "absent is mid-roll without startdelay_iv", startDelay: nil, wantType: "midroll", wantDelay: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := query(t, baseParams(), audioImp(tt.startDelay), appRequest())
			assert.Equal(t, tt.wantType, q.Get("ad_type"))
			assert.Equal(t, tt.wantDelay, q.Get("startdelay_iv"))
		})
	}

	t.Run("video size, placement and ad group from the imp", func(t *testing.T) {
		imp := &openrtb2.Imp{ID: "v", Video: &openrtb2.Video{W: ptrutil.ToPtr[int64](640), H: ptrutil.ToPtr[int64](480), Placement: adcom1.VideoPlacementInStream, StartDelay: ptrutil.ToPtr(adcom1.StartPreRoll)},
			Ext: json.RawMessage(`{"data":{"placementId":"pl-9"},"bidder":{"stid":1,"tagId":"t"}}`)}
		q := query(t, baseParams(), imp, appRequest())
		assert.Equal(t, "640", q.Get("player_width"))
		assert.Equal(t, "480", q.Get("player_height"))
		assert.Equal(t, "1", q.Get("placement_iv"))
		assert.Equal(t, "pl-9", q.Get("ad_group_id"))
	})

	t.Run("audio imp has no size or placement", func(t *testing.T) {
		q := query(t, baseParams(), audioImp(nil), appRequest())
		for _, k := range []string{"player_width", "player_height", "placement_iv", "ad_group_id"} {
			assert.False(t, q.Has(k), k)
		}
	})

	t.Run("mixed audio and video: the video object drives the URL", func(t *testing.T) {
		imp := &openrtb2.Imp{ID: "m",
			Audio: &openrtb2.Audio{StartDelay: ptrutil.ToPtr(adcom1.StartPostRoll)},
			Video: &openrtb2.Video{StartDelay: ptrutil.ToPtr(adcom1.StartPreRoll), W: ptrutil.ToPtr[int64](640), H: ptrutil.ToPtr[int64](480)}}
		q := query(t, baseParams(), imp, appRequest())
		assert.Equal(t, "preroll", q.Get("ad_type"))
		assert.Equal(t, "640", q.Get("player_width"))
	})
}

func TestAdRequestURLInventory(t *testing.T) {
	t.Run("app identity", func(t *testing.T) {
		req := appRequest()
		req.App.StoreURL = "https://apps.apple.com/app/id1"
		req.App.Name = "Genuin"
		req.App.Ver = "2.3.0"
		q := query(t, baseParams(), audioImp(nil), req)
		assert.Equal(t, "com.begenuin.demo", q.Get("bundle"))
		assert.Equal(t, "https://apps.apple.com/app/id1", q.Get("store_url"))
		assert.Equal(t, "Genuin", q.Get("app_name"))
		assert.Equal(t, "2.3.0", q.Get("app_version"))
		assert.False(t, q.Has("site_page"))
	})

	t.Run("site identity", func(t *testing.T) {
		req := &openrtb2.BidRequest{ID: "r", Site: &openrtb2.Site{Domain: "www.begenuin.com", Page: "https://www.begenuin.com/listen/demo", Ref: "https://news.example/", Publisher: &openrtb2.Publisher{ID: "3250"}}}
		q := query(t, baseParams(), audioImp(nil), req)
		assert.Equal(t, "https://www.begenuin.com/listen/demo", q.Get("site_page"))
		assert.Equal(t, "www.begenuin.com", q.Get("domain_s"))
		assert.Equal(t, "https://news.example/", q.Get("site_ref"))
		assert.False(t, q.Has("bundle"))
	})
}

func TestAdRequestURLDeviceUserRegs(t *testing.T) {
	const ifa = "6D92078A-8246-4BA4-AE5B-76104861E7DC"
	withDevice := func(lmt, dnt *int8) *openrtb2.BidRequest {
		req := appRequest()
		req.Device = &openrtb2.Device{OS: "iOS", OSV: "17.4", Make: "Apple", Language: "fr", DeviceType: adcom1.DevicePhone,
			ConnectionType: ptrutil.ToPtr(adcom1.ConnectionWIFI), IFA: ifa, Lmt: lmt, DNT: dnt,
			Geo: &openrtb2.Geo{Lat: ptrutil.ToPtr(40.7128), Lon: ptrutil.ToPtr(-74.006), Country: "USA", ZIP: "10001", Metro: "501"}}
		req.User = &openrtb2.User{ID: "u-42", Consent: "CPXxRfA"}
		return req
	}

	t.Run("device, geo, user and consent", func(t *testing.T) {
		q := query(t, baseParams(), audioImp(nil), withDevice(ptrutil.ToPtr[int8](0), ptrutil.ToPtr[int8](0)))
		assert.Equal(t, "ios", q.Get("os"))
		assert.Equal(t, "17.4", q.Get("osv"))
		assert.Equal(t, "Apple", q.Get("device_make"))
		assert.Equal(t, "fr", q.Get("device_language"))
		assert.Equal(t, "4", q.Get("device_type"))
		assert.Equal(t, "2", q.Get("connectiontype_d"))
		assert.Equal(t, "0", q.Get("dnt"))
		assert.Equal(t, "0", q.Get("is_lat"))
		assert.Equal(t, ifa, q.Get("did"))
		assert.Equal(t, "40.7128", q.Get("location_lat"))
		assert.Equal(t, "-74.006", q.Get("location_lon"))
		assert.Equal(t, "USA", q.Get("country_code"))
		assert.Equal(t, "10001", q.Get("zip"))
		assert.Equal(t, "501", q.Get("metro_dg"))
		assert.Equal(t, "u-42", q.Get("user_id"))
		assert.Equal(t, "CPXxRfA", q.Get("user_ext_consent"))
	})

	t.Run("limited tracking drops did and user_id, keeps the flags", func(t *testing.T) {
		for name, req := range map[string]*openrtb2.BidRequest{
			"lmt": withDevice(ptrutil.ToPtr[int8](1), nil),
			"dnt": withDevice(nil, ptrutil.ToPtr[int8](1)),
		} {
			q := query(t, baseParams(), audioImp(nil), req)
			assert.False(t, q.Has("did"), name)
			assert.False(t, q.Has("user_id"), name)
			assert.Equal(t, "CPXxRfA", q.Get("user_ext_consent"), name)
		}
		req := withDevice(nil, nil)
		req.Regs = &openrtb2.Regs{COPPA: 1}
		q := query(t, baseParams(), audioImp(nil), req)
		assert.False(t, q.Has("did"))
		assert.Equal(t, "1", q.Get("regs_coppa"))
	})

	t.Run("regs", func(t *testing.T) {
		req := appRequest()
		req.Regs = &openrtb2.Regs{GDPR: ptrutil.ToPtr[int8](1), USPrivacy: "1YNN", GPP: "DBABMA~tc", GPPSID: []int8{2, 6}}
		q := query(t, baseParams(), audioImp(nil), req)
		assert.Equal(t, "1", q.Get("regs_ext_gdpr"))
		assert.Equal(t, "1YNN", q.Get("regs_ext_us_privacy"))
		assert.Equal(t, "DBABMA~tc", q.Get("regs_ext_gpp"))
		assert.Equal(t, "2,6", q.Get("regs_ext_gpp_sid"))
		assert.False(t, q.Has("regs_coppa"))
	})

	t.Run("nothing optional when the request is bare", func(t *testing.T) {
		q := query(t, baseParams(), audioImp(nil), appRequest())
		assert.ElementsMatch(t, []string{"req_id", "imp_id", "ad_type", "bundle"}, keysOf(q))
	})
}

func keysOf(q url.Values) []string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	return keys
}

func TestAdRequestURLLength(t *testing.T) {
	req := appRequest()
	req.App.Name = strings.Repeat("k", 4100)
	_, err := testAdapter(t).buildDSPVASTURL(baseParams(), audioImp(nil), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "4000")
}

func TestVASTEndpointConfig(t *testing.T) {
	t.Run("derived from the RTB endpoint", func(t *testing.T) {
		q := query(t, baseParams(), audioImp(nil), appRequest()) // query asserts vastBase, the derived form
		assert.NotEmpty(t, q)
	})

	t.Run("override from extra_info", func(t *testing.T) {
		bidder, err := Builder(openrtb_ext.BidderTriton, config.Adapter{Endpoint: testEndpoint,
			ExtraAdapterInfo: `{"vast_endpoint": "https://vast.example.test/v/{{.AccountID}}/{{.AdUnit}}?src=pbs"}`}, testServer)
		require.NoError(t, err)
		adRequestURL, err := bidder.(*adapter).buildDSPVASTURL(baseParams(), audioImp(nil), appRequest())
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(adRequestURL, "https://vast.example.test/v/3250/6a3aa78ba0daccfd439648b8?src=pbs&"), adRequestURL)
	})

	t.Run("RTB endpoint without /rtb/ needs an explicit vast endpoint", func(t *testing.T) {
		_, err := Builder(openrtb_ext.BidderTriton, config.Adapter{Endpoint: "https://dsp.example.test/bid/{{.AccountID}}/{{.AdUnit}}"}, testServer)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "vast_endpoint")
		_, err = Builder(openrtb_ext.BidderTriton, config.Adapter{Endpoint: "https://dsp.example.test/bid/{{.AccountID}}/{{.AdUnit}}",
			ExtraAdapterInfo: `{"vast_endpoint": "https://dsp.example.test/vast/{{.AccountID}}/{{.AdUnit}}"}`}, testServer)
		require.NoError(t, err)
	})

	t.Run("vast endpoint must carry both macros", func(t *testing.T) {
		_, err := Builder(openrtb_ext.BidderTriton, config.Adapter{Endpoint: testEndpoint,
			ExtraAdapterInfo: `{"vast_endpoint": "https://dsp.example.test/vast/{{.AccountID}}"}`}, testServer)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "vast endpoint")
	})
}

func TestWrapperVAST(t *testing.T) {
	vast := wrapperVAST(`a"b`, "https://x.test/vast/1/t?req_id=1&q=]]>end")
	assert.Contains(t, vast, `<Ad id="a&quot;b">`)
	assert.Contains(t, vast, "<AdSystem>Triton</AdSystem>")
	assert.Contains(t, vast, "<![CDATA[https://x.test/vast/1/t?req_id=1&q=]]]]><![CDATA[>end]]>")
	assert.Contains(t, vast, "<Impression></Impression><Creatives></Creatives>")
}

func TestWrapperBidMixedMedia(t *testing.T) {
	imp := &openrtb2.Imp{ID: "m",
		Audio: &openrtb2.Audio{StartDelay: ptrutil.ToPtr(adcom1.StartPostRoll)},
		Video: &openrtb2.Video{StartDelay: ptrutil.ToPtr(adcom1.StartPreRoll), W: ptrutil.ToPtr[int64](640), H: ptrutil.ToPtr[int64](480)},
		Ext:   json.RawMessage(`{"bidder":{"stid":1443132,"tagId":"t","cpm":0.75}}`)}
	bids, errs := wrapperBid(imp, &openrtb_ext.ExtImpTriton{STID: 1443132, TagID: "t", CPM: 0.75}, &adapters.RequestData{Uri: vastBase + "?req_id=r", NoRequest: true})
	require.Empty(t, errs)
	require.Len(t, bids.Bids, 1)
	assert.Equal(t, openrtb_ext.BidTypeVideo, bids.Bids[0].BidType)
	assert.Equal(t, openrtb2.MarkupVideo, bids.Bids[0].Bid.MType)
	assert.Equal(t, int64(640), bids.Bids[0].Bid.W)
	assert.Equal(t, 0.75, bids.Bids[0].Bid.Price)
	assert.Equal(t, "triton:1443132", bids.Bids[0].Bid.CrID)
}

func TestRaceConcurrentMakeRequests(t *testing.T) {
	bidder, err := Builder(openrtb_ext.BidderTriton, config.Adapter{Endpoint: testEndpoint}, testServer)
	require.NoError(t, err)

	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tag := fmt.Sprintf("tag-%d", i)
			imp := openrtb2.Imp{ID: "a", Audio: &openrtb2.Audio{MIMEs: []string{"audio/mpeg"}},
				Ext: json.RawMessage(fmt.Sprintf(`{"bidder":{"stid":%d,"tagId":%q}}`, 1000+i, tag))}
			reqs, errs := bidder.MakeRequests(&openrtb2.BidRequest{ID: strconv.Itoa(i), App: &openrtb2.App{Publisher: &openrtb2.Publisher{ID: "3250"}}, Imp: []openrtb2.Imp{imp}}, nil)
			assert.Empty(t, errs)
			if assert.Len(t, reqs, 1) {
				u, err := url.Parse(reqs[0].Uri)
				assert.NoError(t, err)
				assert.True(t, strings.HasSuffix(u.Path, "/"+tag), u.Path)
				assert.Equal(t, strconv.Itoa(i), u.Query().Get("req_id"))
			}
		}(i)
	}
	wg.Wait()
}
