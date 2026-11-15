package httpapi

import (
	"strings"
	"testing"
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
