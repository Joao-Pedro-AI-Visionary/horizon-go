package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
)

type memCreds struct {
	mu     sync.Mutex
	cred   *domain.AdminCredential
	tokens map[string]time.Time // hash -> expiry
	used   map[string]bool
}

func (m *memCreds) GetCredential(context.Context, string) (domain.AdminCredential, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cred == nil {
		return domain.AdminCredential{}, false, nil
	}
	return *m.cred, true, nil
}

func (m *memCreds) CreateReset(_ context.Context, h string, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tokens == nil {
		m.tokens, m.used = map[string]time.Time{}, map[string]bool{}
	}
	m.tokens[h] = exp
	return nil
}

func (m *memCreds) ConsumeReset(_ context.Context, h, _, newHash string, now time.Time) (domain.AdminCredential, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	exp, ok := m.tokens[h]
	if !ok || m.used[h] || !now.Before(exp) {
		return domain.AdminCredential{}, false, nil
	}
	for k := range m.tokens {
		m.used[k] = true
	}
	v := 1
	if m.cred != nil {
		v = m.cred.SessionVersion + 1
	}
	m.cred = &domain.AdminCredential{PasswordHash: newHash, SessionVersion: v}
	return *m.cred, true, nil
}

type fakeMailer struct {
	mu   sync.Mutex
	to   []string
	text []string
}

func (f *fakeMailer) Send(_ context.Context, to, _, _, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.to, f.text = append(f.to, to), append(f.text, text)
	return "msg-1", nil
}

func resetServer(t *testing.T, now *time.Time) (http.Handler, *memCreds, *fakeMailer) {
	t.Helper()
	creds, mail := &memCreds{}, &fakeMailer{}
	login := testLogin(t, now)
	login.Creds = creds
	clock := func() time.Time { return *now }
	s := &Server{
		Store: &fakeStore{}, Sources: &fakeSources{}, Collector: &countingCollector{},
		AdminToken: "static-token", RequireAdmin: true, Login: login,
		Reset: &PasswordReset{
			Mailer: mail, To: "owner@example.com", FrontURL: "https://web.example/", Now: clock,
			Limiter:    &LoginLimiter{Max: 3, Window: 15 * time.Minute, MaxBlock: time.Hour, Now: clock},
			MaxPerHour: 5,
		},
	}
	return s.Handler(), creds, mail
}

var linkRe = regexp.MustCompile(`https://web\.example/reset\?token=(\S+)`)

func forgotOK(t *testing.T, h http.Handler, body string) {
	t.Helper()
	rec := doAuth(h, http.MethodPost, "/api/auth/forgot", "", body)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), forgotReply) {
		t.Fatalf("forgot: %d %s", rec.Code, rec.Body.String())
	}
}

func loginToken(t *testing.T, h http.Handler, pw string) (string, int) {
	t.Helper()
	rec := doAuth(h, http.MethodPost, "/api/auth/login", "", loginBody("joao", pw))
	var out struct{ Token string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Token, rec.Code
}

func TestForgotMailsOnlyTheConfiguredAddress(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	h, creds, mail := resetServer(t, &now)
	forgotOK(t, h, `{"email":"attacker@evil.example","to":"attacker@evil.example"}`)
	if len(mail.to) != 1 || mail.to[0] != "owner@example.com" {
		t.Fatalf("sent to %v", mail.to)
	}
	m := linkRe.FindStringSubmatch(mail.text[0])
	if m == nil {
		t.Fatalf("no link in %q", mail.text[0])
	}
	tok, _ := url.QueryUnescape(m[1])
	if _, ok := creds.tokens[HashResetToken(tok)]; !ok || len(creds.tokens) != 1 {
		t.Fatal("only the sha256 of the token must be stored")
	}
	if _, raw := creds.tokens[tok]; raw {
		t.Fatal("raw token stored")
	}
	if exp := creds.tokens[HashResetToken(tok)]; !exp.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("expiry %v", exp)
	}
	// Rate limit: same generic 200, no more e-mails after 3 per IP.
	for i := 0; i < 4; i++ {
		forgotOK(t, h, "")
	}
	if len(mail.to) != 3 {
		t.Fatalf("want 3 e-mails within the IP limit, got %d", len(mail.to))
	}
}

func TestResetChangesPasswordOnceAndEndsSessions(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	h, _, mail := resetServer(t, &now)
	old, code := loginToken(t, h, testPassword)
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	forgotOK(t, h, "")
	tok, _ := url.QueryUnescape(linkRe.FindStringSubmatch(mail.text[0])[1])

	body := func(tok, pw string) string {
		b, _ := json.Marshal(map[string]string{"token": tok, "new_password": pw})
		return string(b)
	}
	if rec := doAuth(h, http.MethodPost, "/api/auth/reset", "", body(tok, "short")); rec.Code != http.StatusBadRequest {
		t.Fatalf("short password: %d", rec.Code)
	}
	if rec := doAuth(h, http.MethodPost, "/api/auth/reset", "", body("nope", "a brand new password")); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errBadReset.Error()) {
		t.Fatalf("bad token: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doAuth(h, http.MethodPost, "/api/auth/reset", "", body(tok, "a brand new password")); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body.String())
	}
	// Single use.
	if rec := doAuth(h, http.MethodPost, "/api/auth/reset", "", body(tok, "another new password")); rec.Code != http.StatusBadRequest {
		t.Fatalf("reuse: %d", rec.Code)
	}
	// The old session is gone; the old password no longer works; the new one does.
	if rec := doAuth(h, http.MethodGet, "/api/auth/me", "Bearer "+old, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old session still valid: %d", rec.Code)
	}
	if rec := doAuth(h, http.MethodPost, "/api/pipeline/run", "Bearer "+old, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old session can write: %d", rec.Code)
	}
	if _, code := loginToken(t, h, testPassword); code != http.StatusUnauthorized {
		t.Fatalf("old password: %d", code)
	}
	fresh, code := loginToken(t, h, "a brand new password")
	if code != http.StatusOK {
		t.Fatalf("new password: %d", code)
	}
	if rec := doAuth(h, http.MethodGet, "/api/auth/me", "Bearer "+fresh, ""); rec.Code != http.StatusOK {
		t.Fatalf("new session: %d", rec.Code)
	}
	// The static admin token is unaffected.
	if rec := doAuth(h, http.MethodGet, "/api/auth/me", "Bearer static-token", ""); rec.Code != http.StatusOK {
		t.Fatalf("admin token: %d", rec.Code)
	}
}

func TestResetTokenExpiresAfter30Minutes(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	h, _, mail := resetServer(t, &now)
	forgotOK(t, h, "")
	tok, _ := url.QueryUnescape(linkRe.FindStringSubmatch(mail.text[0])[1])
	now = now.Add(31 * time.Minute)
	b, _ := json.Marshal(map[string]string{"token": tok, "new_password": "a brand new password"})
	if rec := doAuth(h, http.MethodPost, "/api/auth/reset", "", string(b)); rec.Code != http.StatusBadRequest {
		t.Fatalf("expired token: %d", rec.Code)
	}
	if _, code := loginToken(t, h, testPassword); code != http.StatusOK {
		t.Fatalf("env password must keep working: %d", code)
	}
}

func TestForgotWithoutConfigIsStillGeneric(t *testing.T) {
	now := time.Now()
	s := &Server{Store: &fakeStore{}, Sources: &fakeSources{}, Collector: &countingCollector{}, RequireAdmin: true, Login: testLogin(t, &now)}
	rec := doAuth(s.Handler(), http.MethodPost, "/api/auth/forgot", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), forgotReply) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
