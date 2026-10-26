package genuinhb

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseVAST pins what the parser extracts from each fixture; expectations were read off the
// fixture files by hand.
func TestParseVAST(t *testing.T) {
	tests := []struct {
		fixture string
		wantErr bool
		want    vastInfo
	}{
		{fixture: "inline-pricing.xml", want: vastInfo{hasAd: true, price: 2.5, priceCurrency: "USD", adID: "ad-123", creativeID: "cr-1", durationSec: 15}},
		{fixture: "wrapper-pricing.xml", want: vastInfo{hasAd: true, price: 1.75, priceCurrency: "USD", adID: "wr-1"}},
		{fixture: "audio.xml", want: vastInfo{hasAd: true, price: 1.2, priceCurrency: "USD", adID: "audio-1", creativeID: "cr-audio", durationSec: 20, audioOnly: true}},
		{fixture: "no-pricing.xml", want: vastInfo{hasAd: true, creativeID: "cr-np", durationSec: 31}},
		{fixture: "no-ids.xml", want: vastInfo{hasAd: true, price: 1, priceCurrency: "USD", durationSec: 10}},
		{fixture: "cpc-pricing.xml", want: vastInfo{hasAd: true, adID: "cpc-1"}},
		{fixture: "zero-pricing.xml", want: vastInfo{hasAd: true, adID: "zero-1"}},
		{fixture: "nan-pricing.xml", want: vastInfo{hasAd: true, adID: "nan-1"}},
		{fixture: "bad-currency.xml", want: vastInfo{hasAd: true, price: 2, priceCurrency: "DOLLARS", adID: "cur-1"}},
		{fixture: "latin1.xml", want: vastInfo{hasAd: true, price: 0.9, priceCurrency: "USD", adID: "latin-1"}},
		{fixture: "empty.xml", want: vastInfo{}},
		{fixture: "error-only.xml", want: vastInfo{}},
		{fixture: "malformed.xml", wantErr: true},
		{fixture: "not-vast.xml", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			got, err := parseVAST(loadFixture(t, test.fixture))
			if test.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestParseDuration(t *testing.T) {
	tests := map[string]int{
		"00:00:15":     15,
		" 00:00:10 ":   10,
		"00:01:00.000": 60,
		"00:00:30.5":   31,
		"01:02:03":     3723,
		"":             0,
		"15":           0,
		"00:15":        0,
		"00:61:00":     0,
		"00:00:60":     0,
		"aa:bb:cc":     0,
		"-1:00:00":     0,
	}
	for value, want := range tests {
		assert.Equal(t, want, parseDuration(value), "duration %q", value)
	}
}
