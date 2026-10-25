package useragent

import (
	"slices"
	"testing"
)

func testHints() ClientHints {
	return ClientHints{
		SecChUA:                "ua",
		SecChUAMobile:          "mobile",
		SecChUAPlatform:        "platform",
		SecChUAArch:            "arch",
		SecChUABitness:         "bitness",
		SecChUAFullVersion:     "full-version",
		SecChUAFullVersionList: "full-version-list",
		SecChUAModel:           "model",
		SecChUAPlatformVersion: "platform-version",
	}
}

func TestClientHintsHeaders(t *testing.T) {
	want := [][2]string{
		{"sec-ch-ua", "ua"},
		{"sec-ch-ua-mobile", "mobile"},
		{"sec-ch-ua-platform", "platform"},
		{"sec-ch-ua-arch", "arch"},
		{"sec-ch-ua-bitness", "bitness"},
		{"sec-ch-ua-full-version", "full-version"},
		{"sec-ch-ua-full-version-list", "full-version-list"},
		{"sec-ch-ua-model", "model"},
		{"sec-ch-ua-platform-version", "platform-version"},
	}
	var got [][2]string
	for name, value := range testHints().Headers() {
		got = append(got, [2]string{name, value})
	}
	if !slices.Equal(got, want) {
		t.Errorf("Headers() = %v, want %v", got, want)
	}
}

func TestClientHintsHeaders_StopsWhenYieldReturnsFalse(t *testing.T) {
	calls := 0
	testHints().Headers()(func(string, string) bool {
		calls++
		return false
	})
	if calls != 1 {
		t.Errorf("yield calls = %d, want 1", calls)
	}
}
