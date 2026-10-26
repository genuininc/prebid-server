package openrtb_ext

// ExtImpGenuinHB defines the contract for bidrequest.imp[i].ext.prebid.bidder.genuinhb and its
// aliases. Which fields apply depends on the alias's source type (see static/bidder-params/genuinhb.json).
type ExtImpGenuinHB struct {
	// vast-tag: the Genuin ad server tag
	CustomerID string `json:"customerId"`
	TagID      string `json:"tagId"`

	// gam-wrapper: the Google Ad Manager tag wrapped into a bid
	AdUnitPath     string            `json:"adUnitPath"`
	CPM            float64           `json:"cpm"`
	CustParams     map[string]string `json:"custParams"`
	DescriptionURL string            `json:"descriptionUrl"`
	// Sizes are extra GAM master video ad slot sizes ("WxH") offered after the imp's own size, so a
	// line item trafficked at 640x480 can fill a 320x480 player (GAM matches sz against creative sizes).
	Sizes []string `json:"sizes"`
}
