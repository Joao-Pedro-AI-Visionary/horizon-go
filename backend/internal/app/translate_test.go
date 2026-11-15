package app

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTranslateCopiesPortugueseWithoutModel(t *testing.T) {
	llm := &fakeLLM{answers: []string{`{"title_pt": "x", "summary_pt": "y"}`}}
	a := &Analyzer{LLM: llm, Cache: &memCache{}, Retry: fastRetry}
	got, err := a.Translate(context.Background(), "Nova versão do Go é lançada", "O Go lançou uma nova versão com melhorias.")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Copied || got.Source() != "copy" || got.TitlePT != "Nova versão do Go é lançada" ||
		got.SummaryPT != "O Go lançou uma nova versão com melhorias." {
		t.Fatalf("copy = %+v", got)
	}
	if len(llm.calls) != 0 {
		t.Fatalf("model called %d times for Portuguese text", len(llm.calls))
	}
}

func TestTranslateCallsModelAndCaches(t *testing.T) {
	llm := &fakeLLM{answers: []string{"```json\n{\"title_pt\": \" Go 1.28 é lançado \", \"summary_pt\": \"Compilador mais rápido.\"}\n```"}}
	cache := &memCache{}
	a := &Analyzer{LLM: llm, Cache: cache, Retry: fastRetry}
	got, err := a.Translate(context.Background(), "Go 1.28 is released", "A faster compiler.")
	if err != nil {
		t.Fatal(err)
	}
	if got.Copied || got.Source() != "llm" || got.TitlePT != "Go 1.28 é lançado" || got.SummaryPT != "Compilador mais rápido." {
		t.Fatalf("translation = %+v", got)
	}
	if len(llm.calls) != 1 || !llm.calls[0].JSON || !strings.Contains(llm.calls[0].System, "pt-BR") ||
		!strings.Contains(llm.calls[0].User, "<untrusted_item>") {
		t.Fatalf("calls = %+v", llm.calls)
	}
	for k := range cache.m {
		if !strings.HasPrefix(k, CacheKindTranslate+"|") {
			t.Errorf("cache key %q not under kind %q", k, CacheKindTranslate)
		}
	}
	again, err := a.Translate(context.Background(), "Go 1.28 is released", "A faster compiler.")
	if err != nil || again != got || len(llm.calls) != 1 {
		t.Fatalf("second call = %+v, %v, model calls %d (want cache hit)", again, err, len(llm.calls))
	}
}

func TestTranslateRetriesAndReportsInvalidOutput(t *testing.T) {
	llm := &fakeLLM{answers: []string{"not json", `{"summary_pt": "s"}`, `{"title_pt": "t"}`}}
	a := &Analyzer{LLM: llm, Retry: fastRetry}
	_, err := a.Translate(context.Background(), "Go ships", "A summary.")
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("err = %v, want ErrInvalidOutput", err)
	}
	if len(llm.calls) != fastRetry.Attempts {
		t.Fatalf("model calls = %d, want %d", len(llm.calls), fastRetry.Attempts)
	}

	flaky := &fakeLLM{errs: []error{errors.New("503")}, answers: []string{"", `{"title_pt": "Go sai", "summary_pt": ""}`}}
	a = &Analyzer{LLM: flaky, Retry: fastRetry}
	got, err := a.Translate(context.Background(), "Go ships", "")
	if err != nil || got.TitlePT != "Go sai" || got.SummaryPT != "" || len(flaky.calls) != 2 {
		t.Fatalf("after outage = %+v, %v, calls %d", got, err, len(flaky.calls))
	}

	if _, err := a.Translate(context.Background(), "  ", "x"); err == nil {
		t.Fatal("empty title translated")
	}
}

func TestParseTranslation(t *testing.T) {
	cases := []struct {
		raw        string
		hasSummary bool
		ok         bool
		title, sum string
	}{
		{`{"title_pt": "T", "summary_pt": "S"}`, true, true, "T", "S"},
		{`{"title_pt": "T", "summary_pt": "invented"}`, false, true, "T", ""},
		{`{"title_pt": "", "summary_pt": "S"}`, true, false, "", ""},
		{`{"title_pt": "T", "summary_pt": "  "}`, true, false, "", ""},
		{`nope`, true, false, "", ""},
	}
	for _, c := range cases {
		got, err := ParseTranslation(c.raw, c.hasSummary)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.raw, err, c.ok)
			continue
		}
		if err != nil && !errors.Is(err, ErrInvalidOutput) {
			t.Errorf("%s: err %v does not wrap ErrInvalidOutput", c.raw, err)
		}
		if c.ok && (got.TitlePT != c.title || got.SummaryPT != c.sum) {
			t.Errorf("%s: got %+v", c.raw, got)
		}
	}
}

// The translation step must not touch the scoring prompt or its cache key.
func TestTranslationKeepsScoringPromptVersion(t *testing.T) {
	if PromptVersion != "v2" {
		t.Fatalf("PromptVersion = %q; translation must not bump it", PromptVersion)
	}
	sys, _ := ScoringPrompt(ScoreInput{Title: "T"})
	if strings.Contains(sys, "pt-BR") || strings.Contains(sys, "translat") {
		t.Fatal("scoring prompt mentions translation")
	}
}
