package triton

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/prebid/openrtb/v20/adcom1"
	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters"
	"github.com/prebid/prebid-server/v4/errortypes"
	"github.com/prebid/prebid-server/v4/macros"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/prebid/prebid-server/v4/util/ptrutil"
	"golang.org/x/text/language"
)

// The Triton wrapper bid: an audio or video impression gets a VAST wrapper bid with no network call
// (docs/adr/0004, 0005). The wrapper points at one of two targets, decided by the placement's params:
//   - baseUrl set: Triton's on-demand ad request, {baseUrl}?stid=…&type=…&<documented parameters>
//     built from the impression, the device and the user (Triton's on-demand ad request parameters).
//   - baseUrl absent: dsp-service's VAST endpoint {vast_endpoint}/{brand}/{tag}?<request context>,
//     which serves the ad tag's creative with the context resolved into its pixels.
// The device resolves the wrapper either way.

const (
	// defaultCPM is the wrapper bid's price when the placement carries no cpm.
	defaultCPM = 0.05

	// maxAdRequestURLLength keeps the ad request URL under Triton's 4096-character limit (414 beyond it).
	maxAdRequestURLLength = 4000

	// maxDistLength is Triton's limit for the dist parameter.
	maxDistLength = 50

	// maxAdsCap is Triton's upper bound for maxAds.
	maxAdsCap = 10

	// adSystem is the VAST AdSystem the wrapper reports.
	adSystem = "Triton"

	adTypePreroll  = "preroll"
	adTypeMidroll  = "midroll"
	adTypePostroll = "postroll"
)

// guidPattern is the lowercase RFC 4122 form Triton requires for cookie listener ids.
var guidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// wrapperRequest returns the no-network "request" for one audio or video impression; its URI is the
// ad request URL, which MakeBids wraps into the bid. Problems are warnings: the impression gets no
// bid and the rest of the request is unaffected.
func (a *adapter) wrapperRequest(index int, imp *openrtb2.Imp, params *openrtb_ext.ExtImpTriton, request *openrtb2.BidRequest) (*adapters.RequestData, error) {
	var adRequestURL string
	var err error
	if params.BaseURL != "" {
		adRequestURL, err = buildTritonURL(params, imp, request)
	} else {
		if err = requireTag(index, params); err != nil {
			return nil, err
		}
		adRequestURL, err = a.buildDSPVASTURL(params, imp, request)
	}
	if err != nil {
		return nil, &errortypes.Warning{
			WarningCode: errortypes.UnknownWarningCode,
			Message:     fmt.Sprintf("imp[%d]: no Triton wrapper bid: %v", index, err),
		}
	}
	return &adapters.RequestData{
		Method:    http.MethodGet,
		Uri:       adRequestURL,
		ImpIDs:    []string{imp.ID},
		NoRequest: true,
	}, nil
}

// finishURL appends the query to the base (keeping a query already on it) and enforces the length limit.
func finishURL(base *url.URL, q url.Values) (string, error) {
	query := strings.ReplaceAll(q.Encode(), "+", "%20")
	if base.RawQuery != "" {
		query = base.RawQuery + "&" + query
	}
	base.RawQuery = query
	adRequestURL := base.String()
	if len(adRequestURL) > maxAdRequestURLLength {
		return "", fmt.Errorf("ad request URL is %d characters, more than the %d allowed", len(adRequestURL), maxAdRequestURLLength)
	}
	return adRequestURL, nil
}

// ---------------------------------------------------------------------------------------------------
// Target 1: Triton's on-demand ad request (baseUrl set). Parameters and their meaning follow Triton's
// "On-demand ad request parameters" and "Advertising specification parameters" pages; nothing is sent
// that those pages do not document, and nothing without a source in the request. ip and ua are never
// sent: the device supplies its own when it fetches the wrapper.

// buildTritonURL builds {baseUrl}?stid=…&type=…&… for one audio or video impression.
func buildTritonURL(p *openrtb_ext.ExtImpTriton, imp *openrtb2.Imp, req *openrtb2.BidRequest) (string, error) {
	base, err := url.Parse(p.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return "", fmt.Errorf("baseUrl must be an absolute https URL")
	}
	m := mediaOf(p, imp)

	q := url.Values{}
	q.Set("stid", strconv.FormatInt(p.STID, 10))
	q.Set("type", adType(m.startDelay))
	q.Set("maxAds", strconv.FormatInt(maxAds(m.maxSeq), 10))
	if m.minDuration > 0 {
		q.Set("mindur", strconv.FormatInt(m.minDuration, 10))
	}
	if m.maxDuration > 0 {
		q.Set("maxdur", strconv.FormatInt(m.maxDuration, 10))
	}
	if len(m.delivery) > 0 {
		switch m.delivery[0] {
		case adcom1.DeliveryStreaming:
			q.Set("delivery-method", "streaming")
		case adcom1.DeliveryProgressive:
			q.Set("delivery-method", "progressive")
		case adcom1.DeliveryDownload:
			q.Set("delivery-method", "download")
		}
	}
	var sizes []string
	for _, c := range m.companions {
		if c.W != nil && c.H != nil && *c.W > 0 && *c.H > 0 {
			sizes = append(sizes, strconv.FormatInt(*c.W, 10)+"x"+strconv.FormatInt(*c.H, 10))
		}
	}
	if len(sizes) > 0 {
		q.Set("banners", strings.Join(sizes, ","))
	}
	setIfNotEmpty(q, "lsid", listenerID(req))
	if brand := resolveBrand(p, req); brand != "" {
		q.Set("ttag", "brand_id:"+brand)
	}
	setIfNotEmpty(q, "feed-type", p.FeedType)

	switch {
	case req.App != nil:
		if req.App.Bundle != "" {
			q.Set("bundle-id", req.App.Bundle)
			q.Set("store-id", req.App.Bundle)
			q.Set("dist", truncate(req.App.Bundle, maxDistLength))
		}
		setIfNotEmpty(q, "store-url", req.App.StoreURL)
	case req.Site != nil:
		switch {
		case req.Site.Page != "":
			q.Set("site-url", req.Site.Page)
			q.Set("pageURL", req.Site.Page)
		case req.Site.Domain != "":
			q.Set("site-url", "https://"+req.Site.Domain)
		}
		setIfNotEmpty(q, "referrer", firstNonEmpty(req.Site.Ref, req.Site.Page))
		if req.Site.Domain != "" {
			q.Set("dist", truncate(req.Site.Domain, maxDistLength))
		}
	}
	if d := req.Device; d != nil {
		setIfNotEmpty(q, "device-os", d.OS)
		setIfNotEmpty(q, "device-osv", d.OSV)
		setIfNotEmpty(q, "device-language", d.Language)
		if d.Geo != nil && d.Geo.Lat != nil && d.Geo.Lon != nil {
			q.Set("lat", strconv.FormatFloat(*d.Geo.Lat, 'f', -1, 64))
			q.Set("long", strconv.FormatFloat(*d.Geo.Lon, 'f', -1, 64))
		}
	}
	if content := contentOf(req); content != nil {
		setIfNotEmpty(q, "content-language", content.Language)
		if content.CatTax == adcom1.CatTaxIABContent20 && len(content.Cat) > 0 {
			q.Set("iab-v2-cat", strings.Join(content.Cat, ","))
		}
		setIfNotEmpty(q, "tags", content.Keywords)
	}
	if len(req.BCat) > 0 {
		q.Set("iab-categories-to-exclude", strings.Join(req.BCat, ","))
	}
	if u := req.User; u != nil {
		if u.Yob > 0 {
			q.Set("yob", strconv.FormatInt(u.Yob, 10))
			if age := int64(time.Now().Year()) - u.Yob; age > 0 {
				q.Set("age", strconv.FormatInt(age, 10))
			}
		}
		switch gender := strings.ToLower(u.Gender); gender {
		case "m", "f", "o":
			q.Set("gender", gender)
		}
		if u.Geo != nil && u.Geo.ZIP != "" {
			if country := alpha2Country(u.Geo.Country); country != "" {
				q.Set("postalcode", u.Geo.ZIP)
				q.Set("country", country)
			}
		}
		setIfNotEmpty(q, "gdpr_consent", u.Consent)
	}
	if r := req.Regs; r != nil {
		if r.GDPR != nil {
			q.Set("gdpr", strconv.Itoa(int(*r.GDPR)))
		}
		setIfNotEmpty(q, "us_privacy", r.USPrivacy)
		setIfNotEmpty(q, "gpp", r.GPP)
		if len(r.GPPSID) > 0 {
			q.Set("gpp_sid", joinInt8(r.GPPSID))
		}
		if r.COPPA == 1 {
			q.Set("coppa", "1")
		}
	}
	return finishURL(base, q)
}

// maxAds is maxseq clamped to Triton's 1..10, default 1.
func maxAds(maxSeq int64) int64 {
	if maxSeq < 1 {
		return 1
	}
	if maxSeq > maxAdsCap {
		return maxAdsCap
	}
	return maxSeq
}

// listenerID applies Triton's lsid rule. Nothing is sent when tracking is limited (lmt, dnt, COPPA).
// Otherwise the advertising id with its platform prefix; else the user id as a cookie id, but only
// in the lowercase GUID form Triton accepts; else none.
func listenerID(req *openrtb2.BidRequest) string {
	if trackingLimited(req) {
		return ""
	}
	if req.Device != nil && req.Device.IFA != "" {
		return advertisingIDPrefix(req.Device.OS) + req.Device.IFA
	}
	if req.User != nil && guidPattern.MatchString(req.User.ID) {
		return "cookie:" + req.User.ID
	}
	return ""
}

// advertisingIDPrefix is the lsid type for the device's advertising id: Apple platforms carry an
// IDFA, Android a GAID, anything else Triton's generic app id.
func advertisingIDPrefix(os string) string {
	os = strings.ToLower(os)
	switch {
	case strings.Contains(os, "ios"), strings.Contains(os, "ipados"), strings.Contains(os, "tvos"), strings.Contains(os, "watchos"), strings.Contains(os, "macos"):
		return "idfa:"
	case strings.Contains(os, "android"):
		return "gaid:"
	default:
		return "app:"
	}
}

// alpha2Country turns an OpenRTB country (ISO 3166-1 alpha-3, alpha-2 tolerated) into Triton's alpha-2.
func alpha2Country(code string) string {
	region, err := language.ParseRegion(code)
	if err != nil || !region.IsCountry() {
		return ""
	}
	return region.String()
}

// contentOf is the inventory's content object: the app's, else the site's.
func contentOf(req *openrtb2.BidRequest) *openrtb2.Content {
	switch {
	case req.App != nil:
		return req.App.Content
	case req.Site != nil:
		return req.Site.Content
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func joinInt8(ids []int8) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(int(id))
	}
	return strings.Join(parts, ",")
}

// ---------------------------------------------------------------------------------------------------
// Target 2: dsp-service's VAST endpoint (baseUrl absent). The request context travels under the pixel
// parameter names dsp-service's creatives declare.

// buildDSPVASTURL builds {vast_endpoint}/{brand}/{tag}?<context> for one audio or video impression.
func (a *adapter) buildDSPVASTURL(p *openrtb_ext.ExtImpTriton, imp *openrtb2.Imp, req *openrtb2.BidRequest) (string, error) {
	brand := resolveBrand(p, req)
	if brand == "" {
		return "", fmt.Errorf("app.publisher.id or site.publisher.id (the brand) is required")
	}
	base, err := resolveURL(a.vastTemplate, macros.EndpointTemplateParams{AccountID: url.PathEscape(brand), AdUnit: url.PathEscape(p.TagID)})
	if err != nil {
		return "", err
	}
	m := mediaOf(p, imp)

	q := url.Values{}
	q.Set("req_id", req.ID)
	q.Set("imp_id", imp.ID)
	q.Set("ad_type", adType(m.startDelay))
	if m.startDelay != nil {
		q.Set("startdelay_iv", strconv.FormatInt(int64(*m.startDelay), 10))
	}
	if m.placement != 0 {
		q.Set("placement_iv", strconv.FormatInt(int64(m.placement), 10))
	}
	if m.w != nil && m.h != nil && *m.w > 0 && *m.h > 0 {
		q.Set("player_width", strconv.FormatInt(*m.w, 10))
		q.Set("player_height", strconv.FormatInt(*m.h, 10))
	}
	setIfNotEmpty(q, "ad_group_id", placementID(imp.Ext))
	switch {
	case req.App != nil:
		setIfNotEmpty(q, "bundle", req.App.Bundle)
		setIfNotEmpty(q, "store_url", req.App.StoreURL)
		setIfNotEmpty(q, "app_name", req.App.Name)
		setIfNotEmpty(q, "app_version", req.App.Ver)
	case req.Site != nil:
		setIfNotEmpty(q, "site_page", req.Site.Page)
		setIfNotEmpty(q, "domain_s", req.Site.Domain)
		setIfNotEmpty(q, "site_ref", req.Site.Ref)
	}
	if d := req.Device; d != nil {
		setIfNotEmpty(q, "os", strings.ToLower(d.OS))
		setIfNotEmpty(q, "osv", d.OSV)
		setIfNotEmpty(q, "device_make", d.Make)
		setIfNotEmpty(q, "device_language", d.Language)
		if d.DeviceType != 0 {
			q.Set("device_type", strconv.FormatInt(int64(d.DeviceType), 10))
		}
		if d.ConnectionType != nil {
			q.Set("connectiontype_d", strconv.FormatInt(int64(*d.ConnectionType), 10))
		}
		if d.DNT != nil {
			q.Set("dnt", strconv.Itoa(int(*d.DNT)))
		}
		if d.Lmt != nil {
			q.Set("is_lat", strconv.Itoa(int(*d.Lmt)))
		}
		if d.IFA != "" && !trackingLimited(req) {
			q.Set("did", d.IFA)
		}
		if g := d.Geo; g != nil {
			if g.Lat != nil && g.Lon != nil {
				q.Set("location_lat", strconv.FormatFloat(*g.Lat, 'f', -1, 64))
				q.Set("location_lon", strconv.FormatFloat(*g.Lon, 'f', -1, 64))
			}
			setIfNotEmpty(q, "country_code", g.Country)
			setIfNotEmpty(q, "zip", g.ZIP)
			setIfNotEmpty(q, "metro_dg", g.Metro)
		}
	}
	if u := req.User; u != nil {
		if !trackingLimited(req) {
			setIfNotEmpty(q, "user_id", u.ID)
		}
		setIfNotEmpty(q, "user_ext_consent", u.Consent)
	}
	if r := req.Regs; r != nil {
		if r.GDPR != nil {
			q.Set("regs_ext_gdpr", strconv.Itoa(int(*r.GDPR)))
		}
		setIfNotEmpty(q, "regs_ext_us_privacy", r.USPrivacy)
		setIfNotEmpty(q, "regs_ext_gpp", r.GPP)
		if len(r.GPPSID) > 0 {
			q.Set("regs_ext_gpp_sid", joinInt8(r.GPPSID))
		}
		if r.COPPA == 1 {
			q.Set("regs_coppa", "1")
		}
	}
	return finishURL(base, q)
}

// deriveVASTEndpoint is the default vast endpoint: the RTB endpoint with its /rtb/ segment replaced
// by /vast/, i.e. dsp-service's sibling route.
func deriveVASTEndpoint(rtbEndpoint string) (string, error) {
	if !strings.Contains(rtbEndpoint, "/rtb/") {
		return "", fmt.Errorf("endpoint has no /rtb/ segment to derive the vast endpoint from; set extra_info.vast_endpoint")
	}
	return strings.Replace(rtbEndpoint, "/rtb/", "/vast/", 1), nil
}

// parseVASTTemplate parses and validates the vast endpoint template like the RTB one.
func parseVASTTemplate(endpoint string) (*template.Template, error) {
	t, err := template.New("vastEndpointTemplate").Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("unable to parse vast endpoint url template: %w", err)
	}
	if err := validateEndpoint(t); err != nil {
		return nil, fmt.Errorf("vast endpoint: %w", err)
	}
	return t, nil
}

// ---------------------------------------------------------------------------------------------------
// Shared

// mediaFields is what the ad request reads from the audio or video object the bidder serves the
// impression as (the route: video before audio, each only when enabled).
type mediaFields struct {
	startDelay  *adcom1.StartDelay
	placement   adcom1.VideoPlacementSubtype
	w, h        *int64
	maxSeq      int64
	minDuration int64
	maxDuration int64
	delivery    []adcom1.DeliveryMethod
	companions  []openrtb2.Banner
}

func mediaOf(p *openrtb_ext.ExtImpTriton, imp *openrtb2.Imp) mediaFields {
	switch route(p, imp) {
	case routeVideo:
		v := imp.Video
		return mediaFields{v.StartDelay, v.Placement, v.W, v.H, v.MaxSeq, v.MinDuration, v.MaxDuration, v.Delivery, v.CompanionAd}
	case routeAudio:
		a := imp.Audio
		return mediaFields{startDelay: a.StartDelay, maxSeq: a.MaxSeq, minDuration: a.MinDuration, maxDuration: a.MaxDuration, delivery: a.Delivery, companions: a.CompanionAd}
	}
	return mediaFields{}
}

// adType maps startdelay to the break type: 0 pre-roll, -2 post-roll, anything else (a positive
// offset, -1 generic mid-roll, or absent) mid-roll.
func adType(startDelay *adcom1.StartDelay) string {
	if startDelay == nil {
		return adTypeMidroll
	}
	switch *startDelay {
	case adcom1.StartPreRoll:
		return adTypePreroll
	case adcom1.StartPostRoll:
		return adTypePostroll
	default:
		return adTypeMidroll
	}
}

// trackingLimited is true under limit-ad-tracking, do-not-track or COPPA: no device or user id then.
func trackingLimited(req *openrtb2.BidRequest) bool {
	if req.Device != nil {
		if req.Device.Lmt != nil && *req.Device.Lmt == 1 {
			return true
		}
		if req.Device.DNT != nil && *req.Device.DNT == 1 {
			return true
		}
	}
	return req.Regs != nil && req.Regs.COPPA == 1
}

func setIfNotEmpty(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}

var xmlAttrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

// wrapperVAST renders the bid's creative: a VAST 4.0 wrapper around the ad request URL. The empty
// Impression keeps the document valid when event tracking is off; with it on, PBS injects its own
// tracker into video bids (modifyingVastXmlAllowed).
func wrapperVAST(adID, adRequestURL string) string {
	var b strings.Builder
	b.Grow(len(adRequestURL) + 256)
	b.WriteString(`<VAST version="4.0"><Ad id="`)
	b.WriteString(xmlAttrEscaper.Replace(adID))
	b.WriteString(`"><Wrapper><AdSystem>` + adSystem + `</AdSystem><VASTAdTagURI><![CDATA[`)
	b.WriteString(strings.ReplaceAll(adRequestURL, "]]>", "]]]]><![CDATA[>"))
	b.WriteString(`]]></VASTAdTagURI><Impression></Impression><Creatives></Creatives></Wrapper></Ad></VAST>`)
	return b.String()
}

// wrapperBid turns the synthetic response for one ad request URL into the Triton wrapper bid, typed
// by the route the impression took.
func wrapperBid(imp *openrtb2.Imp, params *openrtb_ext.ExtImpTriton, requestData *adapters.RequestData) (*adapters.BidderResponse, []error) {
	price := params.CPM
	if price <= 0 {
		price = defaultCPM
	}
	adID := "triton-" + imp.ID
	bid := &openrtb2.Bid{
		ID:    adID,
		ImpID: imp.ID,
		Price: price,
		AdM:   wrapperVAST(adID, requestData.Uri),
		CrID:  "triton:" + strconv.FormatInt(params.STID, 10),
	}
	bidType := openrtb_ext.BidTypeAudio
	bid.MType = openrtb2.MarkupAudio
	if route(params, imp) == routeVideo {
		bidType = openrtb_ext.BidTypeVideo
		bid.MType = openrtb2.MarkupVideo
		bid.W = ptrutil.ValueOrDefault(imp.Video.W)
		bid.H = ptrutil.ValueOrDefault(imp.Video.H)
	}

	bidResponse := adapters.NewBidderResponseWithBidsCapacity(1)
	bidResponse.Currency = defaultCurrency
	bidResponse.Bids = append(bidResponse.Bids, &adapters.TypedBid{Bid: bid, BidType: bidType})
	return bidResponse, nil
}

// findImp returns the index and the impression a single-impression request was made for.
func findImp(request *openrtb2.BidRequest, requestData *adapters.RequestData) (int, *openrtb2.Imp, error) {
	if len(requestData.ImpIDs) == 1 {
		for i := range request.Imp {
			if request.Imp[i].ID == requestData.ImpIDs[0] {
				return i, &request.Imp[i], nil
			}
		}
	}
	return 0, nil, &errortypes.BadInput{
		Message: fmt.Sprintf("failed to find the impression for the triton response, impIDs %q", requestData.ImpIDs),
	}
}
