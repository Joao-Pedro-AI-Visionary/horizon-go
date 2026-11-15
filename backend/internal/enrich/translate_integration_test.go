package enrich

import (
	"context"
	"testing"
	"time"
)

// TestTranslateRequestReachesWorker runs the real NATS path of POST
// /api/pipeline/translate: the API-side TranslationQueue publishes on
// items.translate and the worker-side Translator consumes it and sweeps with
// the requested limit. Repo and model are fakes; the bus is the real one.
func TestTranslateRequestReachesWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bus := testBus(t, ctx)
	cons, err := bus.Consumer(ctx, TranslatorDurable, bus.TranslateSubject(), AckWait)
	if err != nil {
		t.Fatal(err)
	}

	repo := newFakeTransRepo(memItem{title: "One"}, memItem{title: "Two"}, memItem{title: "Three"})
	model := &fakeTransModel{}
	// Long period: only the start sweep (empty here) and the request run.
	tr := &Translator{Repo: repo, Model: model, BackfillEvery: time.Hour}
	queue := TranslationQueue{Repo: repo, Bus: bus}

	// Hold the start sweep back by making the queue empty until the request.
	repo.mu.Lock()
	pending := repo.order
	repo.order = nil
	repo.mu.Unlock()

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- tr.Run(runCtx, cons) }()
	// Wait for the start sweep (default batch) to find nothing.
	waitFor(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return repo.lastLimit == DefaultTranslateBatch
	})

	repo.mu.Lock()
	repo.order = pending
	repo.mu.Unlock()
	if err := queue.RequestTranslation(ctx, 2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tr.Stats.Translated.Load() == 2 })
	st, err := queue.TranslationStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Translated != 2 || st.Remaining != 1 {
		t.Fatalf("status = %+v (limit 2 should leave one item)", st)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
