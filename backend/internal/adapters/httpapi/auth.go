package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// Writes (every method other than GET, HEAD and OPTIONS under /api) need
// "Authorization: Bearer <AdminToken>". Reads stay public.
//
// With AdminToken empty, writes are open only when RequireAdmin is false
// (local development); with RequireAdmin set (production) they fail closed
// with 503, so a missing secret never leaves the API writable.

func isRead(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// bearer returns the token of an "Authorization: Bearer <token>" header.
func bearer(r *http.Request) (string, bool) {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	t := strings.TrimSpace(h[len(prefix):])
	return t, t != ""
}

// tokenEqual compares in constant time; hashing first hides the length too.
func tokenEqual(got, want string) bool {
	g := sha256.Sum256([]byte(got))
	w := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

// WritesOpen reports whether writes need no token (dev without a token).
func (s *Server) WritesOpen() bool { return s.AdminToken == "" && !s.RequireAdmin }

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isRead(r.Method) || s.WritesOpen() {
			next.ServeHTTP(w, r)
			return
		}
		if s.AdminToken == "" {
			writeError(w, http.StatusServiceUnavailable, "writes disabled: HORIZON_ADMIN_TOKEN is not configured")
			return
		}
		got, ok := bearer(r)
		if !ok || !tokenEqual(got, s.AdminToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="horizon"`)
			writeError(w, http.StatusUnauthorized, "admin token required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authCheck answers 204 when the request carries a valid admin token (the
// dashboard calls it to unlock editing). It sits behind requireAdmin.
func (s *Server) authCheck(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
