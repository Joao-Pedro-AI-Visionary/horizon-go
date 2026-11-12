package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/jpdev/horizon-go/internal/domain"
)

const testPassword = "correct horse battery staple"

func testLogin(t *testing.T, now *time.Time) *Login {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return *now }
	return &Login{
		Username:     "joao",
		PasswordHash: h,
		Sessions:     &Sessions{Secret: []byte(strings.Repeat("k", 32)), TTL: 12 * time.Hour, Now: clock},
		Limiter:      &LoginLimiter{Max: 5, Window: time.Minute, MaxBlock: 15 * time.Minute, Now: clock},
	}
}

func loginServer(t *testing.T, now *time.Time, adminToken string) http.Handler {
	s := &Server{
		Store:          &fakeStore{settings: domain.Settings{ScoreThreshold: 6, DigestMaxItems: 10, DigestCron: "0 7 * * *", Timezone: "America/Sao_Paulo"}},
		Sources:        &fakeSources{},
		Collector:      &countingCollector{},
		ValidateSource: func(domain.SourceConfig) error { return nil },
		ValidateCron:   func(string) error { return nil },
		Window:         time.Hour,
		AdminToken:     adminToken,
		RequireAdmin:   true,
		Login:          testLogin(t, now),
	}
	return s.Handler()
}

func loginBody(user, pw string) string {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pw})
	return string(b)
}

func TestLoginIssuesASessionThatUnlocksWrites(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	h := loginServer(t, &now, "static-token")

	rec := doAuth(h, http.MethodPost, "/api/auth/login", "", loginBody("joao", "wrong"))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), loginFailed) {
		t.Fatalf("wrong password: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doAuth(h, http.MethodPost, "/api/auth/login", "", loginBody("maria", testPassword)); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), loginFailed) {
		t.Fatalf("wrong user: %d %s", rec.Code, rec.Body.String())
	}
	rec = doAuth(h, http.MethodPost, "/api/auth/login", "", loginBody("joao", testPassword))
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Token     string    `json:"token"`
		Username  string    `json:"username"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Token == "" || out.Username != "joao" || !out.ExpiresAt.Equal(now.Add(12*time.Hour)) {
		t.Fatalf("login body %+v %v", out, err)
	}
	if strings.Contains(rec.Body.String(), testPassword) {
		t.Fatal("password echoed")
	}

	if rec := doAuth(h, http.MethodGet, "/api/auth/me", "Bearer "+out.Token, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"joao"`) {
		t.Fatalf("me: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doAuth(h, http.MethodPost, "/api/pipeline/run", "Bearer "+out.Token, ""); rec.Code != http.StatusOK {
		t.Fatalf("write with session: %d", rec.Code)
	}
	// The static admin token keeps working for the schedule and the X push.
	if rec := doAuth(h, http.MethodPost, "/api/auth/check", "Bearer static-token", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("admin token: %d", rec.Code)
	}
	// Tampered and expired sessions are refused.
	if rec := doAuth(h, http.MethodPost, "/api/auth/check", "Bearer "+out.Token+"x", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered: %d", rec.Code)
	}
	now = now.Add(12*time.Hour + time.Second)
	if rec := doAuth(h, http.MethodPost, "/api/auth/check", "Bearer "+out.Token, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired: %d", rec.Code)
	}
	if rec := doAuth(h, http.MethodGet, "/api/auth/me", "Bearer "+out.Token, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired me: %d", rec.Code)
	}
	if rec := doAuth(h, http.MethodPost, "/api/auth/logout", "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", rec.Code)
	}
}

func TestLoginRateLimitWithBackoff(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	h := loginServer(t, &now, "")
	for i := 0; i < 5; i++ {
		if rec := doAuth(h, http.MethodPost, "/api/auth/login", "", loginBody("joao", "nope")); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	// Blocked, even with the right password.
	rec := doAuth(h, http.MethodPost, "/api/auth/login", "", loginBody("joao", testPassword))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("blocked: %d retry-after=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// After the block, one more failure doubles it.
	now = now.Add(61 * time.Second)
	if rec := doAuth(h, http.MethodPost, "/api/auth/login", "", loginBody("joao", "nope")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("after block: %d", rec.Code)
	}
	if rec := doAuth(h, http.MethodPost, "/api/auth/login", "", loginBody("joao", testPassword)); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "120" {
		t.Fatalf("backoff: %d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	now = now.Add(121 * time.Second)
	if rec := doAuth(h, http.MethodPost, "/api/auth/login", "", loginBody("joao", testPassword)); rec.Code != http.StatusOK {
		t.Fatalf("after backoff: %d", rec.Code)
	}
}

func TestAuthStatusLoginMode(t *testing.T) {
	now := time.Now()
	rec := doAuth(loginServer(t, &now, ""), http.MethodGet, "/api/auth", "", "")
	if !strings.Contains(rec.Body.String(), `"mode":"login"`) {
		t.Fatalf("got %s", rec.Body.String())
	}
}

func TestSessionsRejectOtherSecrets(t *testing.T) {
	a := &Sessions{Secret: []byte(strings.Repeat("a", 32))}
	b := &Sessions{Secret: []byte(strings.Repeat("b", 32))}
	tok, _ := a.Issue("joao")
	if _, _, err := b.Verify(tok); err == nil {
		t.Fatal("token from another secret accepted")
	}
	if sub, _, err := a.Verify(tok); err != nil || sub != "joao" {
		t.Fatalf("verify: %q %v", sub, err)
	}
	for _, bad := range []string{"", ".", "abc", "abc.def"} {
		if _, _, err := a.Verify(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
