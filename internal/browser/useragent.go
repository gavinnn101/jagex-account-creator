package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mafredri/cdp/rpcc"

	"jagex_account_creator/internal/useragent"
)

// UserAgentBrand is one entry of a brand list in the UA metadata.
type UserAgentBrand struct {
	Brand   string `json:"brand"`
	Version string `json:"version"`
}

// UserAgentMetadata is the UA metadata that Chrome reports. Its JSON shape is
// the CDP UserAgentMetadata type plus formFactors.
type UserAgentMetadata struct {
	Brands          []UserAgentBrand `json:"brands"`
	FullVersionList []UserAgentBrand `json:"fullVersionList"`
	Platform        string           `json:"platform"`
	PlatformVersion string           `json:"platformVersion"`
	Architecture    string           `json:"architecture"`
	Model           string           `json:"model"`
	Mobile          bool             `json:"mobile"`
	Bitness         string           `json:"bitness"`
	Wow64           bool             `json:"wow64"`
	FormFactors     []string         `json:"formFactors"`
}

// UserAgentOverride is the UA string and the UA metadata of a real Chrome.
// SetUserAgentOverride and TargetAttacher send it to the targets of a headless
// Chrome.
type UserAgentOverride struct {
	UserAgent string
	Metadata  UserAgentMetadata
}

// setUserAgentOverrideArgs is the params object of Network.setUserAgentOverride.
type setUserAgentOverrideArgs struct {
	UserAgent         string            `json:"userAgent"`
	UserAgentMetadata UserAgentMetadata `json:"userAgentMetadata"`
}

// SetUserAgentOverride sends override to the page target of c. Call it before
// the first navigation.
func (c *Client) SetUserAgentOverride(ctx context.Context, override *UserAgentOverride) error { // coverage-ignore: needs a live Chrome
	return setUserAgentOverride(ctx, c.conn, override)
}

// setUserAgentOverride sends override to the target of conn.
func setUserAgentOverride(ctx context.Context, conn *rpcc.Conn, override *UserAgentOverride) error { // coverage-ignore: needs a live Chrome
	args := setUserAgentOverrideArgs{UserAgent: override.UserAgent, UserAgentMetadata: override.Metadata}
	// cdp v0.35.0 has no Network binding for this method.
	// Its Emulation binding is outside the allowed domains, and its metadata struct has no formFactors.
	if err := rpcc.Invoke(ctx, "Network.setUserAgentOverride", &args, nil, conn); err != nil {
		return fmt.Errorf("setting the user agent override: %w", err)
	}
	return nil
}

// userAgentJS reads the UA and the UA metadata in one evaluation. The result of
// getHighEntropyValues also holds brands, mobile and platform.
const userAgentJS = `(async () => {
	const d = navigator.userAgentData;
	if (!d) {
		return {ua: navigator.userAgent};
	}
	const values = await d.getHighEntropyValues(["architecture", "bitness", "formFactors", "fullVersionList", "model", "platformVersion", "uaFullVersion", "wow64"]);
	return {ua: navigator.userAgent, values: values};
})()`

// errNoUserAgentData reports a page with no navigator.userAgentData. A page that
// is not a secure context has none.
var errNoUserAgentData = errors.New("navigator.userAgentData unavailable")

// highEntropyValues is the result of getHighEntropyValues.
type highEntropyValues struct {
	UserAgentMetadata

	UAFullVersion string `json:"uaFullVersion"`
}

// uaReading is the result of userAgentJS.
type uaReading struct {
	UserAgent string             `json:"ua"`
	Values    *highEntropyValues `json:"values"`
}

// clientHints returns the client hint header values that Chrome sends for r.
func (r uaReading) clientHints() (*useragent.ClientHints, error) {
	// Cloudflare compares the browser's client hints with the ones the API
	// requests send, so a session without them cannot stay coherent.
	v := r.Values
	if v == nil || len(v.Brands) == 0 {
		return nil, errNoUserAgentData
	}
	mobile := "?0"
	if v.Mobile {
		mobile = "?1"
	}
	return &useragent.ClientHints{
		SecChUA:                brandList(v.Brands),
		SecChUAMobile:          mobile,
		SecChUAPlatform:        sfString(v.Platform),
		SecChUAArch:            sfString(v.Architecture),
		SecChUABitness:         sfString(v.Bitness),
		SecChUAFullVersion:     sfString(v.UAFullVersion),
		SecChUAFullVersionList: brandList(v.FullVersionList),
		SecChUAModel:           sfString(v.Model),
		SecChUAPlatformVersion: sfString(v.PlatformVersion),
	}, nil
}

// override returns the UA override that r describes, with the UA of headful Chrome.
func (r uaReading) override() (*UserAgentOverride, error) {
	if r.Values == nil || len(r.Values.Brands) == 0 {
		return nil, errNoUserAgentData
	}
	ua, err := headfulUserAgent(r.UserAgent)
	if err != nil {
		return nil, err
	}
	return &UserAgentOverride{UserAgent: ua, Metadata: r.Values.UserAgentMetadata}, nil
}

// headfulUserAgent returns the UA of headful Chrome for the UA ua of headless Chrome.
func headfulUserAgent(ua string) (string, error) {
	// Limitation: the replace approximates Chrome's UA grammar. A token renamed without the word Headless passes the check.
	ua = strings.Replace(ua, "HeadlessChrome/", "Chrome/", 1)
	if strings.Contains(ua, "Headless") {
		return "", fmt.Errorf("user agent %q still names a headless Chrome", ua)
	}
	return ua, nil
}

// brandList formats brands as the value of sec-ch-ua or sec-ch-ua-full-version-list.
func brandList(brands []UserAgentBrand) string {
	items := make([]string, len(brands))
	for i, b := range brands {
		items[i] = sfString(b.Brand) + ";v=" + sfString(b.Version)
	}
	return strings.Join(items, ", ")
}

// sfString formats s as a structured-field string (RFC 8941), the form of each
// quoted client hint value. Such a string escapes only the backslash and the quote.
func sfString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
