package triton

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/template"

	"github.com/buger/jsonparser"
	"github.com/prebid/openrtb/v20/adcom1"
	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters"
	"github.com/prebid/prebid-server/v4/errortypes"
	"github.com/prebid/prebid-server/v4/macros"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/prebid/prebid-server/v4/util/ptrutil"
)

// The Triton wrapper bid: an audio or video impression gets a VAST wrapper around dsp-service's VAST
// endpoint GET {vast_endpoint}/{brand}/{tag}?<context>, with no network call (docs/adr/0004, 0005).
// The device resolves it; dsp-service serves the tag's creative (its local rotation, or Triton
// server-side) with the request context resolved into the creative's pixel macros.

const (
	// defaultCPM is the wrapper bid's price when the placement carries no cpm.
	defaultCPM = 0.05

	// maxAdRequestURLLength keeps the ad request URL well under common URL limits.
	maxAdRequestURLLength = 4000

	// adSystem is the VAST AdSystem the wrapper reports.
	adSystem = "Triton"

	adTypePreroll  = "preroll"
	adTypeMidroll  = "midroll"
	adTypePostroll = "postroll"
)

// wrapperRequest returns the no-network "request" for one audio or video impression; its URI is the
// dsp-service VAST URL, which MakeBids wraps into the bid. Problems are warnings: the impression
// gets no bid and the rest of the request is unaffected.
func (a *adapter) wrapperRequest(index int, imp *openrtb2.Imp, params *openrtb_ext.ExtImpTriton, request *openrtb2.BidRequest) (*adapters.RequestData, error) {
	adRequestURL, err := a.buildAdRequestURL(params, imp, request)
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

// buildAdRequestURL builds dsp-service's VAST URL for one audio or video impression: the vast
// endpoint resolved with the brand and the ad tag, plus the request context as the pixel parameters
// dsp-service's creatives declare (req_id, imp_id, device, site or app, player, user, regs). Every
// parameter is sent only when its source is present; ip and ua are never sent, the device supplies
// its own.
func (a *adapter) buildAdRequestURL(p *openrtb_ext.ExtImpTriton, imp *openrtb2.Imp, req *openrtb2.BidRequest) (string, error) {
	brand := resolveBrand(p, req)
	if brand == "" {
		return "", fmt.Errorf("app.publisher.id or site.publisher.id (the brand) is required")
	}
	base, err := resolveURL(a.vastTemplate, macros.EndpointTemplateParams{AccountID: url.PathEscape(brand), AdUnit: url.PathEscape(p.TagID)})
	if err != nil {
		return "", err
	}

	q := url.Values{}
	q.Set("req_id", req.ID)
	q.Set("imp_id", imp.ID)
	setPlayerParams(q, imp)
	setInventoryParams(q, imp, req)
	setDeviceParams(q, req)
	setUserParams(q, req)
	setRegsParams(q, req)

	query := strings.ReplaceAll(q.Encode(), "+", "%20")
	if base.RawQuery != "" {
		query = base.RawQuery + "&" + query
	}
	base.RawQuery = query
	adRequestURL := base.String()
	if len(adRequestURL) > maxAdRequestURLLength {
		return "", fmt.Errorf("VAST URL is %d characters, more than the %d allowed", len(adRequestURL), maxAdRequestURLLength)
	}
	return adRequestURL, nil
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

// mediaFields is what the VAST URL reads from the impression's audio or video object. Video wins
// when both are present, matching the wrapper bid's type.
type mediaFields struct {
	startDelay *adcom1.StartDelay
	placement  adcom1.VideoPlacementSubtype
	w, h       *int64
}

func mediaOf(imp *openrtb2.Imp) mediaFields {
	switch {
	case imp.Video != nil:
		return mediaFields{imp.Video.StartDelay, imp.Video.Placement, imp.Video.W, imp.Video.H}
	case imp.Audio != nil:
		return mediaFields{startDelay: imp.Audio.StartDelay}
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

// setPlayerParams adds the player size, the break position and the placement type.
func setPlayerParams(q url.Values, imp *openrtb2.Imp) {
	m := mediaOf(imp)
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
}

// setInventoryParams adds the app's identity or the site's URLs.
func setInventoryParams(q url.Values, imp *openrtb2.Imp, req *openrtb2.BidRequest) {
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
}

// setDeviceParams adds the device description, its advertising id when tracking is allowed, and geo.
func setDeviceParams(q url.Values, req *openrtb2.BidRequest) {
	d := req.Device
	if d == nil {
		return
	}
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

// setUserParams adds the user id (never under limited tracking) and the TCF consent string.
func setUserParams(q url.Values, req *openrtb2.BidRequest) {
	if req.User == nil {
		return
	}
	if !trackingLimited(req) {
		setIfNotEmpty(q, "user_id", req.User.ID)
	}
	setIfNotEmpty(q, "user_ext_consent", req.User.Consent)
}

// setRegsParams forwards the OpenRTB 2.6 regulation fields under their pixel names.
func setRegsParams(q url.Values, req *openrtb2.BidRequest) {
	r := req.Regs
	if r == nil {
		return
	}
	if r.GDPR != nil {
		q.Set("regs_ext_gdpr", strconv.Itoa(int(*r.GDPR)))
	}
	setIfNotEmpty(q, "regs_ext_us_privacy", r.USPrivacy)
	setIfNotEmpty(q, "regs_ext_gpp", r.GPP)
	if len(r.GPPSID) > 0 {
		ids := make([]string, len(r.GPPSID))
		for i, id := range r.GPPSID {
			ids[i] = strconv.Itoa(int(id))
		}
		q.Set("regs_ext_gpp_sid", strings.Join(ids, ","))
	}
	if r.COPPA == 1 {
		q.Set("regs_coppa", "1")
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

// wrapperVAST renders the bid's creative: a VAST 4.0 wrapper around the VAST URL. The empty
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

// wrapperBid turns the synthetic response for one VAST URL into the Triton wrapper bid.
// The params were validated by MakeRequests; only the price and the station are read again.
func wrapperBid(imp *openrtb2.Imp, requestData *adapters.RequestData) (*adapters.BidderResponse, []error) {
	price, _ := jsonparser.GetFloat(imp.Ext, "bidder", "cpm")
	if price <= 0 {
		price = defaultCPM
	}
	stid, _ := jsonparser.GetInt(imp.Ext, "bidder", "stid")

	adID := "triton-" + imp.ID
	bid := &openrtb2.Bid{
		ID:    adID,
		ImpID: imp.ID,
		Price: price,
		AdM:   wrapperVAST(adID, requestData.Uri),
		CrID:  "triton:" + strconv.FormatInt(stid, 10),
	}
	bidType := openrtb_ext.BidTypeAudio
	bid.MType = openrtb2.MarkupAudio
	if imp.Video != nil {
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
