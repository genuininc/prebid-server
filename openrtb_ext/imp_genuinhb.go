package openrtb_ext

// ExtImpGenuinHB defines the contract for bidrequest.imp[i].ext.prebid.bidder.genuinhb
type ExtImpGenuinHB struct {
	CustomerID string `json:"customerId"`
	TagID      string `json:"tagId"`
}
