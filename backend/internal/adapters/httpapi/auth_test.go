package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ingest"
)

func authServer(token string, require bool) http.Handler {
	s := &Server{
		Store:          &fakeStore{settings: domain.Settings{ScoreThreshold: 6, DigestMaxItems: 10, DigestCron: "0 7 * * *", Timezone: "America/Sao_Paulo"}},
		Sources:        &fakeSources{},
		Collector:      &countingCollector{},
		ValidateSource: func(domain.SourceConfig) error { return nil },
		ValidateCron:   func(string) error { return nil },
		Window:         time.Hour,
		AllowedOrigins: []string{"https://web.example"},
		Pusher:         fakePusher{},
		AdminToken:     token,
		RequireAdmin:   require,
	}
	return s.Handler()
}

func doAuth(h http.Handler, method, path, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var writeRoutes = []struct{ method, path, body string }{
	{http.MethodPost, "/api/sources", `{"kind":"rss","name":"x","config":{"url":"https://example.com/feed"}}`},
	{http.MethodPatch, "/api/sources/1", `{"enabled":false}`},
	{http.MethodDelete, "/api/sources/1", ""},
	{http.MethodPost, "/api/sources/1/fetch", ""},
	{http.MethodPut, "/api/settings", `{"score_threshold":6,"digest_max_items":10,"digest_per_source":0,"digest_cron":"0 7 * * *"}`},
	{http.MethodPost, "/api/pipeline/run", ""},
	{http.MethodPost, "/api/digests/generate", ""},
	{http.MethodPost, "/api/auth/check", ""},
	{http.MethodPost, "/api/items/ingest", `[{"source":"X","url":"https://x.com/a/status/1","title":"t"}]`},
}

type fakePusher struct{}

func (fakePusher) Push(_ context.Context, in []ingest.PushItem) (ingest.PushReport, error) {
	if len(in) == 0 {
		return ingest.PushReport{}, domain.Invalidf("empty")
	}
	return ingest.PushReport{Received: len(in), Accepted: len(in)}, nil
}

func TestIngestItems(t *testing.T) {
	h := authServer("tok", true)
	rec := doAuth(h, http.MethodPost, "/api/items/ingest", "Bearer tok", `[{"source":"X","url":"https://x.com/a/status/1","title":"t","text":"x","author":"@a","published_at":"2026-11-12T10:00:00Z","metrics":{"views":10,"likes":2,"reposts":1}}]`)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"accepted":1`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{`[]`, `{"url":"x"}`, `[{"bogus":1}]`} {
		if rec := doAuth(h, http.MethodPost, "/api/items/ingest", "Bearer tok", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d want 400", body, rec.Code)
		}
	}
}

func TestWritesNeedTheAdminToken(t *testing.T) {
	h := authServer("s3cret-token", true)
	for _, rt := range writeRoutes {
		for _, auth := range []string{"", "Bearer wrong", "Basic s3cret-token", "Bearer ", "s3cret-token"} {
			rec := doAuth(h, rt.method, rt.path, auth, rt.body)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s auth=%q: got %d want 401", rt.method, rt.path, auth, rec.Code)
			}
			if rec.Header().Get("WWW-Authenticate") == "" {
				t.Errorf("%s %s: missing WWW-Authenticate", rt.method, rt.path)
			}
		}
		rec := doAuth(h, rt.method, rt.path, "bearer s3cret-token", rt.body)
		if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusServiceUnavailable {
			t.Errorf("%s %s with token: got %d", rt.method, rt.path, rec.Code)
		}
	}
}

func TestAuthCheck(t *testing.T) {
	h := authServer("tok", true)
	if rec := doAuth(h, http.MethodPost, "/api/auth/check", "Bearer tok", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestReadsStayPublic(t *testing.T) {
	h := authServer("tok", true)
	for _, p := range []string{"/api/feed", "/api/sources", "/api/settings", "/api/metrics", "/api/digests", "/healthz"} {
		if rec := doAuth(h, http.MethodGet, p, "", ""); rec.Code == http.StatusUnauthorized || rec.Code == http.StatusServiceUnavailable {
			t.Errorf("GET %s: got %d", p, rec.Code)
		}
	}
}

func TestWritesFailClosedWithoutTokenInProduction(t *testing.T) {
	h := authServer("", true)
	for _, rt := range writeRoutes {
		if rec := doAuth(h, rt.method, rt.path, "Bearer anything", rt.body); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: got %d want 503", rt.method, rt.path, rec.Code)
		}
	}
	if rec := doAuth(h, http.MethodGet, "/api/feed", "", ""); rec.Code != http.StatusOK {
		t.Errorf("GET /api/feed: got %d", rec.Code)
	}
}

func TestWritesOpenInDevWithoutToken(t *testing.T) {
	h := authServer("", false)
	if rec := doAuth(h, http.MethodPost, "/api/auth/check", "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestPreflightAllowsAuthorization(t *testing.T) {
	h := authServer("tok", true)
	req := httptest.NewRequest(http.MethodOptions, "/api/settings", nil)
	req.Header.Set("Origin", "https://web.example")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Access-Control-Allow-Headers"))
	}
}

func TestTokenEqual(t *testing.T) {
	if !tokenEqual("a", "a") || tokenEqual("a", "b") || tokenEqual("", "a") {
		t.Fatal("tokenEqual")
	}
}

func TestAuthStatus(t *testing.T) {
	for _, c := range []struct {
		token   string
		require bool
		want    string
	}{{"", false, "open"}, {"tok", true, "token"}, {"tok", false, "token"}, {"", true, "disabled"}} {
		rec := doAuth(authServer(c.token, c.require), http.MethodGet, "/api/auth", "", "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"mode":"`+c.want+`"`) {
			t.Errorf("%+v: got %d %s", c, rec.Code, rec.Body.String())
		}
	}
}
