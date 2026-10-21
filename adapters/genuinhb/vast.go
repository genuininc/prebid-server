package genuinhb

import (
	"bytes"
	"encoding/xml"
	"io"
	"math"
	"strconv"
	"strings"
)

// vastDocument models only the parts of a VAST 2.0-4.x document the adapter reads. The
// markup itself is passed to the player untouched, so unknown elements are ignored.
type vastDocument struct {
	XMLName xml.Name `xml:"VAST"`
	Ads     []vastAd `xml:"Ad"`
}

type vastAd struct {
	ID      string      `xml:"id,attr"`
	InLine  *vastAdBody `xml:"InLine"`
	Wrapper *vastAdBody `xml:"Wrapper"`
}

type vastAdBody struct {
	Pricing   *vastPricing   `xml:"Pricing"`
	Creatives []vastCreative `xml:"Creatives>Creative"`
}

type vastPricing struct {
	Model    string `xml:"model,attr"`
	Currency string `xml:"currency,attr"`
	Value    string `xml:",chardata"`
}

type vastCreative struct {
	ID     string      `xml:"id,attr"`
	Linear *vastLinear `xml:"Linear"`
}

type vastLinear struct {
	Duration   string          `xml:"Duration"`
	MediaFiles []vastMediaFile `xml:"MediaFiles>MediaFile"`
}

type vastMediaFile struct {
	Type string `xml:"type,attr"`
}

// vastInfo is what the adapter needs from the first ad of a VAST response.
type vastInfo struct {
	// hasAd is false for an empty VAST (no <Ad>), which is the VAST way of saying "no fill".
	hasAd bool
	// price is the <Pricing> CPM; 0 when absent, not a CPM, or not a positive finite number.
	price float64
	// priceCurrency is the <Pricing currency> attribute, upper-cased; "" when absent.
	priceCurrency string
	adID          string
	creativeID    string
	// durationSec is the first Linear <Duration>, rounded up to whole seconds; 0 when unknown.
	durationSec int
	// audioOnly is true when every MediaFile of the first Linear creative has an audio/* MIME type.
	audioOnly bool
}

// parseVAST extracts vastInfo from a VAST document. Only the first <Ad> is inspected: VAST pods
// and buffets are passed through as-is and the player decides what to play.
func parseVAST(body []byte) (vastInfo, error) {
	var doc vastDocument
	decoder := xml.NewDecoder(bytes.NewReader(body))
	// Only ASCII fields (ids, numbers, MIME types) are read and the markup is never re-encoded,
	// so a non-UTF-8 encoding declaration must not make an otherwise valid VAST unusable.
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	if err := decoder.Decode(&doc); err != nil {
		return vastInfo{}, err
	}

	for _, ad := range doc.Ads {
		adBody := ad.InLine
		if adBody == nil {
			adBody = ad.Wrapper
		}
		if adBody == nil {
			continue
		}

		info := vastInfo{hasAd: true, adID: strings.TrimSpace(ad.ID)}
		if adBody.Pricing != nil {
			info.price, info.priceCurrency = parsePricing(adBody.Pricing)
		}
		for _, creative := range adBody.Creatives {
			if info.creativeID == "" {
				info.creativeID = strings.TrimSpace(creative.ID)
			}
			if creative.Linear != nil {
				info.durationSec = parseDuration(creative.Linear.Duration)
				info.audioOnly = allAudio(creative.Linear.MediaFiles)
				break
			}
		}
		return info, nil
	}

	return vastInfo{}, nil
}

// parsePricing returns the CPM and its currency, or 0 when the element is not a usable CPM price.
func parsePricing(p *vastPricing) (float64, string) {
	model := strings.TrimSpace(p.Model)
	if model != "" && !strings.EqualFold(model, "cpm") {
		return 0, ""
	}
	price, err := strconv.ParseFloat(strings.TrimSpace(p.Value), 64)
	if err != nil || math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 {
		return 0, ""
	}
	return price, strings.ToUpper(strings.TrimSpace(p.Currency))
}

// parseDuration converts a VAST HH:MM:SS or HH:MM:SS.mmm duration into whole seconds, rounding
// fractional seconds up. It returns 0 for anything else.
func parseDuration(value string) int {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 3 {
		return 0
	}
	hours, errH := strconv.Atoi(parts[0])
	minutes, errM := strconv.Atoi(parts[1])
	seconds, errS := strconv.ParseFloat(parts[2], 64)
	if errH != nil || errM != nil || errS != nil || hours < 0 || minutes < 0 || minutes > 59 || seconds < 0 || seconds >= 60 {
		return 0
	}
	return hours*3600 + minutes*60 + int(math.Ceil(seconds))
}

func allAudio(files []vastMediaFile) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(f.Type)), "audio/") {
			return false
		}
	}
	return true
}
