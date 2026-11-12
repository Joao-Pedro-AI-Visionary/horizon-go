// Package openai implements ports.LLM on any OpenAI-compatible
// /chat/completions endpoint. Groq (https://api.groq.com/openai/v1) is the
// production preset: free-tier quotas are respected by pacing requests,
// honouring 429 Retry-After with bounded retries and waiting for the
// per-minute token budget reported in the x-ratelimit-* headers.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/llm/ratelimit"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
	"github.com/jpdev/horizon-go/internal/retry"
)

// Groq preset (checked against https://console.groq.com/docs/models and
// https://console.groq.com/docs/rate-limits, Nov 2026). openai/gpt-oss-20b is
// a free-plan model (30 RPM, 1K RPD, 8K TPM, 200K TPD) with JSON mode and a
// low reasoning effort.
const (
	GroqBaseURL         = "https://api.groq.com/openai/v1"
	GroqDefaultModel    = "openai/gpt-oss-20b"
	GroqDefaultRPM      = 20   // below the 30 RPM free limit
	GroqDefaultMinToken = 3000 // wait for the TPM window when fewer tokens remain
	GroqReasoningEffort = "low"

	DefaultTemperature = 0.3
	DefaultTimeout     = 90 * time.Second
)

// Config selects the endpoint and model.
type Config struct {
	Name    string // provider label for logs and errors (e.g. "groq")
	BaseURL string // ends in /v1; /chat/completions is appended
	APIKey  string
	Model   string
	// ReasoningEffort is sent as reasoning_effort (GPT-OSS / Qwen on Groq)
	// together with reasoning_format=hidden; empty omits both.
	ReasoningEffort string
	Temperature     float64
	MaxTokens       int // max_completion_tokens; 0 omits it
	// MinTokens: when x-ratelimit-remaining-tokens drops below it, the next
	// call waits for x-ratelimit-reset-tokens. 0 disables.
	MinTokens int
	Limiter   *ratelimit.Limiter // optional pacing (shared by all workers)
	Retries   int                // 429s absorbed per call (default ratelimit.DefaultRetries)
	Client    *http.Client
	OnUsage   func(ctx context.Context, u domain.TokenUsage)
}

// Chat is a ports.LLM over an OpenAI-compatible API.
type Chat struct{ cfg Config }

var _ ports.LLM = (*Chat)(nil)

// New fills defaults.
func New(cfg Config) *Chat {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Name == "" {
		cfg.Name = "openai"
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = DefaultTemperature
	}
	if cfg.Retries <= 0 {
		cfg.Retries = ratelimit.DefaultRetries
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: DefaultTimeout}
	}
	return &Chat{cfg: cfg}
}

// GroqConfig is the subset of Config the Groq preset exposes.
type GroqConfig struct {
	APIKey    string
	Model     string // "" uses GroqDefaultModel
	RPM       int    // <= 0 uses GroqDefaultRPM
	MaxTokens int    // 0 omits max_completion_tokens
	OnUsage   func(ctx context.Context, u domain.TokenUsage)
}

// NewGroq returns the Groq preset. GPT-OSS models get reasoning_effort=low so
// reasoning tokens do not eat the free per-minute token budget.
func NewGroq(g GroqConfig) *Chat {
	if g.Model == "" {
		g.Model = GroqDefaultModel
	}
	if g.RPM <= 0 {
		g.RPM = GroqDefaultRPM
	}
	effort := ""
	if strings.HasPrefix(g.Model, "openai/gpt-oss") {
		effort = GroqReasoningEffort
	}
	return New(Config{
		Name: "groq", BaseURL: GroqBaseURL, APIKey: g.APIKey, Model: g.Model,
		ReasoningEffort: effort, MaxTokens: g.MaxTokens, MinTokens: GroqDefaultMinToken,
		Limiter: ratelimit.NewPerMinute("groq", g.RPM), OnUsage: g.OnUsage,
	})
}

// Model is the model id.
func (c *Chat) Model() string { return c.cfg.Model }

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatRequest struct {
	Model           string          `json:"model"`
	Messages        []message       `json:"messages"`
	Temperature     float64         `json:"temperature"`
	Stream          bool            `json:"stream"`
	ResponseFormat  *responseFormat `json:"response_format,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	ReasoningFormat string          `json:"reasoning_format,omitempty"`
	MaxTokens       int             `json:"max_completion_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Chat sends one non-streaming completion.
func (c *Chat) Chat(ctx context.Context, req ports.ChatRequest) (string, error) {
	body := chatRequest{Model: c.cfg.Model, Temperature: c.cfg.Temperature, MaxTokens: c.cfg.MaxTokens}
	if req.JSON {
		body.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	if c.cfg.ReasoningEffort != "" {
		body.ReasoningEffort = c.cfg.ReasoningEffort
		body.ReasoningFormat = "hidden"
	}
	if req.System != "" {
		body.Messages = append(body.Messages, message{Role: "system", Content: req.System})
	}
	body.Messages = append(body.Messages, message{Role: "user", Content: req.User})
	data, err := json.Marshal(body)
	if err != nil {
		return "", retry.Permanent(err)
	}

	var out chatResponse
	for attempt := 0; ; attempt++ {
		status, hdr, raw, err := c.do(ctx, data)
		if err != nil {
			return "", err
		}
		c.trackBudget(hdr)
		if status == http.StatusTooManyRequests && attempt < c.cfg.Retries {
			wait, ok := ratelimit.RetryAfter(hdr, raw, time.Now())
			if !ok {
				wait = ratelimit.FallbackWait() << attempt
			}
			c.cfg.Limiter.Block(wait)
			if c.cfg.Limiter == nil {
				// No shared limiter: wait here, bounded.
				if wait > ratelimit.DefaultMaxWait {
					return "", &ratelimit.LimitedError{Provider: c.cfg.Name, Until: time.Now().Add(wait)}
				}
				if err := sleep(ctx, wait); err != nil {
					return "", err
				}
			}
			continue
		}
		if status == http.StatusBadRequest {
			// Groq JSON mode rejects output that is not valid JSON with 400
			// json_validate_failed and returns it in failed_generation. Hand it
			// back so the caller's output validation counts it as an invalid
			// answer (and may still rescue a wrapped object) instead of a
			// permanent configuration error.
			if gen, ok := failedGeneration(raw); ok {
				return gen, nil
			}
		}
		if status != http.StatusOK {
			return "", c.statusErr(status, raw)
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", fmt.Errorf("%s chat: decode: %w", c.cfg.Name, err)
		}
		break
	}
	if c.cfg.OnUsage != nil {
		c.cfg.OnUsage(ctx, domain.TokenUsage{Kind: "chat", Model: c.cfg.Model,
			PromptTokens: out.Usage.PromptTokens, CompletionTokens: out.Usage.CompletionTokens})
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("%s chat: empty response", c.cfg.Name)
	}
	return out.Choices[0].Message.Content, nil
}

func (c *Chat) do(ctx context.Context, data []byte) (int, http.Header, []byte, error) {
	if err := c.cfg.Limiter.Wait(ctx); err != nil {
		return 0, nil, nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return 0, nil, nil, retry.Permanent(err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.cfg.Client.Do(hreq)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%s chat: %w", c.cfg.Name, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%s chat: read: %w", c.cfg.Name, err)
	}
	return resp.StatusCode, resp.Header, raw, nil
}

// trackBudget gates the next call when the per-minute token budget is low.
func (c *Chat) trackBudget(h http.Header) {
	if c.cfg.MinTokens <= 0 || h == nil {
		return
	}
	rem, err := strconv.Atoi(strings.TrimSpace(h.Get("x-ratelimit-remaining-tokens")))
	if err != nil || rem >= c.cfg.MinTokens {
		return
	}
	if reset, err := time.ParseDuration(strings.TrimSpace(h.Get("x-ratelimit-reset-tokens"))); err == nil {
		c.cfg.Limiter.Block(reset)
	}
}

func (c *Chat) statusErr(status int, raw []byte) error {
	msg := strings.TrimSpace(string(raw))
	if len(msg) > 500 {
		msg = msg[:500]
	}
	err := fmt.Errorf("%s chat: status %d: %s", c.cfg.Name, status, msg)
	// 4xx (bad key, unknown model, bad request) will not fix itself; 408/429/5xx may.
	if status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests {
		return retry.Permanent(err)
	}
	return err
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func failedGeneration(raw []byte) (string, bool) {
	var e struct {
		Error struct {
			Code             string `json:"code"`
			FailedGeneration string `json:"failed_generation"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Error.Code != "json_validate_failed" || strings.TrimSpace(e.Error.FailedGeneration) == "" {
		return "", false
	}
	return e.Error.FailedGeneration, true
}

// IsLimited reports whether err is a provider rate-limit block.
func IsLimited(err error) bool {
	var le *ratelimit.LimitedError
	return errors.As(err, &le)
}
