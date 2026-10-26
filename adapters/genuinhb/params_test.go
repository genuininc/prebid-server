package genuinhb

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/prebid/prebid-server/v4/openrtb_ext"
)

func TestValidParams(t *testing.T) {
	validator, err := openrtb_ext.NewBidderParamsValidator("../../static/bidder-params")
	if err != nil {
		t.Fatalf("Failed to fetch the json-schemas. %v", err)
	}

	for _, validParam := range validParams {
		if err := validator.Validate(openrtb_ext.BidderGenuinHB, json.RawMessage(validParam)); err != nil {
			t.Errorf("Schema rejected genuinhb params: %s", validParam)
		}
	}
}

// TestInvalidParams makes sure that the genuinhb schema rejects all the imp.ext fields we don't support,
// in particular anything that could change the shape of the VAST tag URL.
func TestInvalidParams(t *testing.T) {
	validator, err := openrtb_ext.NewBidderParamsValidator("../../static/bidder-params")
	if err != nil {
		t.Fatalf("Failed to fetch the json-schemas. %v", err)
	}

	for _, invalidParam := range invalidParams {
		if err := validator.Validate(openrtb_ext.BidderGenuinHB, json.RawMessage(invalidParam)); err == nil {
			t.Errorf("Schema allowed unexpected params: %s", invalidParam)
		}
	}
}

var validParams = []string{
	`{"customerId": "3119", "tagId": "3753"}`,
	`{"customerId": "42", "tagId": "abc_DEF-1"}`,
	`{"customerId": "a", "tagId": "b"}`,
	`{"customerId": "` + strings.Repeat("9", 64) + `", "tagId": "1"}`,
	`{"customerId": "3119", "tagId": "3753", "unknown": true}`,
	// gam-wrapper shape
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 0.01}`,
	`{"adUnitPath": "/21775744923,12345/genuin/instream/pre", "cpm": 50}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 1.5, "custParams": {"tier": "premium", "gn_placement": "pre-1"}}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 1.5, "custParams": {}, "descriptionUrl": "https://www.begenuin.com/watch/demo"}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 1, "custParams": {"color": "red,blue", "k": "a b"}}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 1, "sizes": ["640x480", "400x300"]}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 1, "sizes": []}`,
}

var invalidParams = []string{
	``,
	`null`,
	`{}`,
	`[]`,
	`{"customerId": "3119"}`,
	`{"tagId": "3753"}`,
	`{"customerId": "", "tagId": "3753"}`,
	`{"customerId": "3119", "tagId": ""}`,
	`{"customerId": 3119, "tagId": "3753"}`,
	`{"customerId": "3119", "tagId": 3753}`,
	`{"customerId": null, "tagId": "3753"}`,
	`{"customerId": "3119", "tagId": {"id": "3753"}}`,
	`{"customerId": "3119/../1", "tagId": "3753"}`,
	`{"customerId": "3119", "tagId": "3753/extra"}`,
	`{"customerId": "3119", "tagId": "//evil.com"}`,
	`{"customerId": "3119", "tagId": ".."}`,
	`{"customerId": "3119", "tagId": "3753?debug=1"}`,
	`{"customerId": "3119", "tagId": "3753#frag"}`,
	`{"customerId": "3119", "tagId": "37%2F53"}`,
	`{"customerId": "31 19", "tagId": "3753"}`,
	`{"customerId": "@evil.com", "tagId": "3753"}`,
	`{"customerId": "3119", "tagId": "https://evil.com"}`,
	`{"customerId": "` + strings.Repeat("9", 65) + `", "tagId": "1"}`,
	// gam-wrapper shape, broken
	`{"adUnitPath": "/21775744923/genuin/instream"}`,
	`{"cpm": 1}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 0}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": -1}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": "1.5"}`,
	`{"adUnitPath": "21775744923/genuin/instream", "cpm": 1}`,
	`{"adUnitPath": "/21775744923/genuin instream", "cpm": 1}`,
	`{"adUnitPath": "", "cpm": 1}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 1, "custParams": {"tier": 1}}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 1, "custParams": ["tier"]}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 1, "custParams": {"k": "a&b"}}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 1, "custParams": {"k": "a=b"}}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 1, "sizes": "640x480"}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 1, "sizes": ["640x480", "640x480"]}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 1, "sizes": ["640 x 480"]}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 1, "sizes": ["640x480|400x300"]}`,
	`{"adUnitPath": "/21775744923/genuin/instream", "cpm": 1, "descriptionUrl": "not a url"}`,
	// half of each shape
	`{"customerId": "3119", "cpm": 1}`,
	`{"tagId": "3753", "adUnitPath": "/21775744923/genuin/instream"}`,
}
