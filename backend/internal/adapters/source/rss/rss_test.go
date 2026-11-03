package rss

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

const atomFeed = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>The Go Blog</title>
  <id>tag:blog.golang.org,2013:blog.golang.org</id>
  <updated>2026-11-01T00:00:00Z</updated>
  <entry>
    <title>Go 1.27 is released</title>
    <id>tag:blog.golang.org,2013:blog.golang.org/go1.27</id>
    <link rel="alternate" href="https://go.dev/blog/go1.27"></link>
    <published>2026-11-01T10:00:00Z</published>
    <updated>2026-11-01T10:00:00Z</updated>
    <author><name>The Go Team</name></author>
    <summary type="html">&lt;p&gt;Today we release Go 1.27.&lt;/p&gt;</summary>
  </entry>
  <entry>
    <title>Old post</title>
    <id>tag:blog.golang.org,2013:blog.golang.org/old</id>
    <link rel="alternate" href="https://go.dev/blog/old"></link>
    <published>2020-01-01T10:00:00Z</published>
    <updated>2020-01-01T10:00:00Z</updated>
  </entry>
  <entry>
    <title>Updated only</title>
    <id>tag:x,2026:updated-only</id>
    <link rel="alternate" href="https://go.dev/blog/updated-only"></link>
    <updated>2026-11-02T08:00:00Z</updated>
  </entry>
</feed>`

const rss2Feed = `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Example</title><link>https://ex.com</link>
<item><title>No guid</title><link>https://ex.com/a</link><pubDate>Mon, 02 Nov 2026 09:00:00 +0000</pubDate><description>Plain &amp; simple</description></item>
<item><title>No link</title><guid>abc</guid><pubDate>Mon, 02 Nov 2026 09:00:00 +0000</pubDate></item>
<item><title>No date</title><link>https://ex.com/nodate</link></item>
</channel></rss>`

func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.UserAgent(), "horizon-go") {
			http.Error(w, "missing ua", http.StatusBadRequest)
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchAtom(t *testing.T) {
	srv := serve(t, http.StatusOK, atomFeed)
	src := New(Params{SourceID: 5, Name: "go-blog", Client: srv.Client(), Config: Config{URL: srv.URL}})
	items, err := src.Fetch(context.Background(), now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d: %+v", len(items), items)
	}
	a := items[0]
	if a.Title != "Go 1.27 is released" || a.URL != "https://go.dev/blog/go1.27" || a.SourceID != 5 {
		t.Fatalf("mapping: %+v", a)
	}
	if a.ExternalID != "tag:blog.golang.org,2013:blog.golang.org/go1.27" || a.Author != "The Go Team" {
		t.Fatalf("id/author: %+v", a)
	}
	if a.Content != "Today we release Go 1.27." {
		t.Fatalf("content: %q", a.Content)
	}
	if !a.PublishedAt.Equal(time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("published: %v", a.PublishedAt)
	}
	if items[1].Title != "Updated only" {
		t.Fatalf("entry with only <updated> must use it as date: %+v", items[1])
	}
}

func TestFetchRSS2(t *testing.T) {
	srv := serve(t, http.StatusOK, rss2Feed)
	src := New(Params{Name: "ex", Client: srv.Client(), Config: Config{URL: srv.URL}})
	items, err := src.Fetch(context.Background(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want only the item with link+date, got %d: %+v", len(items), items)
	}
	if items[0].ExternalID != "https://ex.com/a" || items[0].Content != "Plain & simple" {
		t.Fatalf("guid fallback / content: %+v", items[0])
	}
}

func TestFetchHTTPError(t *testing.T) {
	srv := serve(t, http.StatusNotFound, "")
	src := New(Params{Name: "ex", Client: srv.Client(), Config: Config{URL: srv.URL}})
	if _, err := src.Fetch(context.Background(), now); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want 404 error, got %v", err)
	}
}

func TestFetchInvalidXML(t *testing.T) {
	srv := serve(t, http.StatusOK, "<html>not a feed")
	src := New(Params{Name: "ex", Client: srv.Client(), Config: Config{URL: srv.URL}})
	if _, err := src.Fetch(context.Background(), now); err == nil {
		t.Fatal("want parse error")
	}
}

func TestConfigValidate(t *testing.T) {
	for _, u := range []string{"", "ftp://x/feed", "not a url"} {
		if err := (Config{URL: u}).Validate(); err == nil {
			t.Fatalf("URL %q must be invalid", u)
		}
	}
	if err := (Config{URL: "https://go.dev/blog/feed.atom"}).Validate(); err != nil {
		t.Fatal(err)
	}
}
