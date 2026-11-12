package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jpdev/horizon-go/internal/adapters/llm/cloudflare"
	"github.com/jpdev/horizon-go/internal/adapters/llm/ollama"
	"github.com/jpdev/horizon-go/internal/adapters/llm/openai"
	"github.com/jpdev/horizon-go/internal/adapters/llm/ratelimit"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
)

// embedDims is the size of items.embedding / clusters.centroid (vector(768),
// migrations/00002). Every embedder must return exactly this many dimensions.
const embedDims = 768

// buildModels picks the chat and embedding adapters from the environment:
//
//	LLM_PROVIDER    ollama (default) | groq | openai (any OpenAI-compatible API)
//	EMBED_PROVIDER  ollama (default) | cloudflare | none (no embeddings, no semantic dedup)
//
//	ollama      OLLAMA_URL, LLM_MODEL, EMBED_MODEL, LLM_NUM_CTX (local dev, unchanged)
//	groq        GROQ_API_KEY, LLM_MODEL (default openai/gpt-oss-20b), LLM_RPM (default 20)
//	openai      OPENAI_BASE_URL, OPENAI_API_KEY, LLM_MODEL, LLM_RPM (default unpaced)
//	cloudflare  CF_ACCOUNT_ID, CF_API_TOKEN, EMBED_MODEL (default @cf/baai/bge-base-en-v1.5,
//	            768 dims), EMBED_RPM (default 300); a spent daily allocation degrades
//	            to "no embedding" instead of failing
//
// LLM_MAX_TOKENS caps the completion of the hosted chat providers (0 = unset).
func buildModels(env func(string) string, onUsage func(context.Context, domain.TokenUsage)) (ports.LLM, ports.Embedder, error) {
	get := func(k, def string) string {
		if v := strings.TrimSpace(env(k)); v != "" {
			return v
		}
		return def
	}
	num := func(k string) int {
		n, _ := strconv.Atoi(strings.TrimSpace(env(k)))
		return max(n, 0)
	}
	var oc *ollama.Client
	ollamaClient := func() *ollama.Client {
		if oc == nil {
			oc = ollama.New(ollama.Config{
				URL:        get("OLLAMA_URL", ollama.DefaultURL),
				ChatModel:  get("LLM_MODEL", ollama.DefaultChatModel),
				EmbedModel: get("EMBED_MODEL", ollama.DefaultEmbedModel),
				NumCtx:     num("LLM_NUM_CTX"),
				OnUsage:    onUsage,
			})
		}
		return oc
	}

	var llm ports.LLM
	switch p := strings.ToLower(get("LLM_PROVIDER", "ollama")); p {
	case "ollama":
		llm = ollamaClient().LLM()
	case "groq":
		key := env("GROQ_API_KEY")
		if strings.TrimSpace(key) == "" {
			return nil, nil, fmt.Errorf("LLM_PROVIDER=groq needs GROQ_API_KEY")
		}
		llm = openai.NewGroq(openai.GroqConfig{APIKey: key, Model: get("LLM_MODEL", ""), RPM: num("LLM_RPM"),
			MaxTokens: num("LLM_MAX_TOKENS"), OnUsage: onUsage})
	case "openai":
		base := get("OPENAI_BASE_URL", "")
		model := get("LLM_MODEL", "")
		if base == "" || model == "" {
			return nil, nil, fmt.Errorf("LLM_PROVIDER=openai needs OPENAI_BASE_URL and LLM_MODEL")
		}
		var lim *ratelimit.Limiter
		if rpm := num("LLM_RPM"); rpm > 0 {
			lim = ratelimit.NewPerMinute("openai", rpm)
		}
		llm = openai.New(openai.Config{Name: "openai", BaseURL: base, APIKey: env("OPENAI_API_KEY"), Model: model,
			ReasoningEffort: get("LLM_REASONING_EFFORT", ""), MaxTokens: num("LLM_MAX_TOKENS"), Limiter: lim, OnUsage: onUsage})
	default:
		return nil, nil, fmt.Errorf("unknown LLM_PROVIDER %q (ollama, groq or openai)", p)
	}

	var emb ports.Embedder
	switch p := strings.ToLower(get("EMBED_PROVIDER", "ollama")); p {
	case "ollama":
		emb = ollamaClient().Embedder()
	case "cloudflare":
		acc, tok := strings.TrimSpace(env("CF_ACCOUNT_ID")), strings.TrimSpace(env("CF_API_TOKEN"))
		if acc == "" || tok == "" {
			return nil, nil, fmt.Errorf("EMBED_PROVIDER=cloudflare needs CF_ACCOUNT_ID and CF_API_TOKEN")
		}
		rpm := num("EMBED_RPM")
		if rpm == 0 {
			rpm = cloudflare.DefaultRPM
		}
		emb = cloudflare.New(cloudflare.Config{AccountID: acc, APIToken: tok, Model: get("EMBED_MODEL", cloudflare.DefaultModel),
			Dimensions: embedDims, Limiter: ratelimit.NewPerMinute("cloudflare", rpm), OnUsage: onUsage})
	case "none":
		// nil Embedder: items are scored but not embedded; each one becomes its
		// own cluster (duplicate_count 0) and the dedup LLM is never called.
	default:
		return nil, nil, fmt.Errorf("unknown EMBED_PROVIDER %q (ollama, cloudflare or none)", p)
	}
	return llm, emb, nil
}
