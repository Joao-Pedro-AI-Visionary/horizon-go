package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
	"github.com/jpdev/horizon-go/internal/retry"
)

// TranslatePromptVersion is part of the "translate" cache key. It is separate
// from PromptVersion so translation changes never bust the scoring cache.
const TranslatePromptVersion = "t1"

// CacheKindTranslate is the ai_cache kind of translations.
const CacheKindTranslate = "translate"

const translateSystem = `You are a professional technology news translator. Translate the item title and summary into Brazilian Portuguese (pt-BR).

- ` + untrustedInputRule + `
- Translate faithfully; do not add, remove, or explain information.
` + evidenceRules + `
- Keep product names, company names, model names, version numbers, code identifiers, commands, and URLs exactly as written.
- Keep established English technical terms when Brazilian developers normally use them (e.g. "open source", "benchmark", "prompt").
- If the summary is empty, return an empty summary_pt.

Return valid JSON only:
{
  "title_pt": "<title in pt-BR>",
  "summary_pt": "<summary in pt-BR>"
}`

// TranslationPrompt builds the system and user messages for one item.
func TranslationPrompt(title, summary string) (system, user string) {
	user = fmt.Sprintf(`Translate the following item.

<untrusted_item>
Title: %s
Summary: %s
</untrusted_item>`, sanitize(title, 500), sanitize(summary, maxTextRunes))
	return translateSystem, user
}

// ParseTranslation validates the translator output contract. Errors wrap
// ErrInvalidOutput.
func ParseTranslation(raw string, hasSummary bool) (domain.Translation, error) {
	body, err := extractJSON(raw)
	if err != nil {
		return domain.Translation{}, invalid(err)
	}
	var v struct {
		TitlePT   string `json:"title_pt"`
		SummaryPT string `json:"summary_pt"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return domain.Translation{}, invalid(fmt.Errorf("decode translation: %w", err))
	}
	t := domain.Translation{
		TitlePT:   truncateRunes(strings.TrimSpace(v.TitlePT), 500),
		SummaryPT: truncateRunes(strings.TrimSpace(v.SummaryPT), maxTextRunes),
	}
	if t.TitlePT == "" {
		return domain.Translation{}, invalid(errors.New("translation: missing title_pt"))
	}
	if hasSummary && t.SummaryPT == "" {
		return domain.Translation{}, invalid(errors.New("translation: missing summary_pt"))
	}
	if !hasSummary {
		t.SummaryPT = ""
	}
	return t, nil
}

// Translate returns the pt-BR title and summary of an item. Text that already
// looks Portuguese is copied without calling the model (Copied=true); the
// rest goes to the LLM port with retry and the "translate" cache.
func (a *Analyzer) Translate(ctx context.Context, title, summary string) (domain.Translation, error) {
	title, summary = strings.TrimSpace(title), strings.TrimSpace(summary)
	if title == "" {
		return domain.Translation{}, errors.New("translate: empty title")
	}
	if domain.LooksPortuguese(title, summary) {
		return domain.Translation{TitlePT: title, SummaryPT: summary, Copied: true}, nil
	}
	key := domain.ContentHash(TranslatePromptVersion, a.LLM.Model(), title, summary)
	var out domain.Translation
	if a.cached(ctx, CacheKindTranslate, key, &out) && out.TitlePT != "" {
		out.Copied = false
		return out, nil
	}
	sys, user := TranslationPrompt(title, summary)
	err := retry.Do(ctx, a.policy(), func(ctx context.Context) error {
		raw, err := a.LLM.Chat(ctx, ports.ChatRequest{System: sys, User: user, JSON: true})
		if err != nil {
			return err
		}
		out, err = ParseTranslation(raw, summary != "")
		return err
	})
	if err != nil {
		return domain.Translation{}, fmt.Errorf("translate: %w", err)
	}
	a.store(ctx, CacheKindTranslate, key, a.LLM.Model(), out)
	return out, nil
}
