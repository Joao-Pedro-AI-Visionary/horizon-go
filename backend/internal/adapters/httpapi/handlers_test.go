package httpapi

// Handler tests with in-memory fakes: validation, status mapping, CORS and
// the single-collection guard. The same endpoints run against the real
// Postgres and NATS in api_integration_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/domain"
)

type fakeStore struct {
	lastQuery domain.FeedQuery
	lastDays  int
	settings  domain.Settings
	feedErr   error
	pingErr   error
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }
func (f *fakeStore) Feed(_ context.Context, q domain.FeedQuery) (domain.FeedPage, error) {
	f.lastQuery = q
	return domain.FeedPage{Items: []domain.FeedItem{}, Page: q.Page, PageSize: q.PageSize}, f.feedErr
}
func (f *fakeStore) FeedTimeline(_ context.Context, q domain.FeedQuery, days int) (domain.FeedTimeline, error) {
	f.lastQuery, f.lastDays = q, days
	return domain.FeedTimeline{Days: days, Buckets: []domain.TimelineBucket{}}, f.feedErr
}
func (f *fakeStore) Item(_ context.Context, id int64) (domain.ItemDetail, error) {
	if id == 1 {
		return domain.ItemDetail{ID: 1, Title: "one"}, nil
	}
	return domain.ItemDetail{}, postgres.ErrNotFound
}
func (f *fakeStore) ListDigests(context.Context, int) ([]domain.Digest, error) {
	return []domain.Digest{}, nil
}
func (f *fakeStore) GetDigest(_ context.Context, date, _ string) (domain.Digest, error) {
	if date == "2026-11-02" {
		return domain.Digest{Date: date, Markdown: "# md", HTML: "<p>h</p>"}, nil
	}
	return domain.Digest{}, postgres.ErrNotFound
}
func (f *fakeStore) ListSourceStatus(context.Context) ([]postgres.SourceStatus, error) {
	return nil, nil
}
func (f *fakeStore) GetSettings(context.Context) (domain.Settings, error) { return f.settings, nil }
func (f *fakeStore) PutSettings(_ context.Context, s domain.Settings) (domain.Settings, error) {
	f.settings = s
	return s, nil
}
func (f *fakeStore) Metrics(_ context.Context, days int) (domain.Metrics, error) {
	return domain.Metrics{Days: days}, nil
}

type fakeSources struct{ created []domain.SourceConfig }

func (f *fakeSources) Create(_ context.Context, s domain.SourceConfig) (domain.SourceConfig, error) {
	if s.Name == "dup" {
		return s, postgres.ErrConflict
	}
	s.ID = int64(len(f.created) + 1)
	f.created = append(f.created, s)
	return s, nil
}
func (f *fakeSources) Get(_ context.Context, id int64) (domain.SourceConfig, error) {
	return domain.SourceConfig{}, postgres.ErrNotFound
}
func (f *fakeSources) List(context.Context, bool) ([]domain.SourceConfig, error) { return nil, nil }
func (f *fakeSources) Update(_ context.Context, s domain.SourceConfig) (domain.SourceConfig, error) {
	return s, nil
}
func (f *fakeSources) Delete(context.Context, int64) error { return postgres.ErrNotFound }

type blockingCollector struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingCollector) Collect(context.Context, time.Time) (app.Report, error) {
	close(b.started)
	<-b.release
	return app.Report{RunID: "r1"}, nil
}
func (b *blockingCollector) CollectSource(context.Context, int64, time.Time) (app.Report, error) {
	return app.Report{}, postgres.ErrNotFound
}

type fakeDigester struct{}

func (fakeDigester) Generate(_ context.Context, date string) (domain.Digest, error) {
	if date == "bad" {
		return domain.Digest{}, domain.Invalidf("date must be YYYY-MM-DD")
	}
	return domain.Digest{}, errors.New("db exploded: secret detail")
}

func newTestServer(t *testing.T) (*Server, *fakeStore, *httptest.Server) {
	t.Helper()
	fs := &fakeStore{settings: domain.Settings{Timezone: "America/Sao_Paulo"}}
	s := &Server{
		Store: fs, Sources: &fakeSources{}, Digests: fakeDigester{},
		Collector:      &blockingCollector{started: make(chan struct{}), release: make(chan struct{})},
		ValidateCron:   func(c string) error { return map[bool]error{true: domain.Invalidf("bad cron")}[c == "nope"] },
		AllowedOrigins: []string{"http://localhost:5173"},
		Checks:         []Check{{Name: "postgres", Fn: fs.Ping}},
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, fs, srv
}

func do(t *testing.T, method, u, body string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, u, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestParseFeedQuery(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	q, err := parseFeedQuery(url.Values{
		"source": {"Reddit"}, "source_id": {"3"}, "tag": {"Go"}, "min_score": {"6.5"}, "q": {"release"},
		"period": {"7d"}, "sort": {"recent"}, "page": {"2"}, "page_size": {"50"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if q.SourceKind != "reddit" || q.SourceID != 3 || q.Tag != "Go" || *q.MinScore != 6.5 || q.Q != "release" ||
		!q.From.Equal(now.Add(-7*24*time.Hour)) || q.Sort != "recent" || q.Page != 2 || q.PageSize != 50 {
		t.Fatalf("query: %+v", q)
	}
	q, err = parseFeedQuery(url.Values{}, now)
	if err != nil || q.Page != 1 || q.PageSize != 20 || q.Sort != "score" || !q.From.IsZero() {
		t.Fatalf("defaults: %+v %v", q, err)
	}
	q, err = parseFeedQuery(url.Values{"from": {"2026-11-01"}, "to": {"2026-11-02T10:00:00Z"}}, now)
	if err != nil || q.From.Day() != 1 || q.To.Hour() != 10 {
		t.Fatalf("from/to: %+v %v", q, err)
	}
	for _, bad := range []url.Values{
		{"source": {"twitter"}}, {"source_id": {"x"}}, {"min_score": {"11"}}, {"min_score": {"abc"}},
		{"period": {"1y"}}, {"sort": {"title"}}, {"page": {"0"}}, {"page_size": {"101"}},
		{"from": {"yesterday"}}, {"from": {"2026-11-02"}, "to": {"2026-11-01"}}, {"q": {strings.Repeat("x", 201)}},
	} {
		if _, err := parseFeedQuery(bad, now); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%v: want ErrInvalid, got %v", bad, err)
		}
	}
}

func TestStatusMapping(t *testing.T) {
	_, fs, srv := newTestServer(t)
	cases := []struct {
		method, path, body string
		status             int
		contains           string
	}{
		{"GET", "/api/feed?min_score=99", "", 400, "min_score"},
		{"GET", "/api/feed?tag=go", "", 200, `"items":[]`},
		{"GET", "/api/items/1", "", 200, `"title":"one"`},
		{"GET", "/api/items/2", "", 404, "not found"},
		{"GET", "/api/items/abc", "", 400, "id must be"},
		{"GET", "/api/digests/2026-11-02?format=md", "", 200, "# md"},
		{"GET", "/api/digests/2026-11-02?format=pdf", "", 400, "format"},
		{"GET", "/api/digests/02-11-2026", "", 400, "YYYY-MM-DD"},
		{"GET", "/api/digests/2026-01-01", "", 404, "not found"},
		{"POST", "/api/digests/generate", `{"date":"bad"}`, 400, "YYYY-MM-DD"},
		{"POST", "/api/digests/generate", `{"date":"2026-11-02","x":1}`, 400, "unknown field"},
		{"POST", "/api/digests/generate", `{}`, 500, "internal error"},
		{"POST", "/api/sources", `{"kind":"rss"}`, 400, "required"},
		{"POST", "/api/sources", `{"kind":"twitter","name":"x"}`, 400, "kind must be"},
		{"POST", "/api/sources", `{"kind":"rss","name":"x","config":[1]}`, 400, "JSON object"},
		{"POST", "/api/sources", `{"kind":"rss","name":"dup"}`, 409, "already exists"},
		{"POST", "/api/sources", `{"kind":"rss","name":"ok","config":{"url":"https://go.dev/blog/feed.atom"}}`, 201, `"enabled":true`},
		{"PATCH", "/api/sources/9", `{"enabled":false}`, 404, "not found"},
		{"DELETE", "/api/sources/9", "", 404, "not found"},
		{"POST", "/api/sources/9/fetch", "", 404, "not found"},
		{"PUT", "/api/settings", `{"score_threshold":6}`, 400, "required"},
		{"PUT", "/api/settings", `{"score_threshold":11,"digest_max_items":5,"digest_per_source":1,"digest_cron":"0 7 * * *"}`, 400, "score_threshold"},
		{"PUT", "/api/settings", `{"score_threshold":6,"digest_max_items":5,"digest_per_source":1,"digest_cron":"nope"}`, 400, "bad cron"},
		{"PUT", "/api/settings", `{"score_threshold":6,"digest_max_items":5,"digest_per_source":1,"digest_cron":"0 7 * * *","timezone":"Mars/Base"}`, 400, "timezone"},
		{"PUT", "/api/settings", `{"score_threshold":7,"digest_max_items":5,"digest_per_source":1,"digest_cron":"0 8 * * *"}`, 200, `"timezone":"America/Sao_Paulo"`},
		{"GET", "/api/feed/timeline?days=0", "", 400, "days"},
		{"GET", "/api/feed/timeline?days=91", "", 400, "days"},
		{"GET", "/api/feed/timeline?min_score=11", "", 400, "min_score"},
		{"GET", "/api/feed/timeline", "", 200, `"days":30`},
		{"GET", "/api/metrics?days=0", "", 400, "days"},
		{"GET", "/api/metrics?days=7", "", 200, `"days":7`},
		{"GET", "/api/nope", "", 404, "not found"},
		{"DELETE", "/api/feed", "", 405, "method not allowed"},
		{"GET", "/healthz", "", 200, `"postgres":"ok"`},
	}
	for _, c := range cases {
		resp, body := do(t, c.method, srv.URL+c.path, c.body)
		if resp.StatusCode != c.status || !strings.Contains(body, c.contains) {
			t.Errorf("%s %s: %d %s (want %d containing %q)", c.method, c.path, resp.StatusCode, body, c.status, c.contains)
		}
		if strings.Contains(body, "secret detail") {
			t.Errorf("%s %s leaked the internal error", c.method, c.path)
		}
	}
	if fs.settings.ScoreThreshold != 7 || fs.settings.DigestCron != "0 8 * * *" {
		t.Errorf("settings not saved: %+v", fs.settings)
	}
	fs.pingErr = errors.New("down")
	if resp, body := do(t, "GET", srv.URL+"/healthz", ""); resp.StatusCode != 503 || !strings.Contains(body, `"status":"degraded"`) {
		t.Errorf("healthz degraded: %d %s", resp.StatusCode, body)
	}
}

func TestTimelineIgnoresTimeFilters(t *testing.T) {
	_, fs, srv := newTestServer(t)
	resp, body := do(t, "GET", srv.URL+"/api/feed/timeline?days=14&source_id=2&tag=go&min_score=6&q=go&period=24h&from=2026-11-01&to=2026-11-02", "")
	if resp.StatusCode != 200 || !strings.Contains(body, `"days":14`) {
		t.Fatalf("timeline: %d %s", resp.StatusCode, body)
	}
	q := fs.lastQuery
	if fs.lastDays != 14 || q.SourceID != 2 || q.Tag != "go" || *q.MinScore != 6 || q.Q != "go" || !q.From.IsZero() || !q.To.IsZero() {
		t.Fatalf("query passed to the store: days=%d %+v", fs.lastDays, q)
	}
}

func TestCORS(t *testing.T) {
	_, _, srv := newTestServer(t)
	resp, _ := do(t, "OPTIONS", srv.URL+"/api/sources", "", "Origin", "http://localhost:5173", "Access-Control-Request-Method", "PATCH")
	if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" ||
		!strings.Contains(resp.Header.Get("Access-Control-Allow-Methods"), "PATCH") {
		t.Errorf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = do(t, "OPTIONS", srv.URL+"/api/sources", "", "Origin", "https://evil.example", "Access-Control-Request-Method", "DELETE")
	if resp.StatusCode != 403 || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("foreign preflight: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = do(t, "GET", srv.URL+"/api/feed", "", "Origin", "http://localhost:5173")
	if resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Errorf("simple request: %v", resp.Header)
	}
}

func TestOneCollectionAtATime(t *testing.T) {
	s, _, srv := newTestServer(t)
	col := s.Collector.(*blockingCollector)
	first := make(chan int)
	go func() {
		resp, body := do(t, "POST", srv.URL+"/api/pipeline/run", "")
		var rep reportDTO
		_ = json.Unmarshal([]byte(body), &rep)
		if rep.RunID != "r1" {
			t.Errorf("first run body: %s", body)
		}
		first <- resp.StatusCode
	}()
	<-col.started
	if resp, body := do(t, "POST", srv.URL+"/api/pipeline/run", ""); resp.StatusCode != 409 {
		t.Errorf("concurrent run: %d %s", resp.StatusCode, body)
	}
	close(col.release)
	if code := <-first; code != 200 {
		t.Errorf("first run: %d", code)
	}
}

type countingCollector struct{ n int }

func (c *countingCollector) Collect(context.Context, time.Time) (app.Report, error) {
	c.n++
	return app.Report{RunID: "r"}, nil
}
func (c *countingCollector) CollectSource(context.Context, int64, time.Time) (app.Report, error) {
	c.n++
	return app.Report{RunID: "r"}, nil
}

func TestCollectCooldown(t *testing.T) {
	s, _, srv := newTestServer(t)
	col := &countingCollector{}
	s.Collector, s.CollectCooldown = col, time.Hour
	if resp, body := do(t, "POST", srv.URL+"/api/pipeline/run", ""); resp.StatusCode != 200 {
		t.Fatalf("first run: %d %s", resp.StatusCode, body)
	}
	resp, _ := do(t, "POST", srv.URL+"/api/pipeline/run", "")
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Errorf("second run: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ := do(t, "POST", srv.URL+"/api/sources/1/fetch", ""); resp.StatusCode != 429 {
		t.Errorf("source fetch inside the cooldown: %d", resp.StatusCode)
	}
	if col.n != 1 {
		t.Errorf("collector ran %d times", col.n)
	}
}
