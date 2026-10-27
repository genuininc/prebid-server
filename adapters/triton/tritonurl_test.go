package triton

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/prebid/openrtb/v20/adcom1"
	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/prebid/prebid-server/v4/util/ptrutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const tritonBase = "https://cmod-na.live.streamtheworld.com/ondemand/ars"

func tritonParams() *openrtb_ext.ExtImpTriton {
	return &openrtb_ext.ExtImpTriton{STID: 1443132, BrandID: "3252", BaseURL: tritonBase}
}

// tq builds the Triton on-demand URL and returns its decoded query.
func tq(t *testing.T, p *openrtb_ext.ExtImpTriton, imp *openrtb2.Imp, req *openrtb2.BidRequest) url.Values {
	t.Helper()
	u, err := buildTritonURL(p, imp, req)
	require.NoError(t, err)
	parsed, err := url.Parse(u)
	require.NoError(t, err)
	assert.Equal(t, tritonBase, parsed.Scheme+"://"+parsed.Host+parsed.Path)
	return parsed.Query()
}

func TestTritonURLRequiredAndBreak(t *testing.T) {
	q := tq(t, tritonParams(), audioImp(ptrutil.ToPtr(adcom1.StartPreRoll)), appRequest())
	assert.Equal(t, "1443132", q.Get("stid"))
	assert.Equal(t, "preroll", q.Get("type"))
	assert.Equal(t, "1", q.Get("maxAds"))
	assert.Equal(t, "brand_id:3252", q.Get("ttag"))
	assert.False(t, q.Has("ip"))
	assert.False(t, q.Has("ua"))

	for sd, want := range map[int]string{-1: "midroll", -2: "postroll", 5: "midroll"} {
		assert.Equal(t, want, tq(t, tritonParams(), audioImp(ptrutil.ToPtr(adcom1.StartDelay(sd))), appRequest()).Get("type"), sd)
	}
	assert.Equal(t, "midroll", tq(t, tritonParams(), audioImp(nil), appRequest()).Get("type"))

	for maxSeq, want := range map[int64]string{0: "1", 3: "3", 11: "10"} {
		imp := audioImp(nil)
		imp.Audio.MaxSeq = maxSeq
		assert.Equal(t, want, tq(t, tritonParams(), imp, appRequest()).Get("maxAds"))
	}
}

func TestTritonURLImpressionParams(t *testing.T) {
	imp := &openrtb2.Imp{ID: "a", Audio: &openrtb2.Audio{MinDuration: 5, MaxDuration: 30, Delivery: []adcom1.DeliveryMethod{adcom1.DeliveryDownload},
		CompanionAd: []openrtb2.Banner{{W: ptrutil.ToPtr[int64](300), H: ptrutil.ToPtr[int64](250)}, {W: ptrutil.ToPtr[int64](728), H: ptrutil.ToPtr[int64](90)}}}}
	q := tq(t, tritonParams(), imp, appRequest())
	assert.Equal(t, "5", q.Get("mindur"))
	assert.Equal(t, "30", q.Get("maxdur"))
	assert.Equal(t, "download", q.Get("delivery-method"))
	assert.Equal(t, "300x250,728x90", q.Get("banners"))

	p := tritonParams()
	p.FeedType = "podcast"
	assert.Equal(t, "podcast", tq(t, p, audioImp(nil), appRequest()).Get("feed-type"))
}

func TestTritonURLInventoryDeviceContentUser(t *testing.T) {
	req := appRequest()
	req.App.StoreURL = "https://apps.apple.com/app/id1"
	req.App.Content = &openrtb2.Content{Language: "en", Cat: []string{"42", "43"}, CatTax: adcom1.CatTaxIABContent20, Keywords: "news,sports"}
	req.BCat = []string{"IAB25", "IAB26"}
	req.Device = &openrtb2.Device{OS: "iOS", OSV: "17.4", Language: "fr", IFA: "6D92078A-8246-4BA4-AE5B-76104861E7DC", Lmt: ptrutil.ToPtr[int8](0),
		Geo: &openrtb2.Geo{Lat: ptrutil.ToPtr(40.7128), Lon: ptrutil.ToPtr(-74.006)}}
	req.User = &openrtb2.User{Yob: 1990, Gender: "F", Consent: "CPXxRfA", Geo: &openrtb2.Geo{ZIP: "10001", Country: "USA"}}
	req.Regs = &openrtb2.Regs{GDPR: ptrutil.ToPtr[int8](1), USPrivacy: "1YNN", GPP: "DBABMA~tc", GPPSID: []int8{2, 6}}
	q := tq(t, tritonParams(), audioImp(nil), req)
	assert.Equal(t, "com.begenuin.demo", q.Get("bundle-id"))
	assert.Equal(t, "com.begenuin.demo", q.Get("store-id"))
	assert.Equal(t, "https://apps.apple.com/app/id1", q.Get("store-url"))
	assert.Equal(t, "com.begenuin.demo", q.Get("dist"))
	assert.Equal(t, "iOS", q.Get("device-os"))
	assert.Equal(t, "17.4", q.Get("device-osv"))
	assert.Equal(t, "fr", q.Get("device-language"))
	assert.Equal(t, "idfa:6D92078A-8246-4BA4-AE5B-76104861E7DC", q.Get("lsid"))
	assert.Equal(t, "40.7128", q.Get("lat"))
	assert.Equal(t, "-74.006", q.Get("long"))
	assert.Equal(t, "en", q.Get("content-language"))
	assert.Equal(t, "42,43", q.Get("iab-v2-cat"))
	assert.Equal(t, "news,sports", q.Get("tags"))
	assert.Equal(t, "IAB25,IAB26", q.Get("iab-categories-to-exclude"))
	assert.Equal(t, "1990", q.Get("yob"))
	age, _ := strconv.Atoi(q.Get("age"))
	assert.True(t, age >= 35 && age <= 40, age)
	assert.Equal(t, "f", q.Get("gender"))
	assert.Equal(t, "10001", q.Get("postalcode"))
	assert.Equal(t, "US", q.Get("country"))
	assert.Equal(t, "1", q.Get("gdpr"))
	assert.Equal(t, "CPXxRfA", q.Get("gdpr_consent"))
	assert.Equal(t, "1YNN", q.Get("us_privacy"))
	assert.Equal(t, "DBABMA~tc", q.Get("gpp"))
	assert.Equal(t, "2,6", q.Get("gpp_sid"))

	site := &openrtb2.BidRequest{ID: "r", Site: &openrtb2.Site{Domain: "www.begenuin.com", Page: "https://www.begenuin.com/listen", Ref: "https://news.example/", Publisher: &openrtb2.Publisher{ID: "1842"}}}
	q = tq(t, tritonParams(), audioImp(nil), site)
	assert.Equal(t, "https://www.begenuin.com/listen", q.Get("site-url"))
	assert.Equal(t, "https://www.begenuin.com/listen", q.Get("pageURL"))
	assert.Equal(t, "https://news.example/", q.Get("referrer"))
	assert.Equal(t, "www.begenuin.com", q.Get("dist"))
	assert.False(t, q.Has("bundle-id"))
}

func TestTritonURLListenerID(t *testing.T) {
	const ifa = "6D92078A-8246-4BA4-AE5B-76104861E7DC"
	const guid = "4fe8ac15-333e-4ce7-8e5c-febdd49ca0fb"
	dev := func(os string, lmt, dnt *int8) *openrtb2.BidRequest {
		req := appRequest()
		req.Device = &openrtb2.Device{OS: os, IFA: ifa, Lmt: lmt, DNT: dnt}
		return req
	}
	assert.Equal(t, "idfa:"+ifa, tq(t, tritonParams(), audioImp(nil), dev("iPadOS", nil, nil)).Get("lsid"))
	assert.Equal(t, "gaid:"+ifa, tq(t, tritonParams(), audioImp(nil), dev("Android", nil, nil)).Get("lsid"))
	assert.Equal(t, "app:"+ifa, tq(t, tritonParams(), audioImp(nil), dev("Roku", nil, nil)).Get("lsid"))
	assert.False(t, tq(t, tritonParams(), audioImp(nil), dev("iOS", ptrutil.ToPtr[int8](1), nil)).Has("lsid"))
	assert.False(t, tq(t, tritonParams(), audioImp(nil), dev("iOS", nil, ptrutil.ToPtr[int8](1))).Has("lsid"))
	req := appRequest()
	req.User = &openrtb2.User{ID: guid}
	assert.Equal(t, "cookie:"+guid, tq(t, tritonParams(), audioImp(nil), req).Get("lsid"))
	req.User.ID = "user-42"
	assert.False(t, tq(t, tritonParams(), audioImp(nil), req).Has("lsid"))
	req.User.ID = guid
	req.Regs = &openrtb2.Regs{COPPA: 1}
	assert.False(t, tq(t, tritonParams(), audioImp(nil), req).Has("lsid"))
}

func TestTritonURLBareAndLimit(t *testing.T) {
	q := tq(t, tritonParams(), audioImp(nil), &openrtb2.BidRequest{ID: "r", App: &openrtb2.App{}})
	assert.ElementsMatch(t, []string{"stid", "type", "maxAds", "ttag"}, keysOf(q))

	p := tritonParams()
	p.BaseURL = tritonBase + "?dist=genuin"
	u, err := buildTritonURL(p, audioImp(nil), appRequest())
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(u, tritonBase+"?dist=genuin&"), u)

	req := appRequest()
	req.App.Content = &openrtb2.Content{Keywords: strings.Repeat("k", 4100)}
	_, err = buildTritonURL(tritonParams(), audioImp(nil), req)
	require.Error(t, err)
}

func TestRouteAndFormats(t *testing.T) {
	on, off := ptrutil.ToPtr(true), ptrutil.ToPtr(false)
	all := &openrtb2.Imp{Video: &openrtb2.Video{}, Audio: &openrtb2.Audio{}, Banner: &openrtb2.Banner{}}
	tests := []struct {
		name    string
		formats *openrtb_ext.ExtImpTritonFormats
		want    impRoute
	}{
		{"no switches: video first", nil, routeVideo},
		{"empty object: video first", &openrtb_ext.ExtImpTritonFormats{}, routeVideo},
		{"video off: audio", &openrtb_ext.ExtImpTritonFormats{Video: off}, routeAudio},
		{"video+audio off: banner", &openrtb_ext.ExtImpTritonFormats{Video: off, Audio: off}, routeBanner},
		{"all off: none", &openrtb_ext.ExtImpTritonFormats{Video: off, Audio: off, Banner: off}, routeNone},
		{"explicit on", &openrtb_ext.ExtImpTritonFormats{Video: on, Audio: on, Banner: on}, routeVideo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, route(&openrtb_ext.ExtImpTriton{Formats: tt.formats}, all))
		})
	}
	assert.Equal(t, routeNone, route(&openrtb_ext.ExtImpTriton{Formats: &openrtb_ext.ExtImpTritonFormats{Banner: off}}, &openrtb2.Imp{Banner: &openrtb2.Banner{}}))
}
