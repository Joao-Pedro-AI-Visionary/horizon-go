package httpapi

import (
	"bytes"
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
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("item html missing %q:\n%s", want, body)
		}
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
}
