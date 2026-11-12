package main

import (
	"strings"
	"testing"

	"github.com/jpdev/horizon-go/internal/adapters/llm/cloudflare"
	"github.com/jpdev/horizon-go/internal/adapters/llm/ollama"
	"github.com/jpdev/horizon-go/internal/adapters/llm/openai"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestBuildModelsDefaultsToOllama(t *testing.T) {
	llm, emb, err := buildModels(envOf(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if llm.Model() != ollama.DefaultChatModel || emb.Model() != ollama.DefaultEmbedModel {
		t.Errorf("llm=%s emb=%s", llm.Model(), emb.Model())
	}
	if _, ok := llm.(*ollama.Chat); !ok {
		t.Errorf("llm is %T", llm)
	}
}

func TestBuildModelsProduction(t *testing.T) {
	llm, emb, err := buildModels(envOf(map[string]string{
		"LLM_PROVIDER": "groq", "GROQ_API_KEY": "gsk", "EMBED_PROVIDER": "none",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := llm.(*openai.Chat); !ok || llm.Model() != openai.GroqDefaultModel {
		t.Errorf("llm = %T %s", llm, llm.Model())
	}
	if emb != nil {
		t.Errorf("EMBED_PROVIDER=none must give a nil embedder, got %T", emb)
	}
	llm, emb, err = buildModels(envOf(map[string]string{
		"LLM_PROVIDER": "groq", "GROQ_API_KEY": "gsk", "EMBED_PROVIDER": "cloudflare", "CF_ACCOUNT_ID": "acc", "CF_API_TOKEN": "tok",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := emb.(*cloudflare.Embedder); !ok || emb.Model() != cloudflare.DefaultModel {
		t.Errorf("emb = %T", emb)
	}
	// Mixed: hosted chat, local embeddings.
	llm, emb, err = buildModels(envOf(map[string]string{"LLM_PROVIDER": "groq", "GROQ_API_KEY": "gsk", "LLM_MODEL": "qwen/qwen3.8-27b"}), nil)
	if err != nil || llm.Model() != "qwen/qwen3.8-27b" || emb.Model() != ollama.DefaultEmbedModel {
		t.Errorf("mixed: %v %v %v", err, llm, emb)
	}
}

func TestBuildModelsErrors(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"groq without key":    {"LLM_PROVIDER": "groq"},
		"gemini (removed)":    {"EMBED_PROVIDER": "gemini"},
		"cloudflare no token": {"EMBED_PROVIDER": "cloudflare", "CF_ACCOUNT_ID": "acc"},
		"openai without url":  {"LLM_PROVIDER": "openai", "LLM_MODEL": "m"},
		"unknown llm":         {"LLM_PROVIDER": "nope"},
		"unknown embedder":    {"EMBED_PROVIDER": "nope"},
	} {
		if _, _, err := buildModels(envOf(env), nil); err == nil || !strings.Contains(err.Error(), "_") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
