package hub

import (
	"net/http"
	"strings"
)

// contentSecurityPolicy is ADR-0003 §9, verbatim. Vite emits only external
// module scripts and stylesheets, so nothing inline is needed.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"font-src 'self'; connect-src 'self'; manifest-src 'self'; worker-src 'self'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'; object-src 'none'; upgrade-insecure-requests"

// trustedTypesReportOnly previews Trusted Types enforcement (enforced in v0.2).
const trustedTypesReportOnly = "require-trusted-types-for 'script'"

const permissionsPolicy = "camera=(), microphone=(), geolocation=(), usb=(), payment=(), clipboard-read=()"

// securityHeaders sets the UI listener's response headers. HSTS is sent
// only when publicURL is https. No CORS header is ever set, and preflight
// requests are not answered specially, so cross-origin browser calls fail.
func securityHeaders(hsts bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("Content-Security-Policy-Report-Only", trustedTypesReportOnly)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", permissionsPolicy)
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if noStorePath(r.URL.Path) {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// noStorePath reports the prefixes whose responses are never cached. The
// SPA handler sets no-store on index.html itself.
func noStorePath(p string) bool {
	for _, pre := range []string{"/api", "/auth", "/mcp"} {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return p == "/healthz"
}
