package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/digest"
	"github.com/jpdev/horizon-go/internal/domain"
)

// TestAlertsAgainstPostgres checks the alert queries on the real schema.
func TestAlertsAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool := testDB(t, ctx)
	if _, err := pool.Exec(ctx, `TRUNCATE alert_events`); err != nil {
		t.Fatal(err)
	}
	read := postgres.ReadRepo{DB: pool}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO sources (id, kind, name, enabled) VALUES
 (1, 'rss', 'quebrada', true), (2, 'rss', 'recuperada', true), (3, 'rss', 'desligada', false), (4, 'rss', 'antiga', true)`)
	now := time.Now()
	run := func(src int, status, msg string, ago time.Duration) {
		at := now.Add(-ago)
		exec(`INSERT INTO fetch_runs (run_id, source_id, status, error, started_at, finished_at) VALUES ('r', $1, $2, $3, $4, $4)`, src, status, msg, at)
	}
	run(1, "error", "status 502 velho", 3*time.Hour)
	run(1, "error", "status 429 novo", time.Hour)
	run(2, "error", "status 500", 2*time.Hour)
	run(2, "ok", "", time.Hour)              // recovered: no alert
	run(3, "error", "status 404", time.Hour) // disabled: no alert
	run(4, "error", "fora da janela", 72*time.Hour)
	exec(`INSERT INTO items (source_id, external_id, url, url_normalized, url_hash, title, status, last_error, updated_at) VALUES
 (2, 'a', 'https://e/a', 'e/a', 'ha', 'A', 'raw', 'groq 429', now() - interval '30 minutes'),
 (2, 'b', 'https://e/b', 'e/b', 'hb', 'B', 'failed', 'groq 400', now() - interval '20 minutes'),
 (2, 'c', 'https://e/c', 'e/c', 'hc', 'C', 'scored', '', now())`)

	svc := &digest.Service{Store: failingDigest{}, Failures: read}
	if _, err := svc.Generate(ctx, ""); err == nil {
		t.Fatal("want digest failure")
	}

	list, err := read.Alerts(ctx, now.Add(-48*time.Hour), now, 14*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]domain.Alert{}
	for _, a := range list {
		got[a.Kind+":"+a.Name] = a
	}
	if len(list) != 3 {
		t.Fatalf("want 3 alerts, got %+v", list)
	}
	if a := got["source:quebrada"]; a.Message != "status 429 novo" || a.SourceID == nil || *a.SourceID != 1 {
		t.Errorf("source alert %+v", a)
	}
	if a := got["scoring:recuperada"]; a.Message != "2 itens sem nota: groq 400" {
		t.Errorf("scoring alert %+v", a)
	}
	if a := got["digest:Digest"]; a.Message == "" {
		t.Errorf("digest alert missing: %+v", list)
	}

	// A digest saved afterwards closes the digest alert; an old last success
	// raises the collection alert.
	exec(`INSERT INTO digests (digest_date, title, markdown, updated_at) VALUES (CURRENT_DATE, 't', 'm', now() + interval '1 second')`)
	exec(`DELETE FROM fetch_runs WHERE status = 'ok'`)
	run(2, "ok", "", 20*time.Hour)
	list, err = read.Alerts(ctx, now.Add(-48*time.Hour), now.Add(2*time.Second), 14*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, a := range list {
		kinds[a.Kind]++
	}
	if kinds[domain.AlertDigest] != 0 || kinds[domain.AlertCollection] != 1 || kinds[domain.AlertSource] != 2 {
		t.Fatalf("after digest: %+v", list)
	}
}

type failingDigest struct{}

func (failingDigest) GetSettings(context.Context) (domain.Settings, error) {
	return domain.Settings{Timezone: "America/Sao_Paulo", DigestMaxItems: 5}, nil
}
func (failingDigest) DigestCandidates(context.Context, time.Time, time.Time) ([]domain.FeedItem, error) {
	return nil, context.DeadlineExceeded
}
func (failingDigest) SaveDigest(context.Context, domain.Digest) (domain.Digest, error) {
	return domain.Digest{}, nil
}
