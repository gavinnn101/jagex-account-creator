package browser

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"jagex_account_creator/internal/useragent"
)

// The UAs that headful and headless Chrome 155 reported on chrome://version.
const (
	measuredHeadfulUA  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36"
	measuredHeadlessUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/155.0.0.0 Safari/537.36"
)

// measuredReadingJSON is the userAgentJS result of headless Chrome 155 on chrome://version.
const measuredReadingJSON = `{"ua":"` + measuredHeadlessUA + `","values":{"architecture":"x86","bitness":"64","brands":[{"brand":"Google Chrome","version":"155"},{"brand":"Chromium","version":"155"},{"brand":"Not(A:Brand","version":"24"}],"formFactors":["Desktop"],"fullVersionList":[{"brand":"Google Chrome","version":"155.0.8059.40"},{"brand":"Chromium","version":"155.0.8059.40"},{"brand":"Not(A:Brand","version":"24.0.0.0"}],"mobile":false,"model":"","platform":"Windows","platformVersion":"19.0.0","uaFullVersion":"155.0.8059.40","wow64":false}}`

// measuredMetadata returns the UA metadata of Chrome 155 on Windows.
func measuredMetadata() UserAgentMetadata {
	return UserAgentMetadata{
		Brands:          []UserAgentBrand{{"Google Chrome", "155"}, {"Chromium", "155"}, {"Not(A:Brand", "24"}},
		FullVersionList: []UserAgentBrand{{"Google Chrome", "155.0.8059.40"}, {"Chromium", "155.0.8059.40"}, {"Not(A:Brand", "24.0.0.0"}},
		Platform:        "Windows",
		PlatformVersion: "19.0.0",
		Architecture:    "x86",
		Model:           "",
		Mobile:          false,
		Bitness:         "64",
		Wow64:           false,
		FormFactors:     []string{"Desktop"},
	}
}

// testOverride returns an override with the UA ua and the measured metadata.
func testOverride(ua string) *UserAgentOverride {
	return &UserAgentOverride{UserAgent: ua, Metadata: measuredMetadata()}
}

// measuredReading returns measuredReadingJSON as a uaReading.
func measuredReading(t *testing.T) uaReading {
	t.Helper()
	var r uaReading
	if err := json.Unmarshal([]byte(measuredReadingJSON), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHeadfulUserAgent(t *testing.T) {
	tests := []struct {
		name    string
		ua      string
		want    string
		wantErr bool
	}{
		{name: "headless UA", ua: measuredHeadlessUA, want: measuredHeadfulUA},
		{name: "headful UA", ua: measuredHeadfulUA, want: measuredHeadfulUA},
		{name: "token without the slash", ua: "Mozilla/5.0 HeadlessChrome 155.0.0.0", wantErr: true},
		{name: "a second headless token", ua: "HeadlessChrome/155.0.0.0 Safari/537.36 HeadlessChrome/155.0.0.0", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := headfulUserAgent(tt.ua)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("headfulUserAgent(%q) = %q, %v, want %q, error %v", tt.ua, got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestUAReadingClientHints(t *testing.T) {
	// The values that Chrome sent on the wire for the registration XHR.
	want := &useragent.ClientHints{
		SecChUA:                `"Google Chrome";v="155", "Chromium";v="155", "Not(A:Brand";v="24"`,
		SecChUAMobile:          `?0`,
		SecChUAPlatform:        `"Windows"`,
		SecChUAArch:            `"x86"`,
		SecChUABitness:         `"64"`,
		SecChUAFullVersion:     `"155.0.8059.40"`,
		SecChUAFullVersionList: `"Google Chrome";v="155.0.8059.40", "Chromium";v="155.0.8059.40", "Not(A:Brand";v="24.0.0.0"`,
		SecChUAModel:           `""`,
		SecChUAPlatformVersion: `"19.0.0"`,
	}

	got, err := measuredReading(t).clientHints()
	if err != nil {
		t.Fatal(err)
	}
	if *got != *want {
		t.Errorf("clientHints() = %+v, want %+v", got, want)
	}
}

func TestUAReadingClientHintsMobile(t *testing.T) {
	r := measuredReading(t)
	r.Values.Mobile = true

	got, err := r.clientHints()
	if err != nil {
		t.Fatal(err)
	}
	if got.SecChUAMobile != "?1" {
		t.Errorf("SecChUAMobile = %q, want ?1", got.SecChUAMobile)
	}
}

func TestUAReadingNoUserAgentData(t *testing.T) {
	tests := []struct {
		name    string
		reading uaReading
	}{
		{name: "no values", reading: uaReading{UserAgent: measuredHeadlessUA}},
		{name: "no brands", reading: uaReading{UserAgent: measuredHeadlessUA, Values: &highEntropyValues{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.reading.clientHints(); !errors.Is(err, errNoUserAgentData) {
				t.Errorf("clientHints() error = %v, want %v", err, errNoUserAgentData)
			}
			if _, err := tt.reading.override(); !errors.Is(err, errNoUserAgentData) {
				t.Errorf("override() error = %v, want %v", err, errNoUserAgentData)
			}
		})
	}
}

func TestUAReadingOverride(t *testing.T) {
	got, err := measuredReading(t).override()
	if err != nil {
		t.Fatal(err)
	}
	if want := testOverride(measuredHeadfulUA); !reflect.DeepEqual(got, want) {
		t.Errorf("override() = %+v, want %+v", got, want)
	}
}

func TestUAReadingOverrideHeadless(t *testing.T) {
	r := measuredReading(t)
	r.UserAgent = "Mozilla/5.0 HeadlessShell/155.0.0.0"

	if got, err := r.override(); err == nil {
		t.Errorf("override() = %+v, want an error for a headless UA", got)
	}
}

// TestSetUserAgentOverrideArgs checks the params against the override that
// passed the live challenge.
func TestSetUserAgentOverrideArgs(t *testing.T) {
	const measured = `{"userAgent": "` + measuredHeadfulUA + `", "userAgentMetadata": {"brands": [{"brand": "Google Chrome", "version": "155"}, {"brand": "Chromium", "version": "155"}, {"brand": "Not(A:Brand", "version": "24"}], "fullVersionList": [{"brand": "Google Chrome", "version": "155.0.8059.40"}, {"brand": "Chromium", "version": "155.0.8059.40"}, {"brand": "Not(A:Brand", "version": "24.0.0.0"}], "platform": "Windows", "platformVersion": "19.0.0", "architecture": "x86", "model": "", "mobile": false, "bitness": "64", "wow64": false, "formFactors": ["Desktop"]}}`
	override := testOverride(measuredHeadfulUA)
	data, err := json.Marshal(setUserAgentOverrideArgs{UserAgent: override.UserAgent, UserAgentMetadata: override.Metadata})
	if err != nil {
		t.Fatal(err)
	}

	var got, want any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(measured), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("params = %s, want %s", data, measured)
	}
}

func TestSFString(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: `""`},
		{in: "Not(A:Brand", want: `"Not(A:Brand"`},
		{in: `a"b\c`, want: `"a\"b\\c"`},
	}
	for _, tt := range tests {
		if got := sfString(tt.in); got != tt.want {
			t.Errorf("sfString(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}
