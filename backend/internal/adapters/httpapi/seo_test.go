package httpapi

import (
	"bytes"
	"encoding/json"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/jpdev/horizon-go/internal/domain"
)

func TestSEOEndpointsRSSSitemapOGImageAndHTML(t *testing.T) {
	s, _, srv := newTestServer(t)
	s.FrontURL = "https://horizon-web-eight.vercel.app"

	// robots.txt
	resp, body := do(t, http.MethodGet, srv.URL+"/robots.txt", "")
	if resp.StatusCode != 200 || !strings.Contains(body, "Sitemap: https://horizon-web-eight.vercel.app/sitemap.xml") {
		t.Fatalf("robots.txt: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Content-Signal: search=yes, ai-input=yes, ai-train=yes") {
		t.Fatalf("robots.txt missing Content-Signal: %s", body)
	}
	for _, bot := range []string{"OAI-SearchBot", "ChatGPT-User", "GPTBot", "Claude-SearchBot", "Claude-User", "ClaudeBot", "PerplexityBot", "Perplexity-User", "Google-Extended", "Applebot-Extended"} {
		if !strings.Contains(body, "User-agent: "+bot) {
			t.Fatalf("robots.txt missing bot %s: %s", bot, body)
		}
	}
	for _, path := range []string{"/login", "/sources", "/forgot", "/reset", "/api/admin", "/api/pipeline", "/api/newsletter/send", "/api/items/ingest"} {
		if !strings.Contains(body, "Disallow: "+path) {
			t.Fatalf("robots.txt missing Disallow %s: %s", path, body)
		}
	}

	// llms.txt
	resp, body = do(t, http.MethodGet, srv.URL+"/llms.txt", "")
	if resp.StatusCode != 200 || !strings.HasPrefix(body, "# Radar by JpDev") || !strings.Contains(body, "https://horizon-web-eight.vercel.app/sitemap.xml") || !strings.Contains(body, "/llms-full.txt") {
		t.Fatalf("llms.txt: %d %s", resp.StatusCode, body)
	}

	// llms-full.txt includes the static index plus latest feed items
	resp, body = do(t, http.MethodGet, srv.URL+"/llms-full.txt", "")
	if resp.StatusCode != 200 || !strings.Contains(body, "## Últimas notícias") {
		t.Fatalf("llms-full.txt: %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, http.MethodGet, srv.URL+"/api/llms-full.txt", "")
	if resp.StatusCode != 200 || !strings.Contains(body, "# Radar by JpDev") {
		t.Fatalf("api llms-full.txt: %d %s", resp.StatusCode, body)
	}

	// sitemap.xml
	resp, body = do(t, http.MethodGet, srv.URL+"/sitemap.xml", "")
	if resp.StatusCode != 200 || !strings.Contains(body, "<urlset") || !strings.Contains(body, "https://horizon-web-eight.vercel.app/") {
		t.Fatalf("sitemap.xml: %d %s", resp.StatusCode, body)
	}

	// RSS feed & digests
	resp, body = do(t, http.MethodGet, srv.URL+"/rss.xml", "")
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "rss+xml") || !strings.Contains(body, "<rss version=\"2.0\"") {
		t.Fatalf("rss.xml: %d ct=%s %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	resp, body = do(t, http.MethodGet, srv.URL+"/digests/rss.xml", "")
	if resp.StatusCode != 200 || !strings.Contains(body, "Radar by JpDev — Digests") {
		t.Fatalf("digests rss: %d %s", resp.StatusCode, body)
	}

	// Public item HTML with canonical, description, OG and Twitter tags
	resp, body = do(t, http.MethodGet, srv.URL+"/items/1", "")
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("item html: %d ct=%s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, want := range []string{
		`<title>one · Radar by JpDev</title>`,
		`rel="canonical" href="https://horizon-web-eight.vercel.app/items/1"`,
		`property="og:title" content="one"`,
		`name="twitter:card" content="summary_large_image"`,
		`/api/og/items/1.png`,
		`application/ld+json`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("item html missing %q:\n%s", want, body)
		}
	}
	ldStart := strings.Index(body, `<script type="application/ld+json">`)
	if ldStart < 0 {
		t.Fatalf("item html missing JSON-LD script:\n%s", body)
	}
	ldRaw := body[ldStart+len(`<script type="application/ld+json">`):]
	ldEnd := strings.Index(ldRaw, `</script>`)
	if ldEnd < 0 {
		t.Fatalf("item html JSON-LD is unclosed:\n%s", body)
	}
	var ld map[string]any
	if err := json.Unmarshal([]byte(ldRaw[:ldEnd]), &ld); err != nil {
		t.Fatalf("item html JSON-LD: %v\n%s", err, ldRaw[:ldEnd])
	}
	if ld["@type"] != "NewsArticle" || ld["inLanguage"] != "pt-BR" || ld["headline"] != "one" {
		t.Fatalf("unexpected NewsArticle JSON-LD: %#v", ld)
	}
	// Seeded test items may have zero timestamps; when dates exist they must be RFC3339.
	for _, key := range []string{"datePublished", "dateModified"} {
		if raw, ok := ld[key]; ok {
			s, _ := raw.(string)
			if s == "" || !(len(s) >= 20) {
				t.Fatalf("JSON-LD %s is not RFC3339: %#v", key, raw)
			}
		}
	}
	if pub, ok := ld["publisher"].(map[string]any); !ok || pub["name"] != "Radar by JpDev" || pub["alternateName"] != "Horizon by JpDev" {
		t.Fatalf("JSON-LD publisher: %#v", ld["publisher"])
	}

	// Dynamic OG PNG endpoint decodes cleanly to 1200x630
	resp, body = do(t, http.MethodGet, srv.URL+"/api/og/items/1.png", "")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("og png: %d ct=%s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	img, err := png.Decode(bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("decode og png: %v", err)
	}
	if img.Bounds().Dx() != ogWidth || img.Bounds().Dy() != ogHeight {
		t.Fatalf("unexpected og dims: %v", img.Bounds())
	}

	// Verify RenderItemOG renders title wrapping & real avatar without error on long scored item
	sc := 8.7
	pngBytes, err := RenderItemOG(domain.ItemDetail{
		ID:         42,
		SourceName: "Hacker News",
		URL:        "https://go.dev/blog/hello",
		Title:      "A very long engineering headline that wraps cleanly across multiple lines inside the OpenGraph social preview card",
		Summary:    "Concise editorial summary explaining why this matters.",
		Score:      &sc,
	})
	if err != nil || len(pngBytes) < 1000 {
		t.Fatalf("render long item og failed: err=%v len=%d", err, len(pngBytes))
	}

	// Regression: single unbroken token exceeding max width must terminate and add ellipsis.
	unbroken := "https://example.com/" + strings.Repeat("superlongsegment", 20)
	wrapped := wrapLines(nil, unbroken, 900, 3)
	if len(wrapped) != 1 || !strings.HasSuffix(wrapped[0], "…") {
		t.Fatalf("expected truncated unbroken token with ellipsis, got %#v", wrapped)
	}
	multi := wrapLines(nil, "word1 word2 word3 "+unbroken, 200, 2)
	if len(multi) != 2 || !strings.HasSuffix(multi[1], "…") {
		t.Fatalf("expected multi-line truncation with ellipsis, got %#v", multi)
	}
	if _, err := RenderItemOG(domain.ItemDetail{
		ID:      99,
		Title:   unbroken,
		Summary: unbroken,
		Score:   &sc,
	}); err != nil {
		t.Fatalf("RenderItemOG on unbroken long token failed: %v", err)
	}
}
