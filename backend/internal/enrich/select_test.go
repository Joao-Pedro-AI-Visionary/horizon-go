package enrich

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
	"github.com/jpdev/horizon-go/internal/retry"
)

// scriptedLLM answers every call with the same text and counts calls (test-only).
type scriptedLLM struct {
	answer string
	calls  int
}

func (s *scriptedLLM) Model() string { return "scripted" }
func (s *scriptedLLM) Chat(context.Context, ports.ChatRequest) (string, error) {
	s.calls++
	return s.answer, nil
}

func analyzerWith(llm ports.LLM) *app.Analyzer {
	return &app.Analyzer{LLM: llm, Retry: retry.Policy{Attempts: 1}}
}

func TestSelectMatchesThresholds(t *testing.T) {
	target := postgres.DedupRow{ID: 1, Title: "target", Score: 5}
	ns := []postgres.DedupRow{
		{ID: 10, Similarity: 0.95, Score: 7},
		{ID: 11, Similarity: 0.88, Score: 4},
		{ID: 12, Similarity: 0.85, Score: 8}, // ambiguous, LLM says same (index 0 after sort)
		{ID: 13, Similarity: 0.80, Score: 1}, // ambiguous, LLM says different
		{ID: 14, Similarity: 0.79, Score: 9},
	}
	// Sorted by score: [0]=12 (8), [1]=target (5), [2]=13 (1).
	llm := &scriptedLLM{answer: `{"duplicates": [[0, 1]]}`}
	d, err := SelectMatches(context.Background(), analyzerWith(llm), domain.DefaultThresholds, target, ns)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.Direct, []int64{10, 11}) {
		t.Errorf("direct = %v", d.Direct)
	}
	if !reflect.DeepEqual(d.Ambiguous, []int64{12, 13}) {
		t.Errorf("ambiguous = %v", d.Ambiguous)
	}
	if !reflect.DeepEqual(d.Confirmed, []int64{12}) {
		t.Errorf("confirmed = %v", d.Confirmed)
	}
	if !reflect.DeepEqual(d.Matched(), []int64{10, 11, 12}) {
		t.Errorf("matched = %v", d.Matched())
	}
	if llm.calls != 1 {
		t.Errorf("llm calls = %d, want 1 (one batch for the band)", llm.calls)
	}
}

func TestSelectMatchesSkipsLLMOutsideBand(t *testing.T) {
	llm := &scriptedLLM{answer: `{"duplicates": []}`}
	ns := []postgres.DedupRow{{ID: 2, Similarity: 0.91}, {ID: 3, Similarity: 0.3}}
	d, err := SelectMatches(context.Background(), analyzerWith(llm), domain.DefaultThresholds, postgres.DedupRow{ID: 1}, ns)
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls != 0 {
		t.Errorf("LLM must not be called without ambiguous neighbours, calls = %d", llm.calls)
	}
	if !reflect.DeepEqual(d.Matched(), []int64{2}) {
		t.Errorf("matched = %v", d.Matched())
	}
}

func TestSelectMatchesPropagatesLLMError(t *testing.T) {
	llm := &scriptedLLM{answer: "not json"}
	ns := []postgres.DedupRow{{ID: 2, Similarity: 0.82, PublishedAt: time.Now()}}
	if _, err := SelectMatches(context.Background(), analyzerWith(llm), domain.DefaultThresholds, postgres.DedupRow{ID: 1}, ns); err == nil {
		t.Fatal("an unreadable LLM answer must fail the item (retried later), not silently keep it separate")
	}
}
