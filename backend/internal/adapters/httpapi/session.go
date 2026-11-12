package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Login is the username + password admin login. A successful login returns
// a signed session token (HMAC-SHA256, see Sessions) that the dashboard
// sends as "Authorization: Bearer". Bearer, not cookies: the dashboard
// (Vercel) and the API (Render) are different sites, and third-party
// cookies are blocked by browsers.
type Login struct {
	Username     string
	PasswordHash []byte // bcrypt
	Sessions     *Sessions
	Limiter      *LoginLimiter
}

// Enabled reports whether login is configured.
func (l *Login) Enabled() bool {
	return l != nil && l.Username != "" && len(l.PasswordHash) > 0 && l.Sessions != nil
}

// dummyHash keeps a wrong username as slow as a wrong password.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("horizon-dummy-password"), bcrypt.MinCost)

// Check compares the username in constant time and the password with bcrypt.
func (l *Login) Check(username, password string) bool {
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(l.Username)) == 1
	hash := l.PasswordHash
	if !userOK {
		hash = dummyHash
	}
	pwOK := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	return userOK && pwOK
}

// Sessions signs and verifies stateless session tokens:
// base64url(payload) "." base64url(HMAC-SHA256(secret, payload)).
type Sessions struct {
	Secret []byte
	TTL    time.Duration
	Now    func() time.Time
}

// DefaultSessionTTL is how long a login lasts.
const DefaultSessionTTL = 12 * time.Hour

type sessionClaims struct {
	Sub string `json:"sub"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

var b64 = base64.RawURLEncoding

func (s *Sessions) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sessions) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return DefaultSessionTTL
}

func (s *Sessions) sign(payload string) string {
	m := hmac.New(sha256.New, s.Secret)
	m.Write([]byte("horizon-session.v1." + payload))
	return b64.EncodeToString(m.Sum(nil))
}

// Issue returns a token for sub and its expiry.
func (s *Sessions) Issue(sub string) (string, time.Time) {
	now := s.now()
	exp := now.Add(s.ttl())
	raw, _ := json.Marshal(sessionClaims{Sub: sub, Iat: now.Unix(), Exp: exp.Unix()})
	payload := b64.EncodeToString(raw)
	return payload + "." + s.sign(payload), exp
}

var errBadSession = errors.New("invalid or expired session")

// Verify checks the signature and expiry and returns the subject and expiry.
func (s *Sessions) Verify(token string) (string, time.Time, error) {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || payload == "" || sig == "" || len(s.Secret) == 0 {
		return "", time.Time{}, errBadSession
	}
	if !hmac.Equal([]byte(sig), []byte(s.sign(payload))) {
		return "", time.Time{}, errBadSession
	}
	raw, err := b64.DecodeString(payload)
	if err != nil {
		return "", time.Time{}, errBadSession
	}
	var c sessionClaims
	if err := json.Unmarshal(raw, &c); err != nil || c.Sub == "" {
		return "", time.Time{}, errBadSession
	}
	exp := time.Unix(c.Exp, 0)
	if !s.now().Before(exp) {
		return "", time.Time{}, errBadSession
	}
	return c.Sub, exp, nil
}

// LoginLimiter allows Max failed attempts per IP within Window; beyond that
// the IP is blocked, and every further failure doubles the block (capped at
// MaxBlock). A successful login clears the IP.
type LoginLimiter struct {
	Max      int
	Window   time.Duration
	MaxBlock time.Duration
	Now      func() time.Time

	mu  sync.Mutex
	ips map[string]*attempts
}

type attempts struct {
	start   time.Time
	fails   int
	until   time.Time
	penalty time.Duration
}

func (l *LoginLimiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *LoginLimiter) limits() (int, time.Duration, time.Duration) {
	max, win, block := l.Max, l.Window, l.MaxBlock
	if max <= 0 {
		max = 5
	}
	if win <= 0 {
		win = time.Minute
	}
	if block <= 0 {
		block = 15 * time.Minute
	}
	return max, win, block
}

// Blocked returns how long ip must wait (0 = may try now).
func (l *LoginLimiter) Blocked(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a := l.ips[ip]; a != nil {
		if d := a.until.Sub(l.now()); d > 0 {
			return d
		}
	}
	return 0
}

// Fail records a failed attempt.
func (l *LoginLimiter) Fail(ip string) {
	max, win, maxBlock := l.limits()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ips == nil {
		l.ips = map[string]*attempts{}
	}
	now := l.now()
	a := l.ips[ip]
	if a == nil || (a.penalty == 0 && now.Sub(a.start) > win) || (a.penalty > 0 && now.Sub(a.until) > maxBlock) {
		a = &attempts{start: now}
		l.ips[ip] = a
	}
	a.fails++
	if a.fails >= max {
		if a.penalty == 0 {
			a.penalty = win
		} else {
			a.penalty = min(a.penalty*2, maxBlock)
		}
		a.until = now.Add(a.penalty)
	}
	// Forget old entries so the map stays small.
	for k, v := range l.ips {
		if k != ip && now.Sub(v.start) > maxBlock && now.After(v.until) {
			delete(l.ips, k)
		}
	}
}

// Succeed clears ip.
func (l *LoginLimiter) Succeed(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.ips, ip)
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

const loginFailed = "usuário ou senha inválidos"

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.Login.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "login indisponível")
		return
	}
	ip := clientIP(r)
	if wait := s.Login.Limiter.Blocked(ip); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds()+0.999)))
		writeError(w, http.StatusTooManyRequests, "muitas tentativas, tente de novo mais tarde")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if len(in.Username) > 200 || len(in.Password) > 200 || !s.Login.Check(strings.TrimSpace(in.Username), in.Password) {
		s.Login.Limiter.Fail(ip)
		s.log().Warn("login failed", "ip", ip)
		writeError(w, http.StatusUnauthorized, loginFailed)
		return
	}
	s.Login.Limiter.Succeed(ip)
	token, exp := s.Login.Sessions.Issue(s.Login.Username)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "username": s.Login.Username, "expires_at": exp.UTC()})
}

// me (GET /api/auth/me) returns the session behind the bearer token.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearer(r)
	if ok && s.Login.Enabled() {
		if sub, exp, err := s.Login.Sessions.Verify(tok); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"username": sub, "expires_at": exp.UTC(), "kind": "session"})
			return
		}
	}
	if ok && s.AdminToken != "" && tokenEqual(tok, s.AdminToken) {
		writeJSON(w, http.StatusOK, map[string]any{"username": "admin-token", "kind": "token"})
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="horizon"`)
	writeError(w, http.StatusUnauthorized, "não autenticado")
}

// logout is stateless: the dashboard drops its token; 204 either way.
func (s *Server) logout(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
