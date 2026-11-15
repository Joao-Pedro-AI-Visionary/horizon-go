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
// from PromptVersion so changing one prompt never busts the other cache.
const TranslatePromptVersion = "t1"

// TranslateCacheKind is the ai_cache kind of translations.
const TranslateCacheKind = "translate"

const translateSystem = `You translate technology news into Brazilian Portuguese (pt-BR).

- ` + untrustedInputRule + `
- Translate only; do not summarize, expand, comment or add anything.
` + evidenceRules + `
- Keep product, company, project and people names, versions, numbers, code, commands, file names and URLs exactly as written.
- Keep widely used technical terms in English when that is how Brazilian developers say them (e.g. "framework", "deploy", "benchmark", "open source").
- If the text is already in Portuguese, return it unchanged and set "already_pt" to true.

Return valid JSON only:
{
  "title_pt": "<title in pt-BR>",
  "summary_pt": "<summary in pt-BR, empty if no summary was given>",
  "already_pt": <true|false>
}`

// TranslatePrompt builds the messages for one item.
func TranslatePrompt(title, summary string) (system, user string) {
	s := sanitize(summary, maxTextRunes*2)
	if s == "" {
		s = "(none)"
	}
	return translateSystem, fmt.Sprintf(`Translate the following item.

<untrusted_item>
Title: %s
Summary: %s
</untrusted_item>`, sanitize(title, 500), s)
}

// ParseTranslation validates the translation output contract. Errors wrap
// ErrInvalidOutput. When the model reports already_pt the originals are kept
// verbatim and the result is marked Copied.
func ParseTranslation(raw, title, summary string) (domain.Translation, error) {
	t, err := parseTranslation(raw, title, summary)
	return t, invalid(err)
}

func parseTranslation(raw, title, summary string) (domain.Translation, error) {
	body, err := extractJSON(raw)
	if err != nil {
		return domain.Translation{}, err
	}
	var v struct {
		TitlePT   string `json:"title_pt"`
		SummaryPT string `json:"summary_pt"`
		AlreadyPT bool   `json:"already_pt"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return domain.Translation{}, err
	}
	if v.AlreadyPT {
		return domain.Translation{TitlePT: title, SummaryPT: summary, Copied: true}, nil
	}
	t := domain.Translation{
		TitlePT:   truncateRunes(strings.TrimSpace(v.TitlePT), 500),
		SummaryPT: truncateRunes(strings.TrimSpace(v.SummaryPT), maxTextRunes*2),
	}
	if t.TitlePT == "" {
		return domain.Translation{}, errors.New("empty title_pt")
	}
	if strings.TrimSpace(summary) == "" {
		t.SummaryPT = ""
	} else if t.SummaryPT == "" {
		return domain.Translation{}, errors.New("empty summary_pt for a non-empty summary")
	}
	return t, nil
}

// Translate returns the pt-BR title and summary of an item. Text that
// already looks Portuguese is copied without a model call. Model answers are
// cached under kind "translate"; invalid output is retried and, when it
// persists, returned wrapping ErrInvalidOutput.
func (a *Analyzer) Translate(ctx context.Context, title, summary string) (domain.Translation, error) {
	if domain.LooksPortuguese(title, summary) {
		return domain.Translation{TitlePT: title, SummaryPT: summary, Copied: true}, nil
	}
	key := domain.ContentHash(TranslatePromptVersion, a.LLM.Model(), title, summary)
	var out domain.Translation
	if a.cached(ctx, TranslateCacheKind, key, &out) && out.TitlePT != "" {
		return out, nil
	}
	sys, user := TranslatePrompt(title, summary)
	err := retry.Do(ctx, a.policy(), func(ctx context.Context) error {
		raw, err := a.LLM.Chat(ctx, ports.ChatRequest{System: sys, User: user, JSON: true})
		if err != nil {
			return err
		}
		out, err = ParseTranslation(raw, title, summary)
		return err
	})
	if err != nil {
		return domain.Translation{}, fmt.Errorf("translate: %w", err)
	}
	a.store(ctx, TranslateCacheKind, key, a.LLM.Model(), out)
	return out, nil
}
