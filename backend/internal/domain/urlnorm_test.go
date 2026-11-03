package domain

import (
	"errors"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"strips utm params", "https://example.com/post?utm_source=hn&utm_medium=x&id=7", "https://example.com/post?id=7"},
		{"strips tracking params", "https://example.com/a?fbclid=1&gclid=2&ref=ok", "https://example.com/a?ref=ok"},
		{"utm is case-insensitive", "https://example.com/a?UTM_Campaign=z", "https://example.com/a"},
		{"strips fragment", "https://example.com/a#section-2", "https://example.com/a"},
		{"strips www", "https://www.example.com/a", "https://example.com/a"},
		{"strips trailing slash", "https://example.com/a/b/", "https://example.com/a/b"},
		{"root path", "https://www.example.com/", "https://example.com"},
		{"lowercases scheme and host", "HTTPS://Example.COM/CasePath", "https://example.com/CasePath"},
		{"drops default port", "https://example.com:443/a", "https://example.com/a"},
		{"keeps non default port", "http://127.0.0.1:8080/a/", "http://127.0.0.1:8080/a"},
		{"keeps query order", "https://news.ycombinator.com/item?id=42&p=2", "https://news.ycombinator.com/item?id=42&p=2"},
		{"trims spaces", "  https://example.com/a  ", "https://example.com/a"},
		{"all combined", "https://WWW.Go.dev/blog/x/?utm_source=feed#top", "https://go.dev/blog/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeURL(tc.in)
			if err != nil {
				t.Fatalf("NormalizeURL(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeURLRejectsInvalid(t *testing.T) {
	for _, in := range []string{"", "not a url", "ftp://example.com/a", "mailto:a@b.c", "https:///nohost"} {
		if _, err := NormalizeURL(in); !errors.Is(err, ErrInvalidURL) {
			t.Fatalf("NormalizeURL(%q) err = %v, want ErrInvalidURL", in, err)
		}
	}
}

func TestURLHashStableAcrossVariants(t *testing.T) {
	a, _ := NormalizeURL("https://www.example.com/a/?utm_source=x#f")
	b, _ := NormalizeURL("https://example.com/a")
	if URLHash(a) != URLHash(b) {
		t.Fatalf("hash differs for equivalent URLs: %s vs %s", a, b)
	}
	if len(URLHash(a)) != 64 {
		t.Fatalf("expected sha256 hex (64 chars), got %d", len(URLHash(a)))
	}
	if URLHash(a) == URLHash("https://example.com/b") {
		t.Fatal("different URLs must not share a hash")
	}
}

func TestPrepareItem(t *testing.T) {
	it := Item{URL: "https://www.example.com/x/?utm_source=a"}
	if err := it.Prepare(); err != nil {
		t.Fatal(err)
	}
	if it.URLNormalized != "https://example.com/x" || it.URLHash != URLHash("https://example.com/x") {
		t.Fatalf("unexpected prepared item: %+v", it)
	}
}
