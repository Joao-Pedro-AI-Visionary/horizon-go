package domain

import (
	"strings"
	"unicode"
)

// Translation is the pt-BR copy of an item's title and AI summary. Copied is
// set when the text was already Portuguese and no model was called.
type Translation struct {
	TitlePT   string `json:"title_pt"`
	SummaryPT string `json:"summary_pt"`
	Copied    bool   `json:"copied"`
}

// Translation sources stored in items.pt_source.
const (
	PTSourceLLM  = "llm"
	PTSourceCopy = "copy"
)

// Source is the items.pt_source value of t.
func (t Translation) Source() string {
	if t.Copied {
		return PTSourceCopy
	}
	return PTSourceLLM
}

// TranslationStatus summarizes the pt-BR backfill.
type TranslationStatus struct {
	// Remaining items (scored, deduped or duplicate) still without title_pt
	// that have attempts left.
	Remaining int `json:"remaining"`
	// Translated by the model.
	Translated int `json:"translated"`
	// Copied because the original was already Portuguese.
	Copied int `json:"copied"`
	// Failed: gave up after the maximum number of attempts.
	Failed int `json:"failed"`
}

// Function words that are frequent in one language and rare in the others.
// Shared words (que, para, por, como...) are left out on purpose.
var (
	ptMarkers = wordSet("o", "e", "os", "do", "da", "dos", "das", "no", "na", "nos", "nas", "ao", "aos", "à", "às",
		"um", "uma", "uns", "umas", "não", "são", "é", "em", "com", "pelo", "pela", "pelos", "pelas", "seu", "sua",
		"seus", "suas", "também", "mais", "isso", "este", "esta", "esse", "essa", "foi", "ser", "está", "estão",
		"novo", "nova", "novos", "novas", "agora", "você", "sobre", "entre", "até", "já", "quando", "onde", "porque",
		"de")
	enMarkers = wordSet("the", "and", "of", "to", "in", "is", "are", "was", "were", "for", "with", "on", "at", "by",
		"from", "this", "that", "it", "its", "an", "be", "how", "why", "what", "you", "your", "new", "now", "about",
		"into", "over", "after", "has", "have", "will", "can", "not", "but", "or", "than", "their", "they", "we",
		"our", "just", "more", "most")
	esMarkers = wordSet("el", "la", "los", "las", "del", "y", "es", "con", "una", "al", "lo", "pero", "más", "nueva",
		"nuevo", "están", "sus", "también", "hay", "muy", "ya", "este", "esta", "cuando")
)

func wordSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// LooksPortuguese is a cheap language check (no model, no dependency) used
// to copy Portuguese text instead of translating it. It counts Portuguese
// function words and words with ã/õ/ç against English and Spanish function
// words; it errs on the side of "not Portuguese" (the model then translates
// or reports already_pt).
func LooksPortuguese(texts ...string) bool {
	var pt, en, es int
	for _, text := range texts {
		for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
			isPT := ptMarkers[w] || strings.ContainsAny(w, "ãõç")
			if isPT {
				pt++
			}
			if enMarkers[w] {
				en++
			}
			if esMarkers[w] && !isPT {
				es++
			}
		}
	}
	return pt >= 2 && pt > 2*en && pt > es
}
