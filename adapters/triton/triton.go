// Package triton implements the Triton bidder: Triton Digital demand inside the auction (docs/adr/0005).
//
// A banner impression becomes one OpenRTB POST to dsp-service's RTB endpoint
// {endpoint}/rtb/{brand}/{tag} carrying a request-level ext.cxr object; dsp-service runs the Triton
// auction server-side, prices the bid and returns the creative. An audio or video impression becomes
// a Triton wrapper bid: no network call, a VAST wrapper around dsp-service's VAST endpoint
// {vast_endpoint}/{brand}/{tag}?<request context>, which the device resolves (wrapper.go).
package triton

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/buger/jsonparser"
	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters"
	"github.com/prebid/prebid-server/v4/config"
	"github.com/prebid/prebid-server/v4/errortypes"
	"github.com/prebid/prebid-server/v4/macros"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/prebid/prebid-server/v4/util/jsonutil"
	"github.com/prebid/prebid-server/v4/util/ptrutil"
)

const (
	defaultCurrency = "USD"

	// auctionPriceMacro is the OpenRTB price macro dsp-service leaves in adm and the pixel URLs; the
	// exchange never resolves it, so the adapter does (the price is known once the bid is).
	auctionPriceMacro = "${AUCTION_PRICE}"

	// Render mode of a banner placement (params hostRender): who finishes the creative.
	renderServer = "server" // dsp-service, on its servers (default)
	renderClient = "client" // the device, from a boot creative dsp-service returns (post-auction, cxr.pa = 1)
)

// tagIDPattern mirrors the params schema: a tag id can never alter the RTB path.
var tagIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// tritonFeedTypes are the feed-type values Triton documents.
var tritonFeedTypes = map[string]bool{"podcast": true, "music": true, "radio": true, "game": true, "pureplay": true, "replay": true, "txt2speech": true}

// requireTag is the tag check for the paths that call dsp-service (RTB and VAST), where the ad tag is
// the path segment.
func requireTag(index int, params *openrtb_ext.ExtImpTriton) error {
	if params.TagID == "" {
		return &errortypes.BadInput{Message: fmt.Sprintf("imp[%d]: tagId is required (imp.ext.data.tagId from the SDK, the stored imp's triton.tagId, or the tag in its adRequestUrl)", index)}
	}
	return nil
}

type adapter struct {
	endpointTemplate *template.Template
	// vastTemplate is dsp-service's VAST endpoint (the wrapper path), same macros as the RTB one.
	vastTemplate *template.Template
	// giv and adSlots are the host-level ext.cxr values (extra_info); nil = not sent.
	giv     *float64
	adSlots *int
}

// extraInfo is the optional adapters.triton.extra_info JSON object.
type extraInfo struct {
	// GIV is the autoplay volume (0–1) dsp-service assumes for the placement; 0 means muted and
	// dsp-service answers no-bid, absent means its default (0.2).
	GIV *float64 `json:"giv"`
	// AdSlots is dsp-service's post-roll widget slot count; 0 or 1 turns the widget off.
	AdSlots *int `json:"ad_slots"`
	// VASTEndpoint is dsp-service's VAST endpoint template for audio and video impressions; defaults
	// to the RTB endpoint with /rtb/ replaced by /vast/.
	VASTEndpoint string `json:"vast_endpoint"`
}

// Builder builds a new instance of the Triton bidder for the given bidder name with the given config.
func Builder(bidderName openrtb_ext.BidderName, config config.Adapter, server config.Server) (adapters.Bidder, error) {
	if strings.TrimSpace(config.Endpoint) == "" {
		return nil, errors.New("endpoint is empty")
	}
	endpointTemplate, err := template.New("endpointTemplate").Parse(config.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("unable to parse endpoint url template: %w", err)
	}
	if err := validateEndpoint(endpointTemplate); err != nil {
		return nil, err
	}
	info, err := parseExtraInfo(config.ExtraAdapterInfo)
	if err != nil {
		return nil, err
	}
	vastEndpoint := info.VASTEndpoint
	if strings.TrimSpace(vastEndpoint) == "" {
		if vastEndpoint, err = deriveVASTEndpoint(config.Endpoint); err != nil {
			return nil, err
		}
	}
	vastTemplate, err := parseVASTTemplate(vastEndpoint)
	if err != nil {
		return nil, err
	}
	return &adapter{endpointTemplate: endpointTemplate, vastTemplate: vastTemplate, giv: info.GIV, adSlots: info.AdSlots}, nil
}

// validateEndpoint requires an absolute http(s) URL whose path carries both the brand ({{.AccountID}})
// and the ad tag ({{.AdUnit}}) macros, so a host-config mistake cannot silently drop either.
func validateEndpoint(endpointTemplate *template.Template) error {
	const brand, tag = "__brand__", "__tag__"
	u, err := resolveURL(endpointTemplate, macros.EndpointTemplateParams{AccountID: brand, AdUnit: tag})
	if err != nil {
		return err
	}
	if !strings.Contains(u.Path, brand) || !strings.Contains(u.Path, tag) {
		return errors.New("endpoint path must contain both {{.AccountID}} (brand) and {{.AdUnit}} (tag)")
	}
	if strings.Contains(u.Host, brand) || strings.Contains(u.Host, tag) {
		return errors.New("endpoint host must not contain a macro")
	}
	return nil
}

// resolveURL resolves the endpoint template with params and requires an absolute http(s) URL with a host.
func resolveURL(endpointTemplate *template.Template, params macros.EndpointTemplateParams) (*url.URL, error) {
	resolved, err := macros.ResolveMacros(endpointTemplate, params)
	if err != nil {
		return nil, fmt.Errorf("unable to resolve endpoint url template: %w", err)
	}
	u, err := url.Parse(resolved)
	if err != nil {
		return nil, fmt.Errorf("endpoint is not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("endpoint must be an absolute http or https URL")
	}
	if u.Host == "" {
		return nil, errors.New("endpoint has no host")
	}
	return u, nil
}

// parseExtraInfo validates the host-level cxr values: giv in [0, 1], ad_slots >= 0.
func parseExtraInfo(raw string) (extraInfo, error) {
	var info extraInfo
	if strings.TrimSpace(raw) == "" {
		return info, nil
	}
	if err := jsonutil.Unmarshal([]byte(raw), &info); err != nil {
		return extraInfo{}, fmt.Errorf("invalid extra_info: %w", err)
	}
	if info.GIV != nil && (*info.GIV < 0 || *info.GIV > 1) {
		return extraInfo{}, errors.New("invalid extra_info: giv must be between 0 and 1")
	}
	if info.AdSlots != nil && *info.AdSlots < 0 {
		return extraInfo{}, errors.New("invalid extra_info: ad_slots must not be negative")
	}
	return info, nil
}

// MakeRequests routes every impression by media type and the placement's format switches: video or
// audio (when enabled) → a Triton wrapper bid (no network call); banner (when enabled) → one RTB
// request to dsp-service. A disabled bidder or a disabled format is skipped silently; an impression
// with no supported media type is reported.
func (a *adapter) MakeRequests(request *openrtb2.BidRequest, _ *adapters.ExtraRequestInfo) ([]*adapters.RequestData, []error) {
	requests := make([]*adapters.RequestData, 0, len(request.Imp))
	var errs []error

	for i := range request.Imp {
		imp := &request.Imp[i]
		params, err := parseParams(i, imp)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if params.Enabled != nil && !*params.Enabled {
			errs = append(errs, skipped(i, "the bidder is switched off for this placement (enabled: false)"))
			continue
		}
		if imp.Audio == nil && imp.Video == nil && imp.Banner == nil {
			errs = append(errs, &errortypes.BadInput{Message: fmt.Sprintf("imp[%d]: triton supports only banner, video and audio impressions", i)})
			continue
		}

		var rd *adapters.RequestData
		switch route(params, imp) {
		case routeVideo, routeAudio:
			rd, err = a.wrapperRequest(i, imp, params, request)
		case routeBanner:
			rd, err = a.rtbRequest(i, imp, params, request)
		default:
			errs = append(errs, skipped(i, fmt.Sprintf("%s is switched off for this placement (formats)", mediaTypes(imp))))
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		requests = append(requests, rd)
	}
	return requests, errs
}

// skipped is the warning for an impression the placement's switches exclude. A warning, not an
// error: nothing is wrong, and the exchange reports an adapter that returns neither requests nor
// errors as a failure.
func skipped(index int, why string) error {
	return &errortypes.Warning{WarningCode: errortypes.UnknownWarningCode, Message: fmt.Sprintf("imp[%d]: triton skipped: %s", index, why)}
}

// mediaTypes names the media types an impression carries, for messages.
func mediaTypes(imp *openrtb2.Imp) string {
	var names []string
	if imp.Video != nil {
		names = append(names, "video")
	}
	if imp.Audio != nil {
		names = append(names, "audio")
	}
	if imp.Banner != nil {
		names = append(names, "banner")
	}
	return strings.Join(names, "+")
}

// impRoute is the path an impression takes: which media type the bidder serves it as.
type impRoute int

const (
	routeNone   impRoute = iota
	routeVideo           // Triton wrapper bid typed video
	routeAudio           // Triton wrapper bid typed audio
	routeBanner          // dsp-service RTB request
)

// route picks the first enabled media type of the impression, video before audio before banner.
func route(p *openrtb_ext.ExtImpTriton, imp *openrtb2.Imp) impRoute {
	switch {
	case imp.Video != nil && formatEnabled(p, "video"):
		return routeVideo
	case imp.Audio != nil && formatEnabled(p, "audio"):
		return routeAudio
	case imp.Banner != nil && formatEnabled(p, "banner"):
		return routeBanner
	}
	return routeNone
}

// formatEnabled reads the placement's format switch; a missing switch means enabled.
func formatEnabled(p *openrtb_ext.ExtImpTriton, format string) bool {
	if p.Formats == nil {
		return true
	}
	var v *bool
	switch format {
	case "video":
		v = p.Formats.Video
	case "audio":
		v = p.Formats.Audio
	case "banner":
		v = p.Formats.Banner
	}
	return v == nil || *v
}

// parseParams decodes imp.ext.bidder and applies the checks the schema cannot express per path.
func parseParams(index int, imp *openrtb2.Imp) (*openrtb_ext.ExtImpTriton, error) {
	var bidderExt adapters.ExtImpBidder
	if err := jsonutil.Unmarshal(imp.Ext, &bidderExt); err != nil {
		return nil, &errortypes.BadInput{Message: fmt.Sprintf("imp[%d]: invalid imp.ext: %v", index, err)}
	}
	var params openrtb_ext.ExtImpTriton
	if err := jsonutil.Unmarshal(bidderExt.Bidder, &params); err != nil {
		return nil, &errortypes.BadInput{Message: fmt.Sprintf("imp[%d]: invalid imp.ext.bidder: %v", index, err)}
	}
	if params.STID < 1 {
		return nil, &errortypes.BadInput{Message: fmt.Sprintf("imp[%d]: stid must be a positive integer", index)}
	}
	// Brand and tag, most specific source first: the SDK's imp.ext.data.tagId, the explicit params, the
	// path of the provider's adRequestUrl (dsp-service renders …/rtb/{brand}/{tag}), the publisher id.
	// The tag is checked by the paths that need it (dsp-service RTB and VAST), not here.
	urlBrand, urlTag := idsFromAdRequestURL(params.AdRequestURL)
	if tagID := tagIDFromExt(imp.Ext); tagID != "" {
		params.TagID = tagID
	} else if params.TagID == "" {
		params.TagID = urlTag
	}
	if params.BrandID == "" {
		params.BrandID = urlBrand
	}
	if params.TagID != "" && !tagIDPattern.MatchString(params.TagID) {
		return nil, &errortypes.BadInput{Message: fmt.Sprintf("imp[%d]: tagId must match %s", index, tagIDPattern.String())}
	}
	if params.BrandID != "" && !tagIDPattern.MatchString(params.BrandID) {
		return nil, &errortypes.BadInput{Message: fmt.Sprintf("imp[%d]: brandId must match %s", index, tagIDPattern.String())}
	}
	if params.BaseURL != "" {
		if u, err := url.Parse(params.BaseURL); err != nil || u.Scheme != "https" || u.Host == "" || u.Fragment != "" {
			return nil, &errortypes.BadInput{Message: fmt.Sprintf("imp[%d]: baseUrl must be an absolute https URL", index)}
		}
	}
	if params.FeedType != "" && !tritonFeedTypes[params.FeedType] {
		return nil, &errortypes.BadInput{Message: fmt.Sprintf("imp[%d]: feedType must be one of podcast, music, radio, game, pureplay, replay, txt2speech", index)}
	}
	switch params.RenderMode {
	case "":
		params.RenderMode = renderServer
	case renderServer, renderClient:
	default:
		return nil, &errortypes.BadInput{Message: fmt.Sprintf("imp[%d]: hostRender must be %q or %q", index, renderClient, renderServer)}
	}
	if params.CPM < 0 {
		return nil, &errortypes.BadInput{Message: fmt.Sprintf("imp[%d]: cpm must not be negative", index)}
	}
	return &params, nil
}

// cxr is the request-level ext.cxr object dsp-service reads on the RTB path (docs/adr/0005).
type cxr struct {
	GIV         *float64 `json:"giv,omitempty"`
	AdSlots     *int     `json:"ad_slots,omitempty"`
	PA          int      `json:"pa"`
	STID        int64    `json:"stid"`
	RenderMode  string   `json:"host_render"`
	BrandID     string   `json:"brand_id"`
	TagID       string   `json:"tag_id"`
	PlacementID string   `json:"placement_id"`
}

// rtbRequest builds the single-impression OpenRTB request for dsp-service: the incoming request
// narrowed to this impression, bidder params stripped from imp.ext, ext replaced by cxr.
func (a *adapter) rtbRequest(index int, imp *openrtb2.Imp, params *openrtb_ext.ExtImpTriton, request *openrtb2.BidRequest) (*adapters.RequestData, error) {
	if err := requireTag(index, params); err != nil {
		return nil, err
	}
	brand := resolveBrand(params, request)
	if brand == "" {
		return nil, &errortypes.BadInput{Message: fmt.Sprintf("imp[%d]: app.publisher.id or site.publisher.id (the brand) is required", index)}
	}
	u, err := resolveURL(a.endpointTemplate, macros.EndpointTemplateParams{AccountID: url.PathEscape(brand), AdUnit: url.PathEscape(params.TagID)})
	if err != nil {
		return nil, err
	}

	pa := 0
	if params.RenderMode == renderClient {
		pa = 1
	}
	ext, err := jsonutil.Marshal(struct {
		CXR cxr `json:"cxr"`
	}{cxr{
		GIV:         a.giv,
		AdSlots:     a.adSlots,
		PA:          pa,
		STID:        params.STID,
		RenderMode:  params.RenderMode,
		BrandID:     brand,
		TagID:       params.TagID,
		PlacementID: placementID(imp.Ext),
	}})
	if err != nil {
		return nil, err
	}

	single := *imp
	single.Ext = stripBidderParams(imp.Ext)
	single.Banner = bannerWithSize(imp.Banner)
	outgoing := *request
	outgoing.Imp = []openrtb2.Imp{single}
	outgoing.Ext = ext
	body, err := jsonutil.Marshal(outgoing)
	if err != nil {
		return nil, err
	}

	headers := http.Header{}
	headers.Add("Content-Type", "application/json;charset=utf-8")
	headers.Add("Accept", "application/json")
	return &adapters.RequestData{
		Method:  http.MethodPost,
		Uri:     u.String(),
		Body:    body,
		Headers: headers,
		ImpIDs:  []string{imp.ID},
	}, nil
}

// resolveBrand is the brand dsp-service knows the ad tag under: the brandId param when the placement
// sets one, else the Brand of the request (publisher id = Prebid Server account).
func resolveBrand(params *openrtb_ext.ExtImpTriton, request *openrtb2.BidRequest) string {
	if params.BrandID != "" {
		return params.BrandID
	}
	return brandID(request)
}

// tagIDFromExt reads the Ad tag the SDK sends in imp.ext.data.tagId: a string, or the first element
// of a string array (Prebid Mobile's addExtData writes arrays). "" when absent or of another type.
func tagIDFromExt(ext json.RawMessage) string {
	value, dataType, _, err := jsonparser.Get(ext, "data", "tagId")
	if err != nil {
		return ""
	}
	switch dataType {
	case jsonparser.String:
		return string(value)
	case jsonparser.Array:
		var first string
		_, _ = jsonparser.ArrayEach(value, func(item []byte, itemType jsonparser.ValueType, _ int, _ error) {
			if first == "" && itemType == jsonparser.String {
				first = string(item)
			}
		})
		return first
	}
	return ""
}

// idsFromAdRequestURL reads the brand and tag from a dsp-service URL of the form
// …/rtb/{brand}/{tag} or …/vast/{brand}/{tag}; both "" when the URL has no such path. Only the path
// is read: the host never redirects a request (endpoints are host config).
func idsFromAdRequestURL(raw string) (brand, tag string) {
	if raw == "" {
		return "", ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ""
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+2 < len(segments); i++ {
		if (segments[i] == "rtb" || segments[i] == "vast") && tagIDPattern.MatchString(segments[i+1]) && tagIDPattern.MatchString(segments[i+2]) {
			return segments[i+1], segments[i+2]
		}
	}
	return "", ""
}

// bannerWithSize gives the outgoing banner explicit w/h from its first format when the SDK sent only
// format[]: dsp-service's RTB handler reads banner.w/h alone and otherwise sizes the creative 300x600.
// The incoming request is never touched (a copy is returned when anything changes).
func bannerWithSize(banner *openrtb2.Banner) *openrtb2.Banner {
	if banner == nil || (banner.W != nil && banner.H != nil) || len(banner.Format) == 0 {
		return banner
	}
	sized := *banner
	sized.W = ptrutil.ToPtr(banner.Format[0].W)
	sized.H = ptrutil.ToPtr(banner.Format[0].H)
	return &sized
}

// brandID is the Brand: the publisher id of the app or site (one Prebid Server account).
func brandID(request *openrtb2.BidRequest) string {
	if request.App != nil && request.App.Publisher != nil {
		return request.App.Publisher.ID
	}
	if request.Site != nil && request.Site.Publisher != nil {
		return request.Site.Publisher.ID
	}
	return ""
}

// stripBidderParams drops the top-level bidder params from a copy of imp.ext (dsp-service does not
// read them); data, gpid, tid and the rest stay byte for byte. An ext left empty is dropped
// altogether. (jsonutil.DropElement is not used: it matches the first "bidder" at any depth.)
func stripBidderParams(ext json.RawMessage) json.RawMessage {
	if len(ext) == 0 {
		return nil
	}
	rest := jsonparser.Delete(append(json.RawMessage(nil), ext...), "bidder")
	if len(bytes.Trim(rest, " \t\r\n{}")) == 0 {
		return nil
	}
	return rest
}

// placementID is the Placement id dsp-service renders into imp.ext.data for every stored imp
// (ADR 0005 relaxes ADR 0001's "bidder params only" for this Genuin-rendered field); "" when absent.
func placementID(ext json.RawMessage) string {
	id, _ := jsonparser.GetString(ext, "data", "placementId")
	return id
}

// MakeBids turns the answer for one impression into bids. A no-network request (the Triton wrapper
// path) becomes the wrapper bid for its impression; dsp-service's answer becomes bids: 204 → none,
// failure statuses → the standard adapter error (the exchange reports them before MakeBids runs),
// 200 → the OpenRTB bids with their type from mtype and the price macro resolved.
func (a *adapter) MakeBids(request *openrtb2.BidRequest, requestData *adapters.RequestData, responseData *adapters.ResponseData) (*adapters.BidderResponse, []error) {
	if requestData.NoRequest {
		index, imp, err := findImp(request, requestData)
		if err != nil {
			return nil, []error{err}
		}
		params, err := parseParams(index, imp)
		if err != nil {
			return nil, []error{err}
		}
		return wrapperBid(imp, params, requestData)
	}
	if adapters.IsResponseStatusCodeNoContent(responseData) {
		return nil, nil
	}
	if err := adapters.CheckResponseStatusCodeForErrors(responseData); err != nil {
		return nil, []error{err}
	}

	var response openrtb2.BidResponse
	if err := jsonutil.Unmarshal(responseData.Body, &response); err != nil {
		return nil, []error{&errortypes.BadServerResponse{Message: fmt.Sprintf("triton: invalid bid response: %v", err)}}
	}

	bidResponse := adapters.NewBidderResponseWithBidsCapacity(len(request.Imp))
	bidResponse.Currency = defaultCurrency
	if response.Cur != "" {
		bidResponse.Currency = response.Cur
	}
	var errs []error
	for i := range response.SeatBid {
		for j := range response.SeatBid[i].Bid {
			bid := &response.SeatBid[i].Bid[j]
			bidType, err := bidTypeFor(bid, request.Imp)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			resolveAuctionPrice(bid)
			bidResponse.Bids = append(bidResponse.Bids, &adapters.TypedBid{Bid: bid, BidType: bidType})
		}
	}
	return bidResponse, errs
}

// bidTypeFor takes the bid's mtype, falling back to the impression's single media type.
func bidTypeFor(bid *openrtb2.Bid, imps []openrtb2.Imp) (openrtb_ext.BidType, error) {
	switch bid.MType {
	case openrtb2.MarkupBanner:
		return openrtb_ext.BidTypeBanner, nil
	case openrtb2.MarkupVideo:
		return openrtb_ext.BidTypeVideo, nil
	case openrtb2.MarkupAudio:
		return openrtb_ext.BidTypeAudio, nil
	case 0:
	default:
		return "", &errortypes.BadServerResponse{Message: fmt.Sprintf("triton: bid %q has unsupported mtype %d", bid.ID, bid.MType)}
	}
	for i := range imps {
		if imps[i].ID != bid.ImpID {
			continue
		}
		var types []openrtb_ext.BidType
		if imps[i].Banner != nil {
			types = append(types, openrtb_ext.BidTypeBanner)
		}
		if imps[i].Video != nil {
			types = append(types, openrtb_ext.BidTypeVideo)
		}
		if imps[i].Audio != nil {
			types = append(types, openrtb_ext.BidTypeAudio)
		}
		if len(types) == 1 {
			return types[0], nil
		}
		return "", &errortypes.BadServerResponse{Message: fmt.Sprintf("triton: bid %q has no mtype and imp %q has %d media types", bid.ID, bid.ImpID, len(types))}
	}
	return "", &errortypes.BadServerResponse{Message: fmt.Sprintf("triton: bid %q refers to unknown imp %q", bid.ID, bid.ImpID)}
}

// resolveAuctionPrice substitutes the bid price for ${AUCTION_PRICE} in the creative and pixel URLs.
// ${AUCTION_LOSS} is left alone: nothing in Prebid Server fires the loss URL.
func resolveAuctionPrice(bid *openrtb2.Bid) {
	price := strconv.FormatFloat(bid.Price, 'f', 2, 64)
	bid.AdM = strings.ReplaceAll(bid.AdM, auctionPriceMacro, price)
	bid.NURL = strings.ReplaceAll(bid.NURL, auctionPriceMacro, price)
	bid.BURL = strings.ReplaceAll(bid.BURL, auctionPriceMacro, price)
	bid.LURL = strings.ReplaceAll(bid.LURL, auctionPriceMacro, price)
}
