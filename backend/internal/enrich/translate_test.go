package enrich

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/domain"
)

// memItem is an item of fakeTransRepo.
type memItem struct {
	title, summary string
	titlePT        string
	source         string
	attempts       int
}

// fakeTransRepo applies the same rules as postgres.AIRepo in memory.
type fakeTransRepo struct {
	mu        sync.Mutex
	items     map[int64]*memItem
	order     []int64
	lastLimit int
	// pendingHook runs inside PendingTranslations (to start a parallel sweep).
	pendingHook func()
	// saveRace marks the item as translated by someone else just before save.
	saveRace map[int64]bool
}

func newFakeTransRepo(items ...memItem) *fakeTransRepo {
	r := &fakeTransRepo{items: map[int64]*memItem{}, saveRace: map[int64]bool{}}
	for i := range items {
		id := int64(i + 1)
		it := items[i]
		r.items[id] = &it
		r.order = append(r.order, id)
	}
	return r
}

func (r *fakeTransRepo) PendingTranslations(_ context.Context, maxAttempts, limit int) ([]postgres.TranslateRow, error) {
	if r.pendingHook != nil {
		r.pendingHook()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastLimit = limit
	var out []postgres.TranslateRow
	for _, id := range r.order {
		it := r.items[id]
		if it.titlePT == "" && it.attempts < maxAttempts && len(out) < limit {
			out = append(out, postgres.TranslateRow{ID: id, Title: it.title, Summary: it.summary, Attempts: it.attempts})
		}
	}
	return out, nil
}

func (r *fakeTransRepo) SaveTranslation(_ context.Context, id int64, t domain.Translation) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it := r.items[id]
	if r.saveRace[id] {
		it.titlePT, it.source = "by someone else", domain.PTSourceLLM
	}
	if it.titlePT != "" {
		return false, nil
	}
	it.titlePT, it.source = t.TitlePT, t.Source()
	return true, nil
}

func (r *fakeTransRepo) BumpTranslateAttempts(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if it := r.items[id]; it.titlePT == "" {
		it.attempts++
	}
	return nil
}

func (r *fakeTransRepo) TranslationStatus(_ context.Context, maxAttempts int) (domain.TranslationStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var s domain.TranslationStatus
	for _, it := range r.items {
		switch {
		case it.titlePT == "" && it.attempts < maxAttempts:
			s.Remaining++
		case it.titlePT == "":
			s.Failed++
		case it.source == domain.PTSourceCopy:
			s.Copied++
		default:
			s.Translated++
		}
	}
	return s, nil
}

// fakeTransModel answers by title: "bad:" titles give invalid output, "down:"
// titles an outage; Portuguese titles are copied without counting a call.
type fakeTransModel struct {
	mu    sync.Mutex
	calls []string
}

func (m *fakeTransModel) Translate(_ context.Context, title, summary string) (domain.Translation, error) {
	if domain.LooksPortuguese(title, summary) {
		return domain.Translation{TitlePT: title, SummaryPT: summary, Copied: true}, nil
	}
	m.mu.Lock()
	m.calls = append(m.calls, title)
	m.mu.Unlock()
	switch {
	case len(title) > 4 && title[:4] == "bad:":
		return domain.Translation{}, fmt.Errorf("translate: %w: no JSON", app.ErrInvalidOutput)
	case len(title) > 5 && title[:5] == "down:":
		return domain.Translation{}, errors.New("groq: 503 service unavailable")
	}
	return domain.Translation{TitlePT: "PT " + title, SummaryPT: "PT " + summary}, nil
}

func (m *fakeTransModel) called() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

func TestSweepTranslatesCopiesAndNeverRetranslates(t *testing.T) {
	repo := newFakeTransRepo(
		memItem{title: "New model released", summary: "A lab released a model."},
		memItem{title: "Já traduzido", titlePT: "Já traduzido", source: domain.PTSourceLLM},
		memItem{title: "Lançamento do novo modelo da OpenAI", summary: "A empresa anunciou uma nova versão."},
	)
	model := &fakeTransModel{}
	tr := &Translator{Repo: repo, Model: model}
	res, err := tr.Sweep(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := (SweepResult{Translated: 1, Copied: 1}); res != want {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
	if got := model.called(); len(got) != 1 || got[0] != "New model released" {
		t.Fatalf("model calls = %v (the item with title_pt and the Portuguese one must not be sent)", got)
	}
	if got := repo.items[2].titlePT; got != "Já traduzido" {
		t.Fatalf("existing title_pt changed: %q", got)
	}
	// A second sweep has nothing to do and calls nothing.
	res, err = tr.Sweep(context.Background(), 0)
	if err != nil || res != (SweepResult{}) || len(model.called()) != 1 {
		t.Fatalf("second sweep: %+v %v calls=%v", res, err, model.called())
	}
	st, err := tr.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := (domain.TranslationStatus{Translated: 2, Copied: 1}); st != want {
		t.Fatalf("status = %+v, want %+v", st, want)
	}
	if tr.Stats.Translated.Load() != 1 || tr.Stats.Copied.Load() != 1 {
		t.Fatalf("stats: %d %d", tr.Stats.Translated.Load(), tr.Stats.Copied.Load())
	}
}

func TestSweepSkipsItemTranslatedConcurrently(t *testing.T) {
	repo := newFakeTransRepo(memItem{title: "Raced item"})
	repo.saveRace[1] = true
	res, err := (&Translator{Repo: repo, Model: &fakeTransModel{}}).Sweep(context.Background(), 0)
	if err != nil || res != (SweepResult{Skipped: 1}) {
		t.Fatalf("result = %+v %v", res, err)
	}
	if repo.items[1].titlePT != "by someone else" {
		t.Fatal("the concurrent translation was overwritten")
	}
}

func TestSweepSpendsAttemptsOnInvalidOutputAndGivesUpAtThree(t *testing.T) {
	repo := newFakeTransRepo(memItem{title: "bad: broken item"}, memItem{title: "Good item"})
	model := &fakeTransModel{}
	tr := &Translator{Repo: repo, Model: model}
	for i := 1; i <= 3; i++ {
		res, err := tr.Sweep(context.Background(), 0)
		if err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
		if res.Failed != 1 {
			t.Fatalf("sweep %d: %+v", i, res)
		}
		if got := repo.items[1].attempts; got != i {
			t.Fatalf("after sweep %d attempts = %d", i, got)
		}
	}
	// The good item was translated in the first sweep despite the bad one.
	if repo.items[2].titlePT != "PT Good item" {
		t.Fatalf("good item: %q", repo.items[2].titlePT)
	}
	// Given up: no more model calls for it.
	before := len(model.called())
	if res, err := tr.Sweep(context.Background(), 0); err != nil || res != (SweepResult{}) {
		t.Fatalf("after giving up: %+v %v", res, err)
	}
	if len(model.called()) != before {
		t.Fatal("an item past MaxAttempts was sent to the model again")
	}
	st, _ := tr.Status(context.Background())
	if want := (domain.TranslationStatus{Translated: 1, Failed: 1}); st != want {
		t.Fatalf("status = %+v, want %+v", st, want)
	}
}

func TestSweepStopsOnOutageWithoutSpendingAttempts(t *testing.T) {
	repo := newFakeTransRepo(memItem{title: "First"}, memItem{title: "down: quota spent"}, memItem{title: "Third"})
	model := &fakeTransModel{}
	res, err := (&Translator{Repo: repo, Model: model}).Sweep(context.Background(), 0)
	if err == nil {
		t.Fatal("an outage must stop the sweep with an error")
	}
	if res != (SweepResult{Translated: 1}) {
		t.Fatalf("result = %+v", res)
	}
	if repo.items[2].attempts != 0 {
		t.Fatalf("an outage spent an attempt: %d", repo.items[2].attempts)
	}
	if got := model.called(); len(got) != 2 {
		t.Fatalf("the sweep kept calling the model after the outage: %v", got)
	}
}

func TestSweepLimit(t *testing.T) {
	var items []memItem
	for i := range 700 {
		items = append(items, memItem{title: fmt.Sprintf("Item %d", i)})
	}
	cases := []struct {
		name         string
		batch, limit int
		want         int
	}{
		{"default batch", 0, 0, DefaultTranslateBatch},
		{"configured batch", 7, 0, 7},
		{"request limit wins", 7, 3, 3},
		{"request limit clamped", 0, 10_000, MaxTranslateBatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := newFakeTransRepo(items...)
			model := &fakeTransModel{}
			res, err := (&Translator{Repo: repo, Model: model, Batch: c.batch}).Sweep(context.Background(), c.limit)
			if err != nil {
				t.Fatal(err)
			}
			if repo.lastLimit != c.want || res.Translated != c.want || len(model.called()) != c.want {
				t.Fatalf("limit=%d translated=%d calls=%d, want %d", repo.lastLimit, res.Translated, len(model.called()), c.want)
			}
		})
	}
}

func TestSweepIsOneAtATime(t *testing.T) {
	repo := newFakeTransRepo(memItem{title: "Only"})
	tr := &Translator{Repo: repo, Model: &fakeTransModel{}}
	var inner error
	repo.pendingHook = func() {
		repo.pendingHook = nil
		_, inner = tr.Sweep(context.Background(), 0)
	}
	if _, err := tr.Sweep(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(inner, ErrSweepBusy) {
		t.Fatalf("a parallel sweep should be refused, got %v", inner)
	}
}

func TestSweepStopsOnCancel(t *testing.T) {
	repo := newFakeTransRepo(memItem{title: "A"}, memItem{title: "B"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	model := &fakeTransModel{}
	if _, err := (&Translator{Repo: repo, Model: model}).Sweep(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(model.called()) != 0 || repo.items[1].attempts != 0 {
		t.Fatal("a cancelled sweep called the model or spent attempts")
	}
}

func TestRunSweepsAtStartAndOnTick(t *testing.T) {
	repo := newFakeTransRepo(memItem{title: "Start"})
	tr := &Translator{Repo: repo, Model: &fakeTransModel{}, BackfillEvery: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx, nil) }()
	waitFor(t, func() bool { return tr.Stats.Translated.Load() == 1 })
	repo.mu.Lock()
	repo.items[2] = &memItem{title: "Later"}
	repo.order = append(repo.order, 2)
	repo.mu.Unlock()
	waitFor(t, func() bool { return tr.Stats.Translated.Load() == 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
