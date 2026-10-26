package genuinhb

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"

	"github.com/prebid/openrtb/v20/adcom1"
	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/prebid/prebid-server/v4/util/ptrutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gamVideoImp(id string, startDelay *adcom1.StartDelay) openrtb2.Imp {
	return openrtb2.Imp{ID: id, Video: &openrtb2.Video{MIMEs: []string{"video/mp4"}, W: ptrutil.ToPtr[int64](640), H: ptrutil.ToPtr[int64](480), StartDelay: startDelay}}
}

// Expected URLs are worked examples written by hand from the GAM tag parameter reference; query
// keys appear in sorted order because the builder encodes them with url.Values.
func TestBuildGAMTag(t *testing.T) {
	consent := "CPXxRfAPXxRfAAfKABENB-CgAAAAAAAAAAYgAAAAAAAA"
	tests := []struct {
		name     string
		params   openrtb_ext.ExtImpGenuinHB
		imp      openrtb2.Imp
		request  *openrtb2.BidRequest
		defaults []string
		want     string
		wantErr  string
	}{
		{
			name:     "rewarded 320x480 player offers the trafficked sizes from params",
			params:   openrtb_ext.ExtImpGenuinHB{AdUnitPath: "/21775744923/genuin/instream", CPM: 0.5, Sizes: []string{"640x480", "400x300"}},
			imp:      openrtb2.Imp{ID: "rw", Video: &openrtb2.Video{MIMEs: []string{"video/mp4"}, W: ptrutil.ToPtr[int64](320), H: ptrutil.ToPtr[int64](480)}},
			request:  &openrtb2.BidRequest{App: &openrtb2.App{Bundle: "com.begenuin.demo"}},
			defaults: []string{"1920x1080"},
			want:     "https://pubads.g.doubleclick.net/gampad/ads?correlator=123456&env=vp&gdfp_req=1&impl=s&iu=%2F21775744923%2Fgenuin%2Finstream&output=xml_vast4&sz=320x480%7C640x480%7C400x300&unviewed_position_start=1&vpos=preroll",
		},
		{
			name:     "alias default sizes apply when params carry none; the imp size is never repeated",
			params:   openrtb_ext.ExtImpGenuinHB{AdUnitPath: "/21775744923/genuin/instream", CPM: 0.5},
			imp:      gamVideoImp("x", nil),
			request:  &openrtb2.BidRequest{App: &openrtb2.App{Bundle: "com.begenuin.demo"}},
			defaults: []string{"640x480", "400x300", "640x480"},
			want:     "https://pubads.g.doubleclick.net/gampad/ads?correlator=123456&env=vp&gdfp_req=1&impl=s&iu=%2F21775744923%2Fgenuin%2Finstream&output=xml_vast4&sz=640x480%7C400x300&unviewed_position_start=1&vpos=preroll",
		},
		{
			name:    "malformed size in params is rejected",
			params:  openrtb_ext.ExtImpGenuinHB{AdUnitPath: "/21775744923/genuin/instream", CPM: 0.5, Sizes: []string{"640x480", "wide"}},
			imp:     gamVideoImp("x", nil),
			request: &openrtb2.BidRequest{App: &openrtb2.App{Bundle: "com.begenuin.demo"}},
			wantErr: `sizes entry "wide" is not WxH`,
		},
		{
			name:   "app pre-roll with consent and cust params",
			params: openrtb_ext.ExtImpGenuinHB{AdUnitPath: "/21775744923/genuin/instream", CPM: 0.5, CustParams: map[string]string{"tier": "premium", "gn_placement": "pre-1"}},
			imp:    gamVideoImp("pre-1", ptrutil.ToPtr(adcom1.StartDelay(0))),
			request: &openrtb2.BidRequest{
				App:    &openrtb2.App{Bundle: "com.begenuin.demo", StoreURL: "https://apps.apple.com/app/id000000000"},
				Device: &openrtb2.Device{Lmt: ptrutil.ToPtr[int8](0)},
				Regs:   &openrtb2.Regs{GDPR: ptrutil.ToPtr[int8](1), USPrivacy: "1YNN"},
				User:   &openrtb2.User{Consent: consent},
			},
			want: "https://pubads.g.doubleclick.net/gampad/ads?correlator=123456&cust_params=gn_placement%3Dpre-1%26tier%3Dpremium&description_url=https%3A%2F%2Fapps.apple.com%2Fapp%2Fid000000000&env=vp&gdfp_req=1&gdpr=1&gdpr_consent=" + consent + "&impl=s&iu=%2F21775744923%2Fgenuin%2Finstream&output=xml_vast4&sz=640x480&unviewed_position_start=1&url=https%3A%2F%2Fapps.apple.com%2Fapp%2Fid000000000&us_privacy=1YNN&vpos=preroll",
		},
		{
			name:    "site mid-roll without consent",
			params:  openrtb_ext.ExtImpGenuinHB{AdUnitPath: "/21775744923/genuin/instream", CPM: 0.5},
			imp:     gamVideoImp("mid-1", ptrutil.ToPtr(adcom1.StartDelay(-1))),
			request: &openrtb2.BidRequest{Site: &openrtb2.Site{Page: "https://www.begenuin.com/watch/demo?x=1"}},
			want:    "https://pubads.g.doubleclick.net/gampad/ads?correlator=123456&description_url=https%3A%2F%2Fwww.begenuin.com%2Fwatch%2Fdemo%3Fx%3D1&env=vp&gdfp_req=1&impl=s&iu=%2F21775744923%2Fgenuin%2Finstream&output=xml_vast4&sz=640x480&unviewed_position_start=1&url=https%3A%2F%2Fwww.begenuin.com%2Fwatch%2Fdemo%3Fx%3D1&vpos=midroll",
		},
		{
			name:   "post-roll, description override, lmt and gpp",
			params: openrtb_ext.ExtImpGenuinHB{AdUnitPath: "/21775744923/genuin/instream", CPM: 0.5, DescriptionURL: "https://www.begenuin.com/shows/demo"},
			imp:    gamVideoImp("post-1", ptrutil.ToPtr(adcom1.StartDelay(-2))),
			request: &openrtb2.BidRequest{
				Site:   &openrtb2.Site{Page: "https://www.begenuin.com/watch/demo"},
				Device: &openrtb2.Device{Lmt: ptrutil.ToPtr[int8](1)},
				Regs:   &openrtb2.Regs{GPP: "DBABMA~CPXxRfAPXxRfAAfKABENB-CgAAAAAAAAAAYgAAAAAAAA", GPPSID: []int8{2, 6}},
			},
			want: "https://pubads.g.doubleclick.net/gampad/ads?correlator=123456&description_url=https%3A%2F%2Fwww.begenuin.com%2Fshows%2Fdemo&env=vp&gdfp_req=1&gpp=DBABMA~CPXxRfAPXxRfAAfKABENB-CgAAAAAAAAAAYgAAAAAAAA&gpp_sid=2%2C6&impl=s&iu=%2F21775744923%2Fgenuin%2Finstream&npa=1&output=xml_vast4&sz=640x480&unviewed_position_start=1&url=https%3A%2F%2Fwww.begenuin.com%2Fshows%2Fdemo&vpos=postroll",
		},
		{
			name:    "coppa sets npa; no page or store url omits url params; nil startdelay is pre-roll; startdelay 30 is mid-roll",
			params:  openrtb_ext.ExtImpGenuinHB{AdUnitPath: "/21775744923/genuin/instream", CPM: 0.5},
			imp:     gamVideoImp("x", nil),
			request: &openrtb2.BidRequest{App: &openrtb2.App{Bundle: "com.begenuin.demo"}, Regs: &openrtb2.Regs{COPPA: 1}},
			want:    "https://pubads.g.doubleclick.net/gampad/ads?correlator=123456&env=vp&gdfp_req=1&impl=s&iu=%2F21775744923%2Fgenuin%2Finstream&npa=1&output=xml_vast4&sz=640x480&unviewed_position_start=1&vpos=preroll",
		},
		{
			name:    "cust params keep commas and encode spaces as %20",
			params:  openrtb_ext.ExtImpGenuinHB{AdUnitPath: "/21775744923/genuin/instream", CPM: 0.5, CustParams: map[string]string{"color": "red,blue", "k": "a b"}},
			imp:     gamVideoImp("x", nil),
			request: &openrtb2.BidRequest{App: &openrtb2.App{Bundle: "com.begenuin.demo"}},
			want:    "https://pubads.g.doubleclick.net/gampad/ads?correlator=123456&cust_params=color%3Dred%2Cblue%26k%3Da%20b&env=vp&gdfp_req=1&impl=s&iu=%2F21775744923%2Fgenuin%2Finstream&output=xml_vast4&sz=640x480&unviewed_position_start=1&vpos=preroll",
		},
		{
			name:    "base endpoint with its own query keeps it",
			params:  openrtb_ext.ExtImpGenuinHB{AdUnitPath: "/21775744923/genuin/instream", CPM: 0.5},
			imp:     gamVideoImp("x", nil),
			request: &openrtb2.BidRequest{App: &openrtb2.App{Bundle: "com.begenuin.demo"}},
			want:    "https://pubads.g.doubleclick.net/gampad/ads?correlator=123456&env=vp&gdfp_req=1&impl=s&iu=%2F21775744923%2Fgenuin%2Finstream&output=xml_vast4&sz=640x480&tfcd=0&unviewed_position_start=1&vpos=preroll",
		},
		{
			name:    "missing size is an error",
			params:  openrtb_ext.ExtImpGenuinHB{AdUnitPath: "/21775744923/genuin/instream", CPM: 0.5},
			imp:     openrtb2.Imp{ID: "x", Video: &openrtb2.Video{MIMEs: []string{"video/mp4"}}},
			request: &openrtb2.BidRequest{App: &openrtb2.App{Bundle: "com.begenuin.demo"}},
			wantErr: "video.w and video.h",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := gamEndpoint
			if test.name == "base endpoint with its own query keeps it" {
				base = gamEndpoint + "?tfcd=0"
			}
			got, err := buildGAMTag(base, &test.params, &test.imp, test.request, "123456", test.defaults)
			if test.wantErr != "" {
				assert.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestGAMWrapperVAST(t *testing.T) {
	tag := "https://pubads.g.doubleclick.net/gampad/ads?a=1&b=2"
	doc := gamWrapperVAST("gam-pre-1", tag)

	var v struct {
		XMLName xml.Name `xml:"VAST"`
		Version string   `xml:"version,attr"`
		Ads     []struct {
			ID      string    `xml:"id,attr"`
			InLine  *struct{} `xml:"InLine"`
			Wrapper *struct {
				AdSystem    string   `xml:"AdSystem"`
				TagURI      string   `xml:"VASTAdTagURI"`
				Impressions []string `xml:"Impression"`
			} `xml:"Wrapper"`
		} `xml:"Ad"`
	}
	require.NoError(t, xml.Unmarshal([]byte(doc), &v))
	assert.Equal(t, "4.0", v.Version)
	require.Len(t, v.Ads, 1)
	assert.Equal(t, "gam-pre-1", v.Ads[0].ID)
	assert.Nil(t, v.Ads[0].InLine)
	require.NotNil(t, v.Ads[0].Wrapper)
	assert.Equal(t, "GenuinHB", v.Ads[0].Wrapper.AdSystem)
	assert.Equal(t, tag, v.Ads[0].Wrapper.TagURI)
	assert.Equal(t, []string{""}, v.Ads[0].Wrapper.Impressions, "one empty Impression keeps the document valid until PBS injects its tracker")
	assert.Contains(t, doc, "<![CDATA["+tag+"]]>", "the tag must travel in CDATA, unescaped")

	info, err := parseVAST([]byte(doc))
	require.NoError(t, err)
	assert.True(t, info.hasAd)
	assert.Equal(t, "gam-pre-1", info.adID)
}

// TestGAMCorrelatorPerRequest: GAM wants one correlator per page view, which here is one auction
// request: every imp of a request shares it, and a second request gets a new one.
func TestGAMCorrelatorPerRequest(t *testing.T) {
	a := newTestAdapter(t, gamEndpoint, `{"type": "gam-wrapper"}`)
	params := json.RawMessage(`{"bidder":{"adUnitPath":"/21775744923/genuin/instream","cpm":0.5}}`)
	imp := func(id string) openrtb2.Imp { i := gamVideoImp(id, nil); i.Ext = params; return i }
	request := &openrtb2.BidRequest{ID: "r1", App: &openrtb2.App{Bundle: "com.begenuin.demo"}, Imp: []openrtb2.Imp{imp("a"), imp("b"), imp("c")}}

	correlatorOf := func(uri string) string {
		u, err := url.Parse(uri)
		require.NoError(t, err)
		return u.Query().Get("correlator")
	}
	first, errs := a.MakeRequests(request, &adapters.ExtraRequestInfo{})
	require.Empty(t, errs)
	require.Len(t, first, 3)
	c := correlatorOf(first[0].Uri)
	assert.Regexp(t, `^[0-9]{1,20}$`, c)
	assert.Equal(t, c, correlatorOf(first[1].Uri))
	assert.Equal(t, c, correlatorOf(first[2].Uri))
	for _, r := range first {
		assert.True(t, r.NoRequest, "GAM wrapper requests must not hit the network")
	}

	second, errs := a.MakeRequests(request, &adapters.ExtraRequestInfo{})
	require.Empty(t, errs)
	assert.NotEqual(t, c, correlatorOf(second[0].Uri))
}

// TestRaceConcurrentGAMWrapper: many placements through one shared gam-wrapper instance at once.
func TestRaceConcurrentGAMWrapper(t *testing.T) {
	a := newTestAdapter(t, gamEndpoint, `{"type": "gam-wrapper"}`)
	const workers = 64
	var wg sync.WaitGroup
	failures := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := fmt.Sprintf("/21775744923/genuin/placement%d", i)
			imp := gamVideoImp(fmt.Sprintf("imp-%d", i), nil)
			imp.Ext = json.RawMessage(fmt.Sprintf(`{"bidder":{"adUnitPath":%q,"cpm":%d.5}}`, path, i+1))
			request := &openrtb2.BidRequest{ID: "r", App: &openrtb2.App{Bundle: "com.begenuin.demo"}, Imp: []openrtb2.Imp{imp}}
			reqs, errs := a.MakeRequests(request, &adapters.ExtraRequestInfo{})
			if len(errs) > 0 || len(reqs) != 1 {
				failures <- fmt.Errorf("worker %d: MakeRequests: %d requests, errors %v", i, len(reqs), errs)
				return
			}
			u, _ := url.Parse(reqs[0].Uri)
			if got := u.Query().Get("iu"); got != path {
				failures <- fmt.Errorf("worker %d: iu %q, want %q", i, got, path)
				return
			}
			resp, errs := a.MakeBids(request, reqs[0], &adapters.ResponseData{StatusCode: 200, Body: []byte{}})
			if len(errs) > 0 || resp == nil || len(resp.Bids) != 1 {
				failures <- fmt.Errorf("worker %d: MakeBids failed: %v", i, errs)
				return
			}
			if got := resp.Bids[0].Bid.CrID; got != "gam:"+path {
				failures <- fmt.Errorf("worker %d: crid %q, want %q", i, got, "gam:"+path)
			}
			if got := resp.Bids[0].Bid.Price; got != float64(i+1)+0.5 {
				failures <- fmt.Errorf("worker %d: price %v", i, got)
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
