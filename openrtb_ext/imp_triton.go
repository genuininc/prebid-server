package openrtb_ext

// ExtImpTriton defines the contract for bidrequest.imp[i].ext.prebid.bidder.triton: the placement's
// Triton station and dsp-service ad tag, plus the per-placement settings of the two paths
// (see static/bidder-params/triton.json and docs/adr/0005).
type ExtImpTriton struct {
	// STID is the Triton station id of the placement; sent to dsp-service (ext.cxr.stid) and to Triton (stid).
	STID int64 `json:"stid"`
	// TagID is dsp-service's ad tag id, the {tag} path segment of the RTB endpoint.
	TagID string `json:"tagId"`
	// BrandID is dsp-service's brand id for the ad tag (the {brand} path segment and ext.cxr.brand_id)
	// when it differs from the Prebid Server account; empty = the brand in AdRequestURL, else the publisher id.
	BrandID string `json:"brandId"`
	// AdRequestURL is the dsp-service RTB (or VAST) URL the provider record names,
	// https://…/goservices/dsp/rtb/{brand}/{tag}; only its brand and tag path segments are read, as
	// defaults for BrandID and TagID. The host is never used: endpoints live in Prebid Server config.
	AdRequestURL string `json:"adRequestUrl"`
	// RenderMode is the banner placement's render mode (wire key hostRender): "server" (default) or
	// "client" (ext.cxr.pa = 1).
	RenderMode string `json:"hostRender"`
	// CPM is the price of the Triton wrapper bid for audio and video impressions; 0.05 when absent.
	CPM float64 `json:"cpm"`
	// BaseURL is Triton's on-demand ad request base (https://<cmod host>/ondemand/ars). When set, audio
	// and video wrappers point at Triton directly, with the documented request parameters appended;
	// when absent they point at dsp-service's VAST endpoint for the ad tag.
	BaseURL string `json:"baseUrl"`
	// FeedType is Triton's feed-type classification of the inventory (podcast, music, …), when known.
	FeedType string `json:"feedType"`
	// Enabled turns the bidder off for the impression when false; absent means enabled.
	Enabled *bool `json:"enabled"`
	// Formats enables or disables each media type independently; absent (object or field) means enabled.
	Formats *ExtImpTritonFormats `json:"formats"`
}

// ExtImpTritonFormats is the per-format switch of the Triton bidder params.
type ExtImpTritonFormats struct {
	Audio  *bool `json:"audio"`
	Video  *bool `json:"video"`
	Banner *bool `json:"banner"`
}
