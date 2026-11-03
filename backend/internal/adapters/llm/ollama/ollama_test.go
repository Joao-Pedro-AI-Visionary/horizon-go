package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jpdev/horizon-go/internal/ports"
	"github.com/jpdev/horizon-go/internal/retry"
)

func TestChatRequestShape(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"model":"qwen3:14b","message":{"role":"assistant","content":"{\"score\":7}"},"done":true}`))
	}))
	defer srv.Close()

	out, err := New(Config{URL: srv.URL}).LLM().Chat(context.Background(), ports.ChatRequest{System: "sys", User: "usr", JSON: true})
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"score":7}` {
		t.Errorf("content = %q", out)
	}
	if got["model"] != DefaultChatModel || got["think"] != false || got["stream"] != false || got["format"] != "json" {
		t.Errorf("request = %v", got)
	}
	if opts, _ := got["options"].(map[string]any); opts["temperature"] != 0.3 {
		t.Errorf("temperature = %v", got["options"])
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("messages = %v", msgs)
	}
}

func TestEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path != "/api/embed" || req.Model != DefaultEmbedModel || len(req.Input) != 2 {
			t.Errorf("bad request %s %+v", r.URL.Path, req)
		}
		w.Write([]byte(`{"embeddings":[[0.1,0.2],[0.3,0.4]]}`))
	}))
	defer srv.Close()

	vs, err := New(Config{URL: srv.URL}).Embedder().Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[1][1] != 0.4 {
		t.Errorf("vectors = %v", vs)
	}
}

func TestErrorsArePermanentOnlyFor4xx(t *testing.T) {
	status := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer srv.Close()
	llm := New(Config{URL: srv.URL}).LLM()

	_, err := llm.Chat(context.Background(), ports.ChatRequest{User: "x"})
	if err == nil || !retry.IsPermanent(err) {
		t.Fatalf("404 must be permanent: %v", err)
	}
	status = http.StatusServiceUnavailable
	_, err = llm.Chat(context.Background(), ports.ChatRequest{User: "x"})
	if err == nil || retry.IsPermanent(err) {
		t.Fatalf("503 must be retryable: %v", err)
	}
}

func TestEmbedCountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"embeddings":[[0.1]]}`))
	}))
	defer srv.Close()
	if _, err := New(Config{URL: srv.URL}).Embedder().Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("want error on vector count mismatch")
	}
}
