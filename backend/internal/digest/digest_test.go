package digest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
)

func at(h int) *time.Time {
	t := time.Date(2026, 11, 2, h, 0, 0, 0, time.UTC)
	return &t
}

func fi(id, src int64, score float64, h int) domain.FeedItem {
	return domain.FeedItem{ItemID: id, SourceID: src, SourceKind: "rss", SourceName: "S", Title: "t", URL: "https://x.dev/a", Score: score, PublishedAt: at(h)}
}

func ids(items []domain.FeedItem) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ItemID
	}
	return out
}

func eq(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSelectThresholdOrderQuotaCap(t *testing.T) {
	in := []domain.FeedItem{
		fi(1, 1, 9, 1), fi(2, 1, 8, 2), fi(3, 1, 7, 3), // source 1
		fi(4, 2, 8, 5), fi(5, 2, 5.9, 6), // source 2; 5 is under the threshold
		fi(6, 3, 6, 7), fi(7, 3, 8, 1), // 7 ties 2 and 4 at 8: most recent first (4, 2, 7)
	}
	cases := []struct {
		name string
		r    Rules
		want []int64
	}{
		{"threshold only", Rules{Threshold: 6}, []int64{1, 4, 2, 7, 3, 6}},
		{"quota per source", Rules{Threshold: 6, PerSource: 1}, []int64{1, 4, 7}},
		{"quota and cap", Rules{Threshold: 6, PerSource: 2, MaxItems: 4}, []int64{1, 4, 2, 7}},
		{"inclusive threshold", Rules{Threshold: 9}, []int64{1}},
		{"nothing passes", Rules{Threshold: 9.5}, []int64{}},
	}
	for _, c := range cases {
		if got := ids(Select(in, c.r)); !eq(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	if in[0].ItemID != 1 || in[4].ItemID != 5 {
		t.Fatal("input was modified")
	}
}

func TestEscapeMarkdownAndSafeURL(t *testing.T) {
	got := EscapeMarkdown("# Title *bold* [x](javascript:alert(1)) <script>\n- item")
	for _, bad := range []string{"<script>", "[x](", "\n"} {
		if strings.Contains(got, bad) {
			t.Errorf("escaped text still contains %q: %s", bad, got)
		}
	}
	if !strings.HasPrefix(got, `\#`) || !strings.Contains(got, `\*bold\*`) {
		t.Errorf("unexpected escape: %s", got)
	}
	for raw, want := range map[string]string{
		"https://go.dev/blog/x?a=1": "https://go.dev/blog/x?a=1",
		"https://x.dev/a (b)":       "https://x.dev/a%20%28b%29",
		"javascript:alert(1)":       "",
		"ftp://x.dev/a":             "",
		"https://x.dev/\x00a":       "",
		"/relative/path":            "",
		"":                          "",
	} {
		if got := SafeURL(raw); got != want {
			t.Errorf("SafeURL(%q) = %q want %q", raw, got, want)
		}
	}
}

func TestRenderMarkdownAndHTML(t *testing.T) {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	it := domain.FeedItem{
		ItemID: 7, SourceID: 1, SourceKind: "hackernews", SourceName: "Hacker News",
		Title: "Go 1.27 <b>released</b>", URL: "https://go.dev/blog/go1.27", Summary: "Nova versão do Go.",
		ScoreReason: "Release relevante.", Score: 8.5, Tags: []string{"go", "release"}, PublishedAt: at(15), DuplicateCount: 2,
	}
	bad := it
	bad.ItemID, bad.Title, bad.URL, bad.Score = 8, "Evil", "javascript:alert(1)", 7
	v := View{Date: "2026-11-02", Threshold: 6, Considered: 10, Items: []domain.FeedItem{it, bad}, Loc: loc}

	md := Markdown(v)
	for _, want := range []string{
		"# Radar by JpDev — 2026-11-02",
		"De 10 itens analisados nas últimas 24 horas, 2 foram selecionados \\(nota mínima 6.0\\).",
		"1. [Go 1.27 &lt;b&gt;released&lt;/b&gt;](#item-1) ⭐ 8.5/10",
		"## 1. [Go 1.27 &lt;b&gt;released&lt;/b&gt;](https://go.dev/blog/go1.27)",
		"**Nota 8.5/10** · hackernews · Hacker News · 02/11 12:00 · +2 fontes com a mesma notícia", // 15:00 UTC = 12:00 BRT
		"*Por que importa:* Release relevante.",
		"Tags: go · release",
		"## 2. Evil\n", // unsafe link dropped
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "javascript:") {
		t.Error("markdown kept a javascript: link")
	}

	page, err := HTML(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<html lang="pt-BR">`, "<title>Radar by JpDev — 2026-11-02</title>",
		`<a href="https://go.dev/blog/go1.27" rel="noopener noreferrer" target="_blank">Go 1.27 &lt;b&gt;released&lt;/b&gt;</a>`,
		`<span class="score">8.5</span>`, `<span class="tag">release</span>`, "#276b43",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("html missing %q", want)
		}
	}
	if strings.Contains(page, "<b>released") || strings.Contains(page, "javascript:") {
		t.Error("html not escaped")
	}

	empty := Markdown(View{Date: "2026-11-02", Threshold: 6, Considered: 3})
	if !strings.Contains(empty, "Nenhum item atingiu a nota mínima") || !strings.Contains(empty, "De 3 itens") {
		t.Errorf("empty digest: %s", empty)
	}
}

func TestWindow(t *testing.T) {
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, loc)
	// Cron at 07:00 on the same day: 07:00 yesterday -> now.
	now := time.Date(2026, 11, 2, 7, 0, 0, 0, loc)
	from, to := Window(day, now, loc)
	if !to.Equal(now) || !from.Equal(now.Add(-24*time.Hour)) {
		t.Errorf("today: %v .. %v", from, to)
	}
	// A past day: its whole calendar day.
	from, to = Window(day, now.Add(72*time.Hour), loc)
	if !from.Equal(day) || !to.Equal(day.Add(24*time.Hour)) {
		t.Errorf("past day: %v .. %v", from, to)
	}
}

type fakeStore struct {
	st       domain.Settings
	cands    []domain.FeedItem
	from, to time.Time
	saved    domain.Digest
}

func (f *fakeStore) GetSettings(context.Context) (domain.Settings, error) { return f.st, nil }
func (f *fakeStore) DigestCandidates(_ context.Context, from, to time.Time) ([]domain.FeedItem, error) {
	f.from, f.to = from, to
	return f.cands, nil
}
func (f *fakeStore) SaveDigest(_ context.Context, d domain.Digest) (domain.Digest, error) {
	f.saved = d
	d.ID = 1
	return d, nil
}

func TestServiceGenerate(t *testing.T) {
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Date(2026, 11, 2, 7, 0, 0, 0, loc)
	fs := &fakeStore{
		st:    domain.Settings{ScoreThreshold: 6, DigestMaxItems: 2, DigestPerSource: 1, DigestCron: DefaultCron, Timezone: "America/Sao_Paulo"},
		cands: []domain.FeedItem{fi(1, 1, 9, 1), fi(2, 1, 8, 2), fi(3, 2, 7, 3), fi(4, 3, 6.5, 4), fi(5, 3, 2, 4)},
	}
	svc := &Service{Store: fs, Now: func() time.Time { return now }}
	d, err := svc.Generate(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Date != "2026-11-02" || !eq(d.ItemIDs, []int64{1, 3}) || d.ItemCount != 2 || d.Considered != 5 || d.Language != Language {
		t.Fatalf("digest: %+v", d)
	}
	if !fs.to.Equal(now) || !fs.from.Equal(now.Add(-24*time.Hour)) {
		t.Errorf("window %v..%v", fs.from, fs.to)
	}
	if !strings.Contains(d.Markdown, "## 2.") || strings.Contains(d.Markdown, "## 3.") || d.HTML == "" {
		t.Error("rendered digest does not match the selection")
	}

	if _, err := svc.Generate(context.Background(), "2026-11-05"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("future date: %v", err)
	}
	if _, err := svc.Generate(context.Background(), "02/11/2026"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("bad date: %v", err)
	}
}

func TestSchedulerFollowsSettings(t *testing.T) {
	if err := ValidateCron("0 7 * * *"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCron("every day"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad spec: %v", err)
	}
	fs := &fakeStore{st: domain.Settings{DigestCron: DefaultCron, Timezone: "America/Sao_Paulo"}}
	s := &Scheduler{Service: &Service{Store: fs}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	next := s.Next().In(loc)
	if next.Hour() != 7 || next.Minute() != 0 {
		t.Fatalf("next run %v, want 07:00 BRT", next)
	}
	fs.st.DigestCron = "30 18 * * 1-5"
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	next = s.Next().In(loc)
	if next.Hour() != 18 || next.Minute() != 30 || next.Weekday() == time.Saturday || next.Weekday() == time.Sunday {
		t.Fatalf("rescheduled next run %v", next)
	}
	cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
}
