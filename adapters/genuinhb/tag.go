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
	"github.com/prebid/prebid-server/v4/errortypes"
	"github.com/prebid/prebid-server/v4/macros"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"golang.org/x/text/currency"
)

const (
	tagBidIDPrefix = "genuinhb-"

	// Placeholders used once at build time to check where the endpoint template puts the ids.
	customerPlaceholder = "genuinhbcustomerid"
	tagPlaceholder      = "genuinhbtagid"
)

// idPattern mirrors static/bidder-params/genuinhb.json. The schema is enforced by PBS before the
// adapter runs; the adapter re-checks so that an id can never change the shape of the tag URL.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// tagSource is the vast-tag source: GET the Genuin ad server's VAST tag and bid with the VAST.
type tagSource struct {
	// endpointTemplate resolves {{.AccountID}} to the customerId and {{.AdUnit}} to the tagId.
	endpointTemplate *template.Template
	// defaultPrice is the CPM bid when the VAST carries no usable <Pricing>; 0 disables it.
	defaultPrice float64
	// defaultPriceCurrency is the ISO 4217 currency of defaultPrice.
	defaultPriceCurrency string
}

func newTagSource(endpointTemplate *template.Template, info extraInfo) (*tagSource, error) {
	s := &tagSource{
		endpointTemplate:     endpointTemplate,
		defaultPrice:         info.DefaultPrice,
		defaultPriceCurrency: info.Currency,
	}
	if err := s.validateEndpoint(); err != nil {
		return nil, err
	}
	return s, nil
}

// validateEndpoint rejects endpoints that do not put both ids in the URL path, or that would let
// an id influence the host.
func (s *tagSource) validateEndpoint() error {
	u, err := resolveURL(s.endpointTemplate, macros.EndpointTemplateParams{AccountID: customerPlaceholder, AdUnit: tagPlaceholder})
	if err != nil {
		return err
	}
	if strings.Contains(u.Host, customerPlaceholder) || strings.Contains(u.Host, tagPlaceholder) {
		return errors.New("endpoint host must not contain the {{.AccountID}} or {{.AdUnit}} macros")
	}
	if !strings.Contains(u.Path, customerPlaceholder) || !strings.Contains(u.Path, tagPlaceholder) {
		return errors.New("endpoint path must contain {{.AccountID}} (customerId) and {{.AdUnit}} (tagId)")
	}
	return nil
}

// buildEndpointURL returns the VAST tag URL for one customer and tag. Ids are path-escaped, so even
// an id that bypassed validation stays inside its own path segment.
func (s *tagSource) buildEndpointURL(customerID, tagID string) (string, error) {
	return macros.ResolveMacros(s.endpointTemplate, macros.EndpointTemplateParams{
		AccountID: url.PathEscape(customerID),
		AdUnit:    url.PathEscape(tagID),
	})
}

// makeRequests returns one GET per impression, because every impression can carry its own
// customerId and tagId. Invalid impressions are reported and skipped; the others still bid.
func (s *tagSource) makeRequests(request *openrtb2.BidRequest) ([]*adapters.RequestData, []error) {
	headers := buildHeaders(request)
	requests := make([]*adapters.RequestData, 0, len(request.Imp))
	var errs []error

	for i := range request.Imp {
		imp := &request.Imp[i]
		if !supportsMedia(imp) {
			errs = append(errs, unsupportedMedia(i))
			continue
		}

		params, err := parseTagParams(i, imp)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		uri, err := s.buildEndpointURL(params.CustomerID, params.TagID)
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

// parseTagParams decodes and validates the vast-tag shape of the params.
func parseTagParams(index int, imp *openrtb2.Imp) (*openrtb_ext.ExtImpGenuinHB, error) {
	params, err := decodeImpParams(index, imp)
	if err != nil {
		return nil, err
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
	if params.AdUnitPath != "" || params.CPM != 0 {
		return nil, &errortypes.BadInput{
			Message: fmt.Sprintf("imp[%d]: adUnitPath and cpm are not valid for the %s source", index, sourceVastTag),
		}
	}
	return params, nil
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

// makeBids turns one VAST tag response into at most one bid for the impression it was requested for.
func (s *tagSource) makeBids(request *openrtb2.BidRequest, requestData *adapters.RequestData, responseData *adapters.ResponseData) (*adapters.BidderResponse, []error) {
	if ok, err := checkResponse(responseData); !ok {
		if err != nil {
			return nil, []error{err}
		}
		return nil, nil
	}
	if len(bytes.TrimSpace(responseData.Body)) == 0 {
		return nil, nil
	}

	index, imp, err := findImp(request, requestData)
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

	price, priceCurrency, ok := s.resolvePrice(info)
	if !ok {
		return nil, []error{&errortypes.Warning{
			WarningCode: errortypes.UnknownWarningCode,
			Message: fmt.Sprintf("genuinhb VAST for imp %q has no usable CPM <Pricing> and "+
				"adapters.genuinhb.extra_info default_price is not set; no bid", imp.ID),
		}}
	}

	bidType, markupType := bidTypeFor(imp, info)
	bid := &openrtb2.Bid{
		ID:    tagBidIDPrefix + imp.ID,
		ImpID: imp.ID,
		Price: price,
		AdM:   string(responseData.Body),
		CrID:  creativeID(index, imp, info),
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

// resolvePrice prefers the VAST <Pricing> CPM and falls back to the host-configured default price.
func (s *tagSource) resolvePrice(info vastInfo) (float64, string, bool) {
	if info.price > 0 {
		if info.priceCurrency == "" {
			return info.price, defaultCurrency, true
		}
		if unit, err := currency.ParseISO(info.priceCurrency); err == nil {
			return info.price, unit.String(), true
		}
	}
	if s.defaultPrice > 0 {
		return s.defaultPrice, s.defaultPriceCurrency, true
	}
	return 0, "", false
}

// bidTypeFor picks the bid type from the impression the tag was requested for. A VAST tag response
// carries no OpenRTB mtype, so inferring from the impression (the pattern PBS's semgrep rules
// discourage for OpenRTB bidders) is the only option here; when the impression allows both,
// audio-only media files decide.
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
func creativeID(index int, imp *openrtb2.Imp, info vastInfo) string {
	if info.adID != "" {
		return info.adID
	}
	if info.creativeID != "" {
		return info.creativeID
	}
	if params, err := parseTagParams(index, imp); err == nil {
		return "genuinhb-" + params.CustomerID + "-" + params.TagID
	}
	return "genuinhb"
}
