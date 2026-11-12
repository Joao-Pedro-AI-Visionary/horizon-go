// Package ingest wires the Phase 1 pipeline: configured sources -> collector
// -> NATS items.raw -> durable persister -> items.
package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/bus/natsbus"
	"github.com/jpdev/horizon-go/internal/adapters/source"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
)

// Pipeline holds the dependencies of one ingestion cycle.
type Pipeline struct {
	Sources   ports.SourceRepo
	Collector *app.Collector
	Persister *natsbus.Persister
	Client    *http.Client
	BaseURLs  source.BaseURLs
	Log       *slog.Logger
}

// Result is the outcome of Once.
type Result struct {
	Report    app.Report
	Persisted int
}

// brokenSource reports a configuration error through the normal fetch_runs path.
type brokenSource struct {
	name string
	err  error
}

func (b brokenSource) Name() string { return b.name }
func (b brokenSource) Fetch(context.Context, time.Time) ([]domain.Item, error) {
	return nil, b.err
}

// Jobs builds one job per enabled source.
func (p *Pipeline) Jobs(ctx context.Context) ([]app.Job, error) {
	rows, err := p.Sources.List(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	jobs := make([]app.Job, 0, len(rows))
	for _, row := range rows {
		if row.Kind == domain.KindPush {
			continue // items arrive through POST /api/items/ingest
		}
		s, err := source.Build(row, p.Client, p.BaseURLs)
		if err != nil {
			s = brokenSource{name: row.Name, err: err}
		}
		jobs = append(jobs, app.Job{SourceID: row.ID, Source: s})
	}
	return jobs, nil
}

// Collect runs the collector over every enabled source.
func (p *Pipeline) Collect(ctx context.Context, since time.Time) (app.Report, error) {
	jobs, err := p.Jobs(ctx)
	if err != nil {
		return app.Report{}, err
	}
	return p.Collector.Run(ctx, jobs, since)
}

// CollectSource runs the collector over one source, enabled or not.
func (p *Pipeline) CollectSource(ctx context.Context, id int64, since time.Time) (app.Report, error) {
	row, err := p.Sources.Get(ctx, id)
	if err != nil {
		return app.Report{}, err
	}
	if row.Kind == domain.KindPush {
		return app.Report{}, domain.Invalidf("source %q is a push source: send its items to POST /api/items/ingest", row.Name)
	}
	s, err := source.Build(row, p.Client, p.BaseURLs)
	if err != nil {
		s = brokenSource{name: row.Name, err: err}
	}
	return p.Collector.Run(ctx, []app.Job{{SourceID: row.ID, Source: s}}, since)
}

// Once collects, then drains the consumer so every published item is in
// Postgres when it returns.
func (p *Pipeline) Once(ctx context.Context, since time.Time) (Result, error) {
	rep, err := p.Collect(ctx, since)
	if err != nil {
		return Result{Report: rep}, err
	}
	n, err := p.Persister.Drain(ctx)
	return Result{Report: rep, Persisted: n}, err
}

// DefaultSources are seeded by `worker --seed` into an empty sources table:
// AI news and model launches, AI-assisted development and coding agents,
// local LLMs, useful open-source repositories, and harness, token-saving and
// developer-productivity writing. Every feed was fetched and returned valid
// entries when it was added. Per-feed limits keep two collections a day
// under roughly 100 scored items (Groq free tier).
func DefaultSources() []domain.SourceConfig {
	rss := func(name, cfg string) domain.SourceConfig {
		return domain.SourceConfig{Kind: domain.KindRSS, Name: name, Enabled: true, Config: json.RawMessage(cfg)}
	}
	return []domain.SourceConfig{
		rss("Hacker News · IA", `{"url":"https://hnrss.org/newest?q=AI+OR+LLM+OR+GPT+OR+Claude+OR+Gemini&points=100","limit":8}`),
		rss("Hacker News · Claude Code, Codex e Cursor", `{"url":"https://hnrss.org/newest?q=%22Claude+Code%22+OR+Codex+OR+Cursor&points=50","limit":4}`),
		rss("Hacker News · agentes e MCP", `{"url":"https://hnrss.org/newest?q=%22coding+agent%22+OR+MCP&points=50","limit":4}`),
		rss("r/LocalLLaMA", `{"url":"https://www.reddit.com/r/LocalLLaMA/top/.rss?t=day","limit":8}`),
		rss("r/ClaudeAI", `{"url":"https://www.reddit.com/r/ClaudeAI/top/.rss?t=day","limit":6}`),
		rss("Simon Willison", `{"url":"https://simonwillison.net/atom/everything/","limit":5}`),
		rss("Latent Space", `{"url":"https://www.latent.space/feed","limit":3}`),
		rss("Hugging Face Blog", `{"url":"https://huggingface.co/blog/feed.xml","limit":3}`),
		rss("OpenAI News", `{"url":"https://openai.com/news/rss.xml","limit":4}`),
		rss("Anthropic News", `{"url":"https://raw.githubusercontent.com/Olshansk/rss-feeds/main/feeds/feed_anthropic_news.xml","limit":3}`),
		rss("Anthropic Engineering", `{"url":"https://raw.githubusercontent.com/Olshansk/rss-feeds/main/feeds/feed_anthropic_engineering.xml","limit":3}`),
		rss("Google AI Blog", `{"url":"https://blog.google/technology/ai/rss/","limit":3}`),
		rss("Google DeepMind", `{"url":"https://deepmind.google/blog/rss.xml","limit":3}`),
		rss("Mistral AI", `{"url":"https://mistral.ai/rss.xml","limit":2}`),
		rss("Ollama Blog", `{"url":"https://ollama.com/blog/rss.xml","limit":2}`),
		rss("GitHub Trending (diário)", `{"url":"https://mshibanami.github.io/GitHubTrendingRSS/daily/all.xml","limit":8,"undated":true}`),
		rss("GitHub Blog · AI & ML", `{"url":"https://github.blog/ai-and-ml/feed/","limit":3}`),
		rss("Cursor Changelog", `{"url":"https://cursor.com/changelog/rss.xml","limit":3}`),
		rss("Releases · obra/superpowers", `{"url":"https://github.com/obra/superpowers/releases.atom","limit":2}`),
		rss("Releases · anthropics/claude-code", `{"url":"https://github.com/anthropics/claude-code/releases.atom","limit":2}`),
		rss("Releases · openai/codex", `{"url":"https://github.com/openai/codex/releases.atom","limit":2}`),
		rss("Releases · ollama/ollama", `{"url":"https://github.com/ollama/ollama/releases.atom","limit":2}`),
		rss("Addy Osmani", `{"url":"https://addyo.substack.com/feed","limit":2}`),
		rss("The Pragmatic Engineer", `{"url":"https://newsletter.pragmaticengineer.com/feed","limit":2}`),
		rss("MarkTechPost", `{"url":"https://www.marktechpost.com/feed/","limit":4}`),
		rss("TechCrunch · IA", `{"url":"https://techcrunch.com/category/artificial-intelligence/feed/","limit":5}`),
	}
}
