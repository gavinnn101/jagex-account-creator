// Package cookies converts cookies between the browser format and the HTTP client format.
package cookies

import (
	"time"

	"github.com/mafredri/cdp/protocol/network"
	"github.com/sardanioss/httpcloak/client"
)

// CDPToHTTP converts a slice of CDP network cookies to httpcloak cookies.
func CDPToHTTP(cdpCookies []network.Cookie) []client.Cookie {
	out := make([]client.Cookie, 0, len(cdpCookies))
	for _, c := range cdpCookies {
		hc := client.Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			HttpOnly: c.HTTPOnly,
			Secure:   c.Secure,
			SameSite: mapSameSite(c.SameSite),
		}
		// Expires is seconds since epoch. 0 or negative means session cookie.
		if c.Expires > 0 {
			hc.Expires = time.Unix(int64(c.Expires), 0)
		}
		out = append(out, hc)
	}
	return out
}

func mapSameSite(ss network.CookieSameSite) string {
	switch ss {
	case network.CookieSameSiteStrict:
		return "Strict"
	case network.CookieSameSiteLax:
		return "Lax"
	case network.CookieSameSiteNone:
		return "None"
	default:
		return ""
	}
}
