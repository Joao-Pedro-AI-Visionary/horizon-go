package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// Writes (every method other than GET, HEAD and OPTIONS under /api) need
// "Authorization: Bearer <token>", where token is either a session token from
// POST /api/auth/login or the static AdminToken (GitHub Actions schedule, X
// radar push). Reads stay public, except /api/admin/* (alerts), which needs
// the same token.
//
// With neither configured, writes are open only when RequireAdmin is false
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
func (s *Server) WritesOpen() bool {
	return s.AdminToken == "" && !s.Login.Enabled() && !s.RequireAdmin
}

// authorized reports whether tok is the admin token or a valid session.
func (s *Server) authorized(tok string) bool {
	if s.AdminToken != "" && tokenEqual(tok, s.AdminToken) {
		return true
	}
	if s.Login.Enabled() {
		if _, _, err := s.Login.Verify(tok); err == nil {
			return true
		}
	}
	return false
}

// publicWrites are POSTs anyone may call (they check credentials themselves).
var publicWrites = map[string]bool{
	"/api/auth/login": true, "/api/auth/logout": true,
	"/api/auth/forgot": true, "/api/auth/reset": true,
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adminRead := strings.HasPrefix(r.URL.Path, "/api/admin/")
		if (isRead(r.Method) && !adminRead) || s.WritesOpen() || publicWrites[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		if s.AdminToken == "" && !s.Login.Enabled() {
			writeError(w, http.StatusServiceUnavailable, "writes disabled: no admin credentials configured")
			return
		}
		got, ok := bearer(r)
		if !ok || !s.authorized(got) {
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

// authStatus (public GET /api/auth) tells the dashboard how writes work
// without a failed request: "open" (dev, no login needed), "login" (sign in
// at /login), "token" (only the static admin token) or "disabled".
func (s *Server) authStatus(w http.ResponseWriter, _ *http.Request) {
	mode := "token"
	switch {
	case s.WritesOpen():
		mode = "open"
	case s.Login.Enabled():
		mode = "login"
	case s.AdminToken == "":
		mode = "disabled"
	}
	writeJSON(w, http.StatusOK, map[string]string{"mode": mode})
}
