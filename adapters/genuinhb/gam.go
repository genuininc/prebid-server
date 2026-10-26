package genuinhb

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/prebid/openrtb/v20/adcom1"
	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters"
	"github.com/prebid/prebid-server/v4/errortypes"
	"github.com/prebid/prebid-server/v4/macros"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
)

// gamSource is the gam-wrapper source: a VAST wrapper bid around a Google Ad Manager tag, built
// from the provider's params and the request, with no network call (docs/adr/0002, 0004).
type gamSource struct {
	// baseURL is the GAM tag base; every parameter is appended by buildGAMTag.
	baseURL string
	// defaultSizes are the extra slot sizes offered when the params carry no sizes (host config).
	defaultSizes []string
	// newCorrelator returns the GAM correlator for one auction request; tests inject a fixed value.
	newCorrelator func() string
}

// sizePattern is the "WxH" form of a GAM slot size; it mirrors the params schema.
var sizePattern = regexp.MustCompile(`^[0-9]{1,5}x[0-9]{1,5}$`)

func newGAMSource(endpointTemplate *template.Template, defaultSizes []string) (*gamSource, error) {
	u, err := resolveURL(endpointTemplate, macros.EndpointTemplateParams{})
	if err != nil {
		return nil, err
	}
	return &gamSource{baseURL: u.String(), defaultSizes: defaultSizes, newCorrelator: defaultCorrelator}, nil
}

// slotSizes builds GAM's sz list: the imp's own size first, then the configured sizes (params, else
// the alias default), without duplicates. GAM fills when any listed size matches a trafficked creative.
func slotSizes(w, h int64, params []string, defaults []string) string {
	own := strconv.FormatInt(w, 10) + "x" + strconv.FormatInt(h, 10)
	extra := params
	if len(extra) == 0 {
		extra = defaults
	}
	list := []string{own}
	for _, s := range extra {
		if s != own && !containsString(list, s) {
			list = append(list, s)
		}
	}
	return strings.Join(list, "|")
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// defaultCorrelator returns a random GAM correlator; GAM wants the same value for every ad request
// of one page view, which here is one auction request.
func defaultCorrelator() string {
	return strconv.FormatUint(rand.Uint64(), 10)
}

// GAM tag parameters fixed by the gam-wrapper source. See
// https://support.google.com/admanager/answer/10678356 for their meaning.
const (
	gamOutput = "xml_vast4"
	gamEnv    = "vp"

	vposPreroll  = "preroll"
	vposMidroll  = "midroll"
	vposPostroll = "postroll"
)

// buildGAMTag builds the Google Ad Manager VAST tag for one video impression from the provider's
// params and the request. base is the alias endpoint; a query string on it is preserved.
// correlator must be the same for every impression of one auction request.
func buildGAMTag(base string, p *openrtb_ext.ExtImpGenuinHB, imp *openrtb2.Imp, req *openrtb2.BidRequest, correlator string, defaultSizes []string) (string, error) {
	if imp.Video == nil || imp.Video.W == nil || imp.Video.H == nil || *imp.Video.W <= 0 || *imp.Video.H <= 0 {
		return "", fmt.Errorf("imp %q: video.w and video.h are required to build a GAM tag", imp.ID)
	}
	for _, size := range p.Sizes {
		if !sizePattern.MatchString(size) {
			return "", fmt.Errorf("imp %q: sizes entry %q is not WxH", imp.ID, size)
		}
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid GAM endpoint: %w", err)
	}

	q := u.Query()
	q.Set("iu", p.AdUnitPath)
	q.Set("sz", slotSizes(*imp.Video.W, *imp.Video.H, p.Sizes, defaultSizes))
	q.Set("output", gamOutput)
	q.Set("env", gamEnv)
	q.Set("gdfp_req", "1")
	q.Set("impl", "s")
	q.Set("unviewed_position_start", "1")
	q.Set("correlator", correlator)
	q.Set("vpos", vposFor(imp.Video.StartDelay))
	if d := descriptionURL(p, req); d != "" {
		q.Set("description_url", d)
		q.Set("url", d)
	}

	c := consentFrom(req)
	if c.gdpr != "" {
		q.Set("gdpr", c.gdpr)
	}
	if c.tcString != "" {
		q.Set("gdpr_consent", c.tcString)
	}
	if c.usPrivacy != "" {
		q.Set("us_privacy", c.usPrivacy)
	}
	if c.gpp != "" {
		q.Set("gpp", c.gpp)
	}
	if c.gppSID != "" {
		q.Set("gpp_sid", c.gppSID)
	}
	if nonPersonalized(req) {
		q.Set("npa", "1")
	}
	if len(p.CustParams) > 0 {
		q.Set("cust_params", encodeCustParams(p.CustParams))
	}

	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	return u.String(), nil
}

// vposFor maps startdelay to GAM's vpos. An absent startdelay is treated as pre-roll: the stored
// imps always carry it, and a missing value is the pre-roll default in the Genuin placement model.
func vposFor(startDelay *adcom1.StartDelay) string {
	if startDelay == nil {
		return vposPreroll
	}
	switch *startDelay {
	case 0:
		return vposPreroll
	case adcom1.StartPostRoll:
		return vposPostroll
	default:
		return vposMidroll
	}
}

func descriptionURL(p *openrtb_ext.ExtImpGenuinHB, req *openrtb2.BidRequest) string {
	if p.DescriptionURL != "" {
		return p.DescriptionURL
	}
	if req.Site != nil && req.Site.Page != "" {
		return req.Site.Page
	}
	if req.App != nil && req.App.StoreURL != "" {
		return req.App.StoreURL
	}
	return ""
}

type consentSignals struct {
	gdpr      string
	tcString  string
	usPrivacy string
	gpp       string
	gppSID    string
}

// consentFrom reads the OpenRTB 2.6 consent fields. PBS moves the 2.5 ext locations (regs.ext.gdpr,
// regs.ext.us_privacy, user.ext.consent) into them before any adapter runs (openrtb_ext.ConvertUpTo26).
func consentFrom(req *openrtb2.BidRequest) consentSignals {
	var c consentSignals
	if req.Regs != nil {
		if req.Regs.GDPR != nil {
			c.gdpr = strconv.Itoa(int(*req.Regs.GDPR))
		}
		c.usPrivacy = req.Regs.USPrivacy
		c.gpp = req.Regs.GPP
		if len(req.Regs.GPPSID) > 0 {
			ids := make([]string, len(req.Regs.GPPSID))
			for i, id := range req.Regs.GPPSID {
				ids[i] = strconv.Itoa(int(id))
			}
			c.gppSID = strings.Join(ids, ",")
		}
	}
	if req.User != nil {
		c.tcString = req.User.Consent
	}
	return c
}

func nonPersonalized(req *openrtb2.BidRequest) bool {
	if req.Device != nil && req.Device.Lmt != nil && *req.Device.Lmt == 1 {
		return true
	}
	return req.Regs != nil && req.Regs.COPPA == 1
}

// encodeCustParams renders key-values as GAM documents them: "k1=v1&k2=v2", which the outer query
// encoding escapes exactly once (cust_params=k1%3Dv1%26k2%3Dv2). Values keep commas, GAM's
// multi-value separator; the params schema forbids "&" and "=" inside keys and values.
func encodeCustParams(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params[k])
	}
	return strings.Join(parts, "&")
}

var xmlAttrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

// gamWrapperVAST renders the bid's creative: a VAST 4.0 wrapper around the GAM tag. The empty
// Impression keeps the document valid when event tracking is off; with it on, PBS injects its
// own tracker (modifyingVastXmlAllowed).
func gamWrapperVAST(adID, tag string) string {
	var b strings.Builder
	b.Grow(len(tag) + 256)
	b.WriteString(`<VAST version="4.0"><Ad id="`)
	b.WriteString(xmlAttrEscaper.Replace(adID))
	b.WriteString(`"><Wrapper><AdSystem>GenuinHB</AdSystem><VASTAdTagURI><![CDATA[`)
	b.WriteString(strings.ReplaceAll(tag, "]]>", "]]]]><![CDATA[>"))
	b.WriteString(`]]></VASTAdTagURI><Impression></Impression><Creatives></Creatives></Wrapper></Ad></VAST>`)
	return b.String()
}

// makeRequests returns one no-network "request" per video impression whose URI is the GAM tag; the
// exchange hands it straight to makeBids (docs/adr/0004). Invalid impressions are reported and skipped.
func (s *gamSource) makeRequests(request *openrtb2.BidRequest) ([]*adapters.RequestData, []error) {
	correlator := s.newCorrelator()
	requests := make([]*adapters.RequestData, 0, len(request.Imp))
	var errs []error

	for i := range request.Imp {
		imp := &request.Imp[i]
		if !supportsMedia(imp) {
			errs = append(errs, unsupportedMedia(i))
			continue
		}

		params, err := parseGAMParams(i, imp)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		tag, err := buildGAMTag(s.baseURL, params, imp, request, correlator, s.defaultSizes)
		if err != nil {
			errs = append(errs, &errortypes.Warning{
				WarningCode: errortypes.UnknownWarningCode,
				Message:     fmt.Sprintf("imp[%d]: no GAM wrapper bid: %v", i, err),
			})
			continue
		}

		requests = append(requests, &adapters.RequestData{
			Method:    http.MethodGet,
			Uri:       tag,
			ImpIDs:    []string{imp.ID},
			NoRequest: true,
		})
	}

	return requests, errs
}

// parseGAMParams decodes and validates the gam-wrapper shape of the params.
func parseGAMParams(index int, imp *openrtb2.Imp) (*openrtb_ext.ExtImpGenuinHB, error) {
	params, err := decodeImpParams(index, imp)
	if err != nil {
		return nil, err
	}
	if params.AdUnitPath == "" || params.CPM <= 0 {
		return nil, &errortypes.BadInput{
			Message: fmt.Sprintf("imp[%d]: adUnitPath is required and cpm must be greater than 0 for the %s source", index, sourceGAMWrapper),
		}
	}
	if params.CustomerID != "" || params.TagID != "" {
		return nil, &errortypes.BadInput{
			Message: fmt.Sprintf("imp[%d]: customerId and tagId are not valid for the %s source", index, sourceGAMWrapper),
		}
	}
	return params, nil
}

// makeBids turns the synthetic response for one GAM tag into the GAM wrapper bid. The status checks
// only matter if the exchange ever stops synthesising a 200 for a NoRequest request.
func (s *gamSource) makeBids(request *openrtb2.BidRequest, requestData *adapters.RequestData, responseData *adapters.ResponseData) (*adapters.BidderResponse, []error) {
	if ok, err := checkResponse(responseData); !ok {
		if err != nil {
			return nil, []error{err}
		}
		return nil, nil
	}
	if requestData.Uri == "" {
		return nil, []error{&errortypes.BadInput{Message: "genuinhb gam-wrapper response has no tag URI"}}
	}

	index, imp, err := findImp(request, requestData)
	if err != nil {
		return nil, []error{err}
	}
	params, err := parseGAMParams(index, imp)
	if err != nil {
		return nil, []error{err}
	}

	adID := "gam-" + imp.ID
	bid := &openrtb2.Bid{
		ID:    adID,
		ImpID: imp.ID,
		Price: params.CPM,
		AdM:   gamWrapperVAST(adID, requestData.Uri),
		CrID:  "gam:" + params.AdUnitPath,
		MType: openrtb2.MarkupVideo,
	}
	if imp.Video != nil {
		if imp.Video.W != nil {
			bid.W = *imp.Video.W
		}
		if imp.Video.H != nil {
			bid.H = *imp.Video.H
		}
	}

	bidResponse := adapters.NewBidderResponseWithBidsCapacity(1)
	bidResponse.Currency = defaultCurrency
	bidResponse.Bids = append(bidResponse.Bids, &adapters.TypedBid{Bid: bid, BidType: openrtb_ext.BidTypeVideo})
	return bidResponse, nil
}
