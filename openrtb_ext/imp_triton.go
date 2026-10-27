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
	// when it differs from the Prebid Server account; empty = the request's publisher id.
	BrandID string `json:"brandId"`
	// RenderMode is the banner placement's render mode (wire key hostRender): "server" (default) or
	// "client" (ext.cxr.pa = 1).
	RenderMode string `json:"hostRender"`
	// CPM is the price of the Triton wrapper bid for audio and video impressions; 0.05 when absent.
	CPM float64 `json:"cpm"`
}
