package triton

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
		if err := validator.Validate(openrtb_ext.BidderTriton, json.RawMessage(validParam)); err != nil {
			t.Errorf("Schema rejected triton params: %s", validParam)
		}
	}
}

// TestInvalidParams makes sure the schema rejects what dsp-service must never save: a missing or
// non-integer station, a tag that could alter the RTB path, an unknown render mode or feed type,
// a non-positive cpm, and a Triton base URL that is not https.
func TestInvalidParams(t *testing.T) {
	validator, err := openrtb_ext.NewBidderParamsValidator("../../static/bidder-params")
	if err != nil {
		t.Fatalf("Failed to fetch the json-schemas. %v", err)
	}

	for _, invalidParam := range invalidParams {
		if err := validator.Validate(openrtb_ext.BidderTriton, json.RawMessage(invalidParam)); err == nil {
			t.Errorf("Schema allowed unexpected params: %s", invalidParam)
		}
	}
}

var validParams = []string{
	`{"stid": 1443132}`, // tagId may come from imp.ext.data.tagId; the bidder checks it
	`{"stid": 1443132, "adRequestUrl": "https://aapi.begenuin.com/goservices/dsp/rtb/3252/6aa25b631b3a25f3c479ef7c"}`,
	`{"stid": 1443132, "tagId": "6a3aa78ba0daccfd439648b8"}`,
	`{"stid": 1, "tagId": "a"}`,
	`{"stid": 1443132, "tagId": "6aa25b631b3a25f3c479ef7c", "brandId": "3252"}`,
	`{"stid": 1443132, "tagId": "abc_DEF-1", "hostRender": "client"}`,
	`{"stid": 1443132, "tagId": "abc", "hostRender": "server"}`,
	`{"stid": 1443132, "tagId": "abc", "cpm": 0.05}`,
	`{"stid": 1443132, "tagId": "` + strings.Repeat("9", 64) + `"}`,
	`{"stid": 1443132, "tagId": "abc", "unknown": true}`,
	`{"stid": 1443132, "baseUrl": "https://cmod-na.live.streamtheworld.com/ondemand/ars"}`,
	`{"stid": 1443132, "baseUrl": "https://cmod-na.live.streamtheworld.com/ondemand/ars", "feedType": "podcast", "formats": {"audio": true, "video": false, "banner": true}}`,
	`{"stid": 1443132, "enabled": false}`,
	`{"stid": 1443132, "formats": {}}`,
}

var invalidParams = []string{
	``,
	`null`,
	`{}`,
	`[]`,
	`{"tagId": "abc"}`,
	`{"stid": "1443132", "tagId": "abc"}`,
	`{"stid": 0, "tagId": "abc"}`,
	`{"stid": -1, "tagId": "abc"}`,
	`{"stid": 1.5, "tagId": "abc"}`,
	`{"stid": 1443132, "tagId": ""}`,
	`{"stid": 1443132, "tagId": "a/b"}`,
	`{"stid": 1443132, "tagId": "../x"}`,
	`{"stid": 1443132, "tagId": "a?b=1"}`,
	`{"stid": 1443132, "tagId": "a b"}`,
	`{"stid": 1443132, "tagId": "` + strings.Repeat("9", 65) + `"}`,
	`{"stid": 1443132, "tagId": "abc", "hostRender": "device"}`,
	`{"stid": 1443132, "tagId": "abc", "hostRender": "Client"}`,
	`{"stid": 1443132, "tagId": "abc", "cpm": 0}`,
	`{"stid": 1443132, "tagId": "abc", "cpm": -1}`,
	`{"stid": 1443132, "tagId": "abc", "cpm": "1"}`,
	`{"stid": 1443132, "baseUrl": "http://cmod-na.live.streamtheworld.com/ondemand/ars"}`,
	`{"stid": 1443132, "baseUrl": "https://cmod-na.live.streamtheworld.com/ondemand/ars?x=1"}`,
	`{"stid": 1443132, "feedType": "video"}`,
	`{"stid": 1443132, "enabled": "yes"}`,
	`{"stid": 1443132, "formats": {"audio": "on"}}`,
	`{"stid": 1443132, "formats": {"native": true}}`,
	`{"stid": 1443132, "tagId": "abc", "brandId": ""}`,
	`{"stid": 1443132, "tagId": "abc", "brandId": "32/52"}`,
	`{"stid": 1443132, "tagId": "abc", "brandId": 3252}`,
}
