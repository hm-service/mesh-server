package api

import (
	"net/http"
	"net/url"
	"strings"
)

// absoluteURL turns a server-side download path into an address the client can
// fetch directly. Behind a reverse proxy the scheme comes from
// X-Forwarded-Proto; otherwise it follows the connection itself. An address that
// is already absolute is returned unchanged.
func absoluteURL(r *http.Request, ref string) string {
	if ref == "" {
		return ""
	}
	if u, err := url.Parse(ref); err == nil && u.IsAbs() {
		return ref
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		if i := strings.Index(proto, ","); i >= 0 {
			proto = proto[:i]
		}
		if p := strings.TrimSpace(proto); p != "" {
			scheme = p
		}
	}
	return scheme + "://" + r.Host + ref
}
