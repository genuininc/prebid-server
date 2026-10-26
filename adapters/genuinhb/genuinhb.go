// Package genuinhb implements the GenuinHB bidder: the adapter Genuin operates for its own demand
// sources. Each source is its own bidder code (a static alias of genuinhb) and selects its behaviour
// through extra_info.type:
//
//   - vast-tag (bidder code genuinhb): GET the Genuin ad server's VAST tag
//     {endpoint}/tagxml/customer/{customerId}/tag/{tagId} per video or audio impression and bid
//     with the returned VAST, unchanged (tag.go).
//   - gam-wrapper (bidder code gam): no network call; per video impression, bid with a VAST
//     wrapper around a Google Ad Manager tag built from the params and the request (gam.go).
package genuinhb

import (
	"errors"
	"fmt"
	"net/url"
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

	// Source types an alias of this adapter can be configured with (extra_info.type).
	sourceVastTag    = "vast-tag"    // GET a VAST tag, bid with the returned VAST (default)
	sourceGAMWrapper = "gam-wrapper" // bid with a VAST wrapper around a GAM tag, no network call
)

// source is one demand source's behaviour behind the shared adapter. Implementations are immutable
// after construction and safe for concurrent use.
type source interface {
	makeRequests(request *openrtb2.BidRequest) ([]*adapters.RequestData, []error)
	makeBids(request *openrtb2.BidRequest, requestData *adapters.RequestData, responseData *adapters.ResponseData) (*adapters.BidderResponse, []error)
}

type adapter struct {
	source source
}

// extraInfo is the optional adapters.<bidder>.extra_info JSON object.
type extraInfo struct {
	Type         string  `json:"type"`
	DefaultPrice float64 `json:"default_price"`
	Currency     string  `json:"currency"`
	// Sizes is the gam-wrapper default for the extra GAM slot sizes when the params carry none.
	Sizes []string `json:"sizes"`
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
	info, err := parseExtraInfo(config.ExtraAdapterInfo)
	if err != nil {
		return nil, err
	}

	var src source
	switch info.Type {
	case sourceVastTag:
		src, err = newTagSource(endpointTemplate, info)
	case sourceGAMWrapper:
		src, err = newGAMSource(endpointTemplate, info.Sizes)
	}
	if err != nil {
		return nil, err
	}
	return &adapter{source: src}, nil
}

func (a *adapter) MakeRequests(request *openrtb2.BidRequest, _ *adapters.ExtraRequestInfo) ([]*adapters.RequestData, []error) {
	return a.source.makeRequests(request)
}

func (a *adapter) MakeBids(request *openrtb2.BidRequest, requestData *adapters.RequestData, responseData *adapters.ResponseData) (*adapters.BidderResponse, []error) {
	return a.source.makeBids(request, requestData, responseData)
}

// parseExtraInfo returns the validated extra_info with defaults applied (type vast-tag, currency USD).
func parseExtraInfo(raw string) (extraInfo, error) {
	info := extraInfo{Type: sourceVastTag, Currency: defaultCurrency}
	if strings.TrimSpace(raw) == "" {
		return info, nil
	}

	info = extraInfo{}
	if err := jsonutil.Unmarshal([]byte(raw), &info); err != nil {
		return extraInfo{}, fmt.Errorf("invalid extra_info: %w", err)
	}
	switch info.Type {
	case "":
		info.Type = sourceVastTag
	case sourceVastTag, sourceGAMWrapper:
	default:
		return extraInfo{}, fmt.Errorf("invalid extra_info: unknown type %q (want %q or %q)", info.Type, sourceVastTag, sourceGAMWrapper)
	}
	if info.DefaultPrice < 0 {
		return extraInfo{}, errors.New("invalid extra_info: default_price must not be negative")
	}
	for _, size := range info.Sizes {
		if !sizePattern.MatchString(size) {
			return extraInfo{}, fmt.Errorf("invalid extra_info: size %q is not WxH", size)
		}
	}

	if info.Currency == "" {
		info.Currency = defaultCurrency
	} else {
		unit, err := currency.ParseISO(info.Currency)
		if err != nil {
			return extraInfo{}, fmt.Errorf("invalid extra_info: currency %q is not an ISO 4217 code", info.Currency)
		}
		info.Currency = unit.String()
	}
	return info, nil
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

// supportsMedia reports whether the impression is one the adapter can serve at all.
func supportsMedia(imp *openrtb2.Imp) bool {
	return imp.Video != nil || imp.Audio != nil
}

func unsupportedMedia(index int) error {
	return &errortypes.BadInput{
		Message: fmt.Sprintf("imp[%d]: genuinhb supports only video and audio impressions", index),
	}
}

// decodeImpParams reads imp.ext.bidder without validating which source shape it carries.
func decodeImpParams(index int, imp *openrtb2.Imp) (*openrtb_ext.ExtImpGenuinHB, error) {
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
	return &params, nil
}

// checkResponse reports whether the response has content to parse. A no-content answer yields
// (false, nil); a failure status yields its error.
func checkResponse(responseData *adapters.ResponseData) (bool, error) {
	if adapters.IsResponseStatusCodeNoContent(responseData) {
		return false, nil
	}
	if err := adapters.CheckResponseStatusCodeForErrors(responseData); err != nil {
		return false, err
	}
	return true, nil
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
		Message: fmt.Sprintf("failed to find the impression for genuinhb response, impIDs %q", requestData.ImpIDs),
	}
}
