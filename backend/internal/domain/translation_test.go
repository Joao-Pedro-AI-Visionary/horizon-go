package domain

import "testing"

func TestLooksPortuguese(t *testing.T) {
	cases := []struct {
		name  string
		texts []string
		want  bool
	}{
		{"pt title and summary", []string{"Lançamento do Go 1.27 traz melhorias no GC", "A nova versão reduz pausas e melhora o desempenho em servidores."}, true},
		{"pt short title", []string{"Novo modelo da OpenAI é lançado para desenvolvedores"}, true},
		{"pt with accents only", []string{"Atualização de segurança no kernel", ""}, true},
		{"en title and summary", []string{"Go 1.27 is out: the new release of Go", "The release improves the garbage collector and tooling."}, false},
		{"en with a Brazilian city", []string{"São Paulo startup raises $10M for AI agents", "The company builds coding agents for banks."}, false},
		{"en headline no stopwords", []string{"Show HN: Qwen3 quantized benchmarks"}, false},
		{"es title", []string{"El lanzamiento de la nueva versión de Go", "La versión trae mejoras en el rendimiento y es más rápida."}, false},
		{"empty", []string{"", ""}, false},
		{"mixed en summary wins", []string{"Rust 2.0 é anunciado", "The Rust team announced the 2.0 edition with a new borrow checker and faster builds for the ecosystem."}, false},
	}
	for _, c := range cases {
		if got := LooksPortuguese(c.texts...); got != c.want {
			t.Errorf("%s: LooksPortuguese(%q) = %v, want %v", c.name, c.texts, got, c.want)
		}
	}
}

func TestTranslationSource(t *testing.T) {
	if (Translation{Copied: true}).Source() != PTSourceCopy || (Translation{}).Source() != PTSourceLLM {
		t.Fatal("unexpected pt_source mapping")
	}
}

func TestDisplayPrefersPortugueseWithFallback(t *testing.T) {
	f := FeedItem{Title: "Go 1.27 released", Summary: "Faster GC.", TitlePT: "Go 1.27 lançado", SummaryPT: "GC mais rápido."}
	if f.DisplayTitle() != "Go 1.27 lançado" || f.DisplaySummary() != "GC mais rápido." {
		t.Fatalf("feed display = %q / %q", f.DisplayTitle(), f.DisplaySummary())
	}
	f.TitlePT, f.SummaryPT = "  ", ""
	if f.DisplayTitle() != "Go 1.27 released" || f.DisplaySummary() != "Faster GC." {
		t.Fatalf("feed fallback = %q / %q", f.DisplayTitle(), f.DisplaySummary())
	}
	d := ItemDetail{Title: "A", Summary: "B", TitlePT: "C"}
	if d.DisplayTitle() != "C" || d.DisplaySummary() != "B" {
		t.Fatalf("detail display = %q / %q", d.DisplayTitle(), d.DisplaySummary())
	}
}
