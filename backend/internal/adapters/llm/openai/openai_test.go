package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/llm/ratelimit"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
	"github.com/jpdev/horizon-go/internal/retry"
)

const okBody = `{"choices":[{"message":{"role":"assistant","content":"{\"score\":7}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":30}}`

func groqAt(url string, onUsage func(context.Context, domain.TokenUsage)) *Chat {
	c := NewGroq(GroqConfig{APIKey: "gsk_test", RPM: 6000, OnUsage: onUsage})
	c.cfg.BaseURL = url
	return c
}

func TestGroqRequestShape(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(okBody))
	}))
	defer srv.Close()

	var usage domain.TokenUsage
	c := groqAt(srv.URL, func(_ context.Context, u domain.TokenUsage) { usage = u })
	out, err := c.Chat(context.Background(), ports.ChatRequest{System: "sys", User: "usr", JSON: true})
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"score":7}` {
		t.Errorf("content = %q", out)
	}
	if auth != "Bearer gsk_test" {
		t.Errorf("auth = %q", auth)
	}
	if got["model"] != GroqDefaultModel || got["stream"] != false || got["temperature"] != 0.3 ||
		got["reasoning_effort"] != "low" || got["reasoning_format"] != "hidden" {
		t.Errorf("request = %v", got)
	}
	if rf, _ := got["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Errorf("response_format = %v", got["response_format"])
	}
	if msgs, _ := got["messages"].([]any); len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("messages = %v", msgs)
	}
	if usage.Kind != "chat" || usage.PromptTokens != 120 || usage.CompletionTokens != 30 || usage.Model != GroqDefaultModel {
		t.Errorf("usage = %+v", usage)
	}
}

func TestNonReasoningModelOmitsReasoningFields(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(okBody))
	}))
	defer srv.Close()
	c := NewGroq(GroqConfig{APIKey: "k", Model: "llama-3.1-8b-instant", RPM: 6000})
	c.cfg.BaseURL = srv.URL
	if _, err := c.Chat(context.Background(), ports.ChatRequest{User: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["reasoning_effort"]; ok {
		t.Errorf("reasoning_effort sent: %v", got)
	}
	if _, ok := got["response_format"]; ok {
		t.Errorf("response_format sent without JSON: %v", got)
	}
}

func TestRetriesOn429WithRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		w.Write([]byte(okBody))
	}))
	defer srv.Close()
	if _, err := groqAt(srv.URL, nil).Chat(context.Background(), ports.ChatRequest{User: "x"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestRetriesAreBounded(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	_, err := groqAt(srv.URL, nil).Chat(context.Background(), ports.ChatRequest{User: "x"})
	if err == nil || retry.IsPermanent(err) {
		t.Fatalf("429 must stay retryable: %v", err)
	}
	if calls.Load() != int32(ratelimit.DefaultRetries+1) {
		t.Errorf("calls = %d", calls.Load())
	}
}

func TestLongRetryAfterFailsFast(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600") // daily quota spent
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := groqAt(srv.URL, nil)
	start := time.Now()
	_, err := c.Chat(context.Background(), ports.ChatRequest{User: "x"})
	if !IsLimited(err) || time.Since(start) > 5*time.Second {
		t.Fatalf("want a fast LimitedError, got %v after %s", err, time.Since(start))
	}
	// The next call does not even reach the server.
	if _, err := c.Chat(context.Background(), ports.ChatRequest{User: "x"}); !IsLimited(err) || calls.Load() != 1 {
		t.Errorf("err=%v calls=%d", err, calls.Load())
	}
}

func TestLowTokenBudgetGatesNextCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-ratelimit-remaining-tokens", "100")
		w.Header().Set("x-ratelimit-reset-tokens", "2h0m0s")
		w.Write([]byte(okBody))
	}))
	defer srv.Close()
	c := groqAt(srv.URL, nil)
	if _, err := c.Chat(context.Background(), ports.ChatRequest{User: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Chat(context.Background(), ports.ChatRequest{User: "x"}); !IsLimited(err) {
		t.Errorf("second call must wait for the token window: %v", err)
	}
}

func TestJSONValidateFailedReturnsGeneration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"Failed to generate JSON","type":"invalid_request_error","code":"json_validate_failed","failed_generation":"Sure! {\"score\":5}"}}`))
	}))
	defer srv.Close()
	out, err := groqAt(srv.URL, nil).Chat(context.Background(), ports.ChatRequest{User: "x", JSON: true})
	if err != nil || out != `Sure! {"score":5}` {
		t.Errorf("out=%q err=%v", out, err)
	}
}

func TestAuthErrorIsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Invalid API Key"}}`))
	}))
	defer srv.Close()
	_, err := groqAt(srv.URL, nil).Chat(context.Background(), ports.ChatRequest{User: "x"})
	if err == nil || !retry.IsPermanent(err) {
		t.Fatalf("401 must be permanent: %v", err)
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv2.Close()
	if _, err := groqAt(srv2.URL, nil).Chat(context.Background(), ports.ChatRequest{User: "x"}); err == nil || retry.IsPermanent(err) {
		t.Fatalf("502 must be retryable: %v", err)
	}
}
