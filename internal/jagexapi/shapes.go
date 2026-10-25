package jagexapi

import (
	"maps"

	"jagex_account_creator/internal/useragent"
)

// requestShape is the header order that Chrome sends for one kind of request,
// and the header values that this kind fixes. The client runs in TLS-only mode,
// so a request sends only the headers that its shape names.
// The orders and values copy the Chrome major that ShapeChromeMajor names.
type requestShape struct {
	order  []string
	values map[string]string
}

// ShapeChromeMajor is the Chrome major that the shapes and the TLS preset copy.
// On a bump, measure the shapes again. httpcloak must have a preset for the new major.
const ShapeChromeMajor = 155

const (
	// navigationAccept is the accept of a page load.
	navigationAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,image/jxl,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"
	// axiosAccept is the accept that the HTTP library of the two SPAs sends by default.
	axiosAccept = "application/json, text/plain, */*"
	// jsonOnlyAccept is the accept of the XHRs that take JSON only.
	jsonOnlyAccept = "application/json"
)

// sharedValues returns the values that every shape sends.
func sharedValues() map[string]string {
	return map[string]string{
		// Limitation: every target is on account.jagex.com. A target on another host would send a wrong site value.
		"sec-fetch-site":  "same-origin",
		"accept-encoding": "gzip, deflate, br, zstd",
		"accept-language": useragent.AcceptLanguage,
	}
}

// xhrValues returns the values of an XHR, with fixed added.
func xhrValues(fixed map[string]string) map[string]string {
	v := sharedValues()
	v["sec-fetch-mode"] = "cors"
	v["sec-fetch-dest"] = "empty"
	v["priority"] = "u=1, i"
	maps.Copy(v, fixed)
	return v
}

// navigationValues returns the values of a page load. A clicked page load is a
// user activation, so it sends sec-fetch-user.
func navigationValues(clicked bool) map[string]string {
	v := sharedValues()
	v["upgrade-insecure-requests"] = "1"
	v["accept"] = navigationAccept
	v["sec-fetch-mode"] = "navigate"
	v["sec-fetch-dest"] = "document"
	v["priority"] = "u=0, i"
	if clicked {
		v["sec-fetch-user"] = "?1"
	}
	return v
}

// postValues returns the values of a JSON XHR POST, with fixed added.
func postValues(fixed map[string]string) map[string]string {
	v := xhrValues(map[string]string{"accept": axiosAccept, "content-type": "application/json", "origin": baseURL})
	maps.Copy(v, fixed)
	return v
}

// loginLinkShape is the manage SPA's XHR for the login link. Chrome sends it
// before the challenge asks for the high-entropy hints, so it has three hints.
func loginLinkShape() requestShape {
	return requestShape{
		order: []string{
			"sec-ch-ua-platform", "user-agent", "accept", "sec-ch-ua", "locale", "sec-ch-ua-mobile",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "referer",
			"accept-encoding", "accept-language", "cookie", "priority",
		},
		values: xhrValues(map[string]string{"accept": axiosAccept, "locale": siteLocale}),
	}
}

// navigationShape is a page load that is not a redirect follow.
func navigationShape(clicked bool) requestShape {
	return requestShape{
		order: []string{
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-full-version", "sec-ch-ua-arch", "sec-ch-ua-platform",
			"sec-ch-ua-platform-version", "sec-ch-ua-model", "sec-ch-ua-bitness", "sec-ch-ua-full-version-list",
			"upgrade-insecure-requests", "user-agent", "accept",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-user", "sec-fetch-dest", "referer",
			"accept-encoding", "accept-language", "cookie", "priority",
		},
		values: navigationValues(clicked),
	}
}

// redirectShape is a page load that follows a redirect. Chrome sends the hints after sec-fetch-dest here.
func redirectShape(clicked bool) requestShape {
	return requestShape{
		order: []string{
			"upgrade-insecure-requests", "user-agent", "accept",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-user", "sec-fetch-dest",
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-full-version", "sec-ch-ua-arch", "sec-ch-ua-platform",
			"sec-ch-ua-platform-version", "sec-ch-ua-model", "sec-ch-ua-bitness", "sec-ch-ua-full-version-list",
			"referer", "accept-encoding", "accept-language", "cookie", "priority",
		},
		values: navigationValues(clicked),
	}
}

// xhrPostShape is a JSON XHR POST with no locale: the login SPA's first call, the registration init.
// Limitation: the consent POST is not measured and sends no locale.
func xhrPostShape() requestShape {
	return requestShape{
		order: []string{
			"content-length", "sec-ch-ua-full-version-list", "sec-ch-ua-platform", "sec-ch-ua", "sec-ch-ua-bitness",
			"sec-ch-ua-model", "sec-ch-ua-mobile", "sec-ch-ua-arch", "sec-ch-ua-full-version",
			"accept", "content-type", "user-agent", "sec-ch-ua-platform-version", "origin",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "referer",
			"accept-encoding", "accept-language", "cookie", "priority",
		},
		values: postValues(nil),
	}
}

// currentGetShape is the login SPA's XHR GET of the registration state. After
// the first 200 of /current, every GET of it sends if-none-match from Chrome's HTTP cache.
func currentGetShape() requestShape {
	return requestShape{
		order: []string{
			"sec-ch-ua-full-version-list", "sec-ch-ua-platform", "sec-ch-ua", "sec-ch-ua-bitness",
			"sec-ch-ua-model", "sec-ch-ua-mobile", "sec-ch-ua-arch", "sec-ch-ua-full-version",
			"accept", "locale", "user-agent", "sec-ch-ua-platform-version",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "referer",
			"accept-encoding", "accept-language", "cookie", "if-none-match", "priority",
		},
		values: xhrValues(map[string]string{"accept": axiosAccept, "locale": siteLocale}),
	}
}

// localeXHRPostShape is a JSON XHR POST that adds locale: the login SPA's calls
// after the init, and the manage SPA's calls.
// Limitation: no capture holds Chrome's own token and TOTP POSTs. The order is Chrome's order for the same header set.
func localeXHRPostShape() requestShape {
	return requestShape{
		order: []string{
			"content-length", "sec-ch-ua-full-version-list", "sec-ch-ua-platform", "sec-ch-ua", "sec-ch-ua-bitness",
			"sec-ch-ua-model", "sec-ch-ua-mobile", "sec-ch-ua-arch", "sec-ch-ua-full-version",
			"accept", "content-type", "locale", "user-agent", "sec-ch-ua-platform-version", "origin",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "referer",
			"accept-encoding", "accept-language", "cookie", "priority",
		},
		values: postValues(map[string]string{"locale": siteLocale}),
	}
}
