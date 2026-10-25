// Package useragent holds the client hints and the language that Chrome sends.
package useragent

import "iter"

// ClientHints holds the browser's client hint header values, as Chrome sends them.
// The HTTP client must send the same values as the browser that earned cf_clearance.
type ClientHints struct {
	SecChUA                string // e.g. `"Google Chrome";v="155", "Chromium";v="155", "Not(A:Brand";v="24"`
	SecChUAMobile          string // e.g. `?0`
	SecChUAPlatform        string // e.g. `"Windows"`
	SecChUAArch            string // e.g. `"x86"`
	SecChUABitness         string // e.g. `"64"`
	SecChUAFullVersion     string // e.g. `"155.0.8059.40"`
	SecChUAFullVersionList string // e.g. `"Google Chrome";v="155.0.8059.40", "Chromium";v="155.0.8059.40", "Not(A:Brand";v="24.0.0.0"`
	SecChUAModel           string // e.g. `""`
	SecChUAPlatformVersion string // e.g. `"19.0.0"`
}

// Headers yields the header name and the value of each hint, in field order.
func (h ClientHints) Headers() iter.Seq2[string, string] {
	return func(yield func(string, string) bool) {
		pairs := [...][2]string{
			{"sec-ch-ua", h.SecChUA},
			{"sec-ch-ua-mobile", h.SecChUAMobile},
			{"sec-ch-ua-platform", h.SecChUAPlatform},
			{"sec-ch-ua-arch", h.SecChUAArch},
			{"sec-ch-ua-bitness", h.SecChUABitness},
			{"sec-ch-ua-full-version", h.SecChUAFullVersion},
			{"sec-ch-ua-full-version-list", h.SecChUAFullVersionList},
			{"sec-ch-ua-model", h.SecChUAModel},
			{"sec-ch-ua-platform-version", h.SecChUAPlatformVersion},
		}
		for _, p := range pairs {
			if !yield(p[0], p[1]) {
				return
			}
		}
	}
}
