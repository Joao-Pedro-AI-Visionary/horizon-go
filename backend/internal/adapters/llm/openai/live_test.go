package openai

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/ports"
)

// TestGroqLive scores one item with the real Groq API. Opt-in:
//
//	HORIZON_LIVE=1 GROQ_API_KEY=... [LLM_MODEL=...] go test ./internal/adapters/llm/openai -run Live -v
func TestGroqLive(t *testing.T) {
	key := os.Getenv("GROQ_API_KEY")
	if os.Getenv("HORIZON_LIVE") != "1" || key == "" {
		t.Skip("set HORIZON_LIVE=1 and GROQ_API_KEY")
	}
	c := NewGroq(GroqConfig{APIKey: key, Model: os.Getenv("LLM_MODEL")})
	sys, user := app.ScoringPrompt(app.ScoreInput{
		Title:   "Go 1.26 is released",
		URL:     "https://go.dev/blog/go1.26",
		Source:  "rss",
		Content: "The Go team is happy to announce Go 1.26, with a new garbage collector default, faster cgo calls and iterator improvements in the standard library.",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	start := time.Now()
	raw, err := c.Chat(ctx, ports.ChatRequest{System: sys, User: user, JSON: true})
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.ParseAnalysis(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	t.Logf("model=%s in %s: score=%.1f tags=%v summary=%q", c.Model(), time.Since(start).Round(time.Millisecond), a.Score, a.Tags, a.Summary)
}
