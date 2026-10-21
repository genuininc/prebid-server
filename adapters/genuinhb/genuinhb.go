// Package genuinhb implements the GenuinHB bidder: Genuin's own ad server, which serves VAST from
// GET {endpoint}/tagxml/customer/{customerId}/tag/{tagId}. The adapter issues one GET per video or
// audio impression and turns a returned VAST document into a bid whose adm is that VAST, unchanged.
package genuinhb

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"text/template"

	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters"
	"github.com/prebid/prebid-server/v4/config"
	"github.com/prebid/prebid-server/v4/errortypes"
	"github.com/prebid/prebid-server/v4/macros"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/prebid/prebid-server/v4/util/jsonutil"
	"golang.org/x/text/currency"
)

const (
	defaultCurrency = "USD"
	bidIDPrefix     = "genuinhb-"

	// Placeholders used once at build time to check where the endpoint template puts the ids.
	customerPlaceholder = "genuinhbcustomerid"
	tagPlaceholder      = "genuinhbtagid"
)

// idPattern mirrors static/bidder-params/genuinhb.json. The schema is enforced by PBS before the
// adapter runs; the adapter re-checks so that an id can never change the shape of the tag URL.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type adapter struct {
	// endpointTemplate resolves {{.AccountID}} to the customerId and {{.AdUnit}} to the tagId.
	// It is immutable after Builder returns, and template execution is safe for concurrent use.
	endpointTemplate *template.Template
	// defaultPrice is the CPM bid when the VAST carries no usable <Pricing>; 0 disables it.
	defaultPrice float64
	// defaultPriceCurrency is the ISO 4217 currency of defaultPrice.
	defaultPriceCurrency string
}

// extraInfo is the optional adapters.genuinhb.extra_info JSON object.
type extraInfo struct {
	DefaultPrice float64 `json:"default_price"`
	Currency     string  `json:"currency"`
}

// Builder builds a new instance of the GenuinHB adapter for the given bidder with the given config.
func Builder(bidderName openrtb_ext.BidderName, config config.Adapter, server config.Server) (adapters.Bidder, error) {
	if strings.TrimSpace(config.Endpoint) == "" {
		return nil, errors.New("endpoint is empty")
	}
	endpointTemplate, err := template.New("endpointTemplate").Parse(config.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("unable to parse endpoint url template: %w", err)
	}

	bidder := &adapter{endpointTemplate: endpointTemplate}
	if err := bidder.validateEndpoint(); err != nil {
		return nil, err
	}

	bidder.defaultPrice, bidder.defaultPriceCurrency, err = parseExtraInfo(config.ExtraAdapterInfo)
	if err != nil {
		return nil, err
	}

	return bidder, nil
}

// validateEndpoint rejects endpoints that are not absolute http(s) URLs, that do not put both ids
// in the URL path, or that would let an id influence the host.
func (a *adapter) validateEndpoint() error {
	resolved, err := a.buildEndpointURL(customerPlaceholder, tagPlaceholder)
	if err != nil {
		return fmt.Errorf("unable to resolve endpoint url template: %w", err)
	}
	u, err := url.Parse(resolved)
	if err != nil {
		return fmt.Errorf("endpoint is not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("endpoint must be an absolute http or https URL")
	}
	if u.Host == "" {
		return errors.New("endpoint has no host")
	}
	if strings.Contains(u.Host, customerPlaceholder) || strings.Contains(u.Host, tagPlaceholder) {
		return errors.New("endpoint host must not contain the {{.AccountID}} or {{.AdUnit}} macros")
	}
	if !strings.Contains(u.Path, customerPlaceholder) || !strings.Contains(u.Path, tagPlaceholder) {
		return errors.New("endpoint path must contain {{.AccountID}} (customerId) and {{.AdUnit}} (tagId)")
	}
	return nil
}

func parseExtraInfo(raw string) (float64, string, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, defaultCurrency, nil
	}

	var info extraInfo
	if err := jsonutil.Unmarshal([]byte(raw), &info); err != nil {
		return 0, "", fmt.Errorf("invalid extra_info: %w", err)
	}
	if info.DefaultPrice < 0 {
		return 0, "", errors.New("invalid extra_info: default_price must not be negative")
	}

	priceCurrency := defaultCurrency
	if info.Currency != "" {
		unit, err := currency.ParseISO(info.Currency)
		if err != nil {
			return 0, "", fmt.Errorf("invalid extra_info: currency %q is not an ISO 4217 code", info.Currency)
		}
		priceCurrency = unit.String()
	}
	return info.DefaultPrice, priceCurrency, nil
}

// buildEndpointURL returns the VAST tag URL for one customer and tag. Ids are path-escaped, so even
// an id that bypassed validation stays inside its own path segment.
func (a *adapter) buildEndpointURL(customerID, tagID string) (string, error) {
	return macros.ResolveMacros(a.endpointTemplate, macros.EndpointTemplateParams{
		AccountID: url.PathEscape(customerID),
		AdUnit:    url.PathEscape(tagID),
	})
}

// MakeRequests returns one GET per impression, because every impression can carry its own
// customerId and tagId. Invalid impressions are reported and skipped; the others still bid.
func (a *adapter) MakeRequests(request *openrtb2.BidRequest, _ *adapters.ExtraRequestInfo) ([]*adapters.RequestData, []error) {
	headers := buildHeaders(request)
	requests := make([]*adapters.RequestData, 0, len(request.Imp))
	var errs []error

	for i := range request.Imp {
		imp := &request.Imp[i]
		if imp.Video == nil && imp.Audio == nil {
			errs = append(errs, &errortypes.BadInput{
				Message: fmt.Sprintf("imp[%d]: genuinhb supports only video and audio impressions", i),
			})
			continue
		}

		params, err := parseImpParams(i, imp)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		uri, err := a.buildEndpointURL(params.CustomerID, params.TagID)
		if err != nil {
			errs = append(errs, &errortypes.BadInput{
				Message: fmt.Sprintf("imp[%d]: unable to build the genuinhb tag URL: %v", i, err),
			})
			continue
		}

		requests = append(requests, &adapters.RequestData{
			Method:  http.MethodGet,
			Uri:     uri,
			Headers: headers.Clone(),
			ImpIDs:  []string{imp.ID},
		})
	}

	return requests, errs
}

func parseImpParams(index int, imp *openrtb2.Imp) (*openrtb_ext.ExtImpGenuinHB, error) {
	var bidderExt adapters.ExtImpBidder
	if err := jsonutil.Unmarshal(imp.Ext, &bidderExt); err != nil {
		return nil, &errortypes.BadInput{
			Message: fmt.Sprintf("imp[%d]: invalid imp.ext: %v", index, err),
		}
	}

	var params openrtb_ext.ExtImpGenuinHB
	if err := jsonutil.Unmarshal(bidderExt.Bidder, &params); err != nil {
		return nil, &errortypes.BadInput{
			Message: fmt.Sprintf("imp[%d]: invalid imp.ext.bidder: %v", index, err),
		}
	}

	if !idPattern.MatchString(params.CustomerID) {
		return nil, &errortypes.BadInput{
			Message: fmt.Sprintf("imp[%d]: customerId is required and must be 1-64 characters from [A-Za-z0-9_-]", index),
		}
	}
	if !idPattern.MatchString(params.TagID) {
		return nil, &errortypes.BadInput{
			Message: fmt.Sprintf("imp[%d]: tagId is required and must be 1-64 characters from [A-Za-z0-9_-]", index),
		}
	}
	return &params, nil
}

// buildHeaders forwards the end-user context a VAST tag server needs for targeting and fraud
// checks. Accept-Encoding is left unset so Go's transport negotiates and decodes gzip itself.
func buildHeaders(request *openrtb2.BidRequest) http.Header {
	headers := http.Header{}
	headers.Set("Accept", "application/xml")
	if device := request.Device; device != nil {
		if device.UA != "" {
			headers.Set("User-Agent", device.UA)
		}
		if device.IP != "" {
			headers.Set("X-Forwarded-For", device.IP)
		} else if device.IPv6 != "" {
			headers.Set("X-Forwarded-For", device.IPv6)
		}
	}
	if request.Site != nil && request.Site.Page != "" {
		headers.Set("Referer", request.Site.Page)
	}
	return headers
}

// MakeBids turns one VAST tag response into at most one bid for the impression it was requested for.
func (a *adapter) MakeBids(request *openrtb2.BidRequest, requestData *adapters.RequestData, responseData *adapters.ResponseData) (*adapters.BidderResponse, []error) {
	if adapters.IsResponseStatusCodeNoContent(responseData) {
		return nil, nil
	}
	if err := adapters.CheckResponseStatusCodeForErrors(responseData); err != nil {
		return nil, []error{err}
	}
	if len(bytes.TrimSpace(responseData.Body)) == 0 {
		return nil, nil
	}

	imp, err := findImp(request, requestData)
	if err != nil {
		return nil, []error{err}
	}

	info, err := parseVAST(responseData.Body)
	if err != nil {
		return nil, []error{&errortypes.BadServerResponse{
			Message: fmt.Sprintf("genuinhb returned invalid VAST for imp %q: %v", imp.ID, err),
		}}
	}
	if !info.hasAd {
		return nil, nil
	}

	price, priceCurrency, ok := a.resolvePrice(info)
	if !ok {
		return nil, []error{&errortypes.Warning{
			WarningCode: errortypes.UnknownWarningCode,
			Message: fmt.Sprintf("genuinhb VAST for imp %q has no usable CPM <Pricing> and "+
				"adapters.genuinhb.extra_info default_price is not set; no bid", imp.ID),
		}}
	}

	bidType, markupType := bidTypeFor(imp, info)
	bid := &openrtb2.Bid{
		ID:    bidIDPrefix + imp.ID,
		ImpID: imp.ID,
		Price: price,
		AdM:   string(responseData.Body),
		CrID:  creativeID(imp, info),
		MType: markupType,
	}
	if bidType == openrtb_ext.BidTypeVideo && imp.Video != nil {
		if imp.Video.W != nil {
			bid.W = *imp.Video.W
		}
		if imp.Video.H != nil {
			bid.H = *imp.Video.H
		}
	}

	typedBid := &adapters.TypedBid{Bid: bid, BidType: bidType}
	if info.durationSec > 0 {
		typedBid.BidVideo = &openrtb_ext.ExtBidPrebidVideo{Duration: info.durationSec}
	}

	bidResponse := adapters.NewBidderResponseWithBidsCapacity(1)
	bidResponse.Currency = priceCurrency
	bidResponse.Bids = append(bidResponse.Bids, typedBid)
	return bidResponse, nil
}

func findImp(request *openrtb2.BidRequest, requestData *adapters.RequestData) (*openrtb2.Imp, error) {
	if len(requestData.ImpIDs) == 1 {
		for i := range request.Imp {
			if request.Imp[i].ID == requestData.ImpIDs[0] {
				return &request.Imp[i], nil
			}
		}
	}
	return nil, &errortypes.BadInput{
		Message: fmt.Sprintf("failed to find the impression for genuinhb response, impIDs %q", requestData.ImpIDs),
	}
}

// resolvePrice prefers the VAST <Pricing> CPM and falls back to the host-configured default price.
func (a *adapter) resolvePrice(info vastInfo) (float64, string, bool) {
	if info.price > 0 {
		if info.priceCurrency == "" {
			return info.price, defaultCurrency, true
		}
		if unit, err := currency.ParseISO(info.priceCurrency); err == nil {
			return info.price, unit.String(), true
		}
	}
	if a.defaultPrice > 0 {
		return a.defaultPrice, a.defaultPriceCurrency, true
	}
	return 0, "", false
}

// bidTypeFor picks the bid type from the impression the tag was requested for. The tag response
// has no OpenRTB mtype; when the impression allows both, audio-only media files decide.
func bidTypeFor(imp *openrtb2.Imp, info vastInfo) (openrtb_ext.BidType, openrtb2.MarkupType) {
	switch {
	case imp.Audio != nil && imp.Video == nil:
		return openrtb_ext.BidTypeAudio, openrtb2.MarkupAudio
	case imp.Audio != nil && info.audioOnly:
		return openrtb_ext.BidTypeAudio, openrtb2.MarkupAudio
	default:
		return openrtb_ext.BidTypeVideo, openrtb2.MarkupVideo
	}
}

// creativeID returns the VAST <Ad id>, else the first <Creative id>, else an id derived from the tag.
func creativeID(imp *openrtb2.Imp, info vastInfo) string {
	if info.adID != "" {
		return info.adID
	}
	if info.creativeID != "" {
		return info.creativeID
	}
	if params, err := parseImpParams(0, imp); err == nil {
		return "genuinhb-" + params.CustomerID + "-" + params.TagID
	}
	return "genuinhb"
}
