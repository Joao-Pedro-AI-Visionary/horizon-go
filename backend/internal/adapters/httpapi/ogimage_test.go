package httpapi

import (
	"strings"
	"testing"

	"github.com/jpdev/horizon-go/internal/domain"
)

func TestOGFooterBrandsRadarByJpDev(t *testing.T) {
	const want = "Radar by JpDev · horizon-web-eight.vercel.app"
	if ogFooter != want {
		t.Fatalf("ogFooter = %q, want %q", ogFooter, want)
	}
	if strings.Contains(strings.ToLower(ogFooter), "horariodebrasilia") {
		t.Fatalf("ogFooter still carries the old typo: %q", ogFooter)
	}
}

func TestOGCopyPrefersPortugueseWithFallback(t *testing.T) {
	pt := domain.ItemDetail{Title: "Go ships", Summary: "Faster compiler.", TitlePT: "Go lança", SummaryPT: "Compilador mais rápido."}
	if title, summary := ogCopy(pt); title != "Go lança" || summary != "Compilador mais rápido." {
		t.Fatalf("ogCopy(pt) = %q, %q", title, summary)
	}
	orig := domain.ItemDetail{Title: "Go ships", Summary: "Faster compiler.", TitlePT: "  "}
	if title, summary := ogCopy(orig); title != "Go ships" || summary != "Faster compiler." {
		t.Fatalf("ogCopy(orig) = %q, %q", title, summary)
	}
	reason := domain.ItemDetail{Title: "Go ships", ScoreReason: "Relevant."}
	if _, summary := ogCopy(reason); summary != "Relevant." {
		t.Fatalf("ogCopy(reason) summary = %q", summary)
	}
}
