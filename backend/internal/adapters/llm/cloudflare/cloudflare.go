// Package cloudflare implements ports.Embedder on Cloudflare Workers AI
// (POST /accounts/{id}/ai/run/{model}). The default @cf/baai/bge-base-en-v1.5
// returns 768-dimensional vectors, the size of the items.embedding column, so
// no migration is needed (https://developers.cloudflare.com/workers-ai/models/bge-base-en-v1.5/).
//
// Free plan: 10,000 Neurons per day, reset at 00:00 UTC; bge-base costs 6,058
// Neurons per million input tokens; text embeddings are limited to 3,000
// requests per minute. When the daily allocation is spent the adapter returns
// ports.ErrEmbeddingUnavailable (until the next UTC midnight) so the pipeline
// keeps scoring items without embeddings instead of failing.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/llm/ratelimit"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
	"github.com/jpdev/horizon-go/internal/retry"
)

// Defaults (checked against the Workers AI docs, Nov 2026).
const (
	DefaultBaseURL    = "https://api.cloudflare.com/client/v4"
	DefaultModel      = "@cf/baai/bge-base-en-v1.5"
	DefaultDimensions = 768
	// DefaultPooling: Cloudflare recommends cls for accuracy; vectors made
	// with cls and mean are not comparable, so keep one per database.
	DefaultPooling = "cls"
	DefaultRPM     = 300 // far below the 3,000 rpm limit
	DefaultTimeout = 60 * time.Second
	maxBatch       = 100
	// bge-base reads at most 512 tokens; longer inputs are cut here.
	maxInputRunes = 1800
	// Workers AI error code for "daily free allocation used up".
	codeQuotaExceeded = 4006
)

// Config selects the account and model.
type Config struct {
	BaseURL    string
	AccountID  string
	APIToken   string
	Model      string
	Dimensions int
	Pooling    string // "" uses DefaultPooling; "-" omits the field
	Limiter    *ratelimit.Limiter
	Retries    int
	Client     *http.Client
	OnUsage    func(ctx context.Context, u domain.TokenUsage)
	Now        func() time.Time // tests
}

// Embedder is a ports.Embedder on Workers AI.
type Embedder struct{ cfg Config }

var _ ports.Embedder = (*Embedder)(nil)

// New fills defaults.
func New(cfg Config) *Embedder {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.Dimensions <= 0 {
		cfg.Dimensions = DefaultDimensions
	}
	switch cfg.Pooling {
	case "":
		cfg.Pooling = DefaultPooling
	case "-":
		cfg.Pooling = ""
	}
	if cfg.Limiter == nil {
		cfg.Limiter = ratelimit.NewPerMinute("cloudflare", DefaultRPM)
	}
	if cfg.Retries <= 0 {
		cfg.Retries = ratelimit.DefaultRetries
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: DefaultTimeout}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Embedder{cfg: cfg}
}

// Model is the Workers AI model name.
func (e *Embedder) Model() string { return e.cfg.Model }

type runRequest struct {
	Text    []string `json:"text"`
	Pooling string   `json:"pooling,omitempty"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type runResponse struct {
	Success bool `json:"success"`
	Result  struct {
		Shape []int       `json:"shape"`
		Data  [][]float32 `json:"data"`
	} `json:"result"`
	Errors []apiError `json:"errors"`
}

// Embed returns one vector per text, in batches of up to 100.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += maxBatch {
		vs, err := e.batch(ctx, texts[start:min(start+maxBatch, len(texts))])
		if err != nil {
			return nil, err
		}
		out = append(out, vs...)
	}
	return out, nil
}

func (e *Embedder) unavailable(why string, until time.Time) error {
	e.cfg.Limiter.Block(until.Sub(e.cfg.Now()))
	return fmt.Errorf("cloudflare embed: %s until %s: %w", why, until.UTC().Format(time.RFC3339), ports.ErrEmbeddingUnavailable)
}

// nextUTCMidnight is when the daily Neuron allocation resets.
func (e *Embedder) nextUTCMidnight() time.Time {
	n := e.cfg.Now().UTC()
	return time.Date(n.Year(), n.Month(), n.Day()+1, 0, 0, 0, 0, time.UTC)
}

func (e *Embedder) batch(ctx context.Context, texts []string) ([][]float32, error) {
	in := make([]string, len(texts))
	for i, t := range texts {
		if r := []rune(t); len(r) > maxInputRunes {
			t = string(r[:maxInputRunes])
		}
		in[i] = t
	}
	data, err := json.Marshal(runRequest{Text: in, Pooling: e.cfg.Pooling})
	if err != nil {
		return nil, retry.Permanent(err)
	}
	endpoint := e.cfg.BaseURL + "/accounts/" + e.cfg.AccountID + "/ai/run/" + e.cfg.Model

	var out runResponse
	for attempt := 0; ; attempt++ {
		if err := e.cfg.Limiter.Wait(ctx); err != nil {
			if le, ok := err.(*ratelimit.LimitedError); ok {
				return nil, fmt.Errorf("cloudflare embed: rate limited until %s: %w", le.Until.UTC().Format(time.RFC3339), ports.ErrEmbeddingUnavailable)
			}
			return nil, err
		}
		status, hdr, raw, err := e.post(ctx, endpoint, data)
		if err != nil {
			return nil, err
		}
		if quotaExceeded(raw) {
			return nil, e.unavailable("daily free allocation used up", e.nextUTCMidnight())
		}
		if status == http.StatusTooManyRequests {
			if attempt >= e.cfg.Retries {
				return nil, e.unavailable("still rate limited after retries", e.cfg.Now().Add(time.Minute))
			}
			wait, ok := ratelimit.RetryAfter(hdr, raw, e.cfg.Now())
			if !ok {
				wait = ratelimit.FallbackWait() << attempt
			}
			e.cfg.Limiter.Block(wait)
			continue
		}
		if status != http.StatusOK {
			return nil, statusErr(status, raw)
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("cloudflare embed: decode: %w", err)
		}
		if !out.Success {
			return nil, fmt.Errorf("cloudflare embed: unsuccessful response: %s", truncate(raw))
		}
		break
	}
	if len(out.Result.Data) != len(texts) {
		return nil, fmt.Errorf("cloudflare embed: %d vectors for %d inputs", len(out.Result.Data), len(texts))
	}
	for _, v := range out.Result.Data {
		if len(v) != e.cfg.Dimensions {
			return nil, retry.Permanent(fmt.Errorf("cloudflare embed: %s returned %d dims, want %d", e.cfg.Model, len(v), e.cfg.Dimensions))
		}
	}
	if e.cfg.OnUsage != nil {
		// The response has no token counts; record the call.
		e.cfg.OnUsage(ctx, domain.TokenUsage{Kind: "embed", Model: e.cfg.Model})
	}
	return out.Result.Data, nil
}

func (e *Embedder) post(ctx context.Context, endpoint string, data []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return 0, nil, nil, retry.Permanent(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.cfg.APIToken)
	resp, err := e.cfg.Client.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("cloudflare embed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("cloudflare embed: read: %w", err)
	}
	return resp.StatusCode, resp.Header, raw, nil
}

func quotaExceeded(raw []byte) bool {
	var r struct {
		Errors []apiError `json:"errors"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return false
	}
	for _, e := range r.Errors {
		if e.Code == codeQuotaExceeded || strings.Contains(strings.ToLower(e.Message), "daily free allocation") {
			return true
		}
	}
	return false
}

func truncate(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}

func statusErr(status int, raw []byte) error {
	err := fmt.Errorf("cloudflare embed: status %d: %s", status, truncate(raw))
	// Bad token, unknown model or account: will not fix itself.
	if status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests {
		return retry.Permanent(err)
	}
	return err
}
