package hn

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

func newServer(t *testing.T, items map[int]string, top string, topStatus int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/topstories.json" {
			w.WriteHeader(topStatus)
			fmt.Fprint(w, top)
			return
		}
		var id int
		if _, err := fmt.Sscanf(r.URL.Path, "/item/%d.json", &id); err != nil {
			http.NotFound(w, r)
			return
		}
		body, ok := items[id]
		if !ok {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchFiltersAndMaps(t *testing.T) {
	recent := now.Add(-2 * time.Hour).Unix()
	old := now.Add(-72 * time.Hour).Unix()
	items := map[int]string{
		1: fmt.Sprintf(`{"id":1,"type":"story","by":"alice","title":"Go 1.27 released","url":"https://go.dev/blog/go1.27?utm_source=hn","score":250,"time":%d}`, recent),
		2: fmt.Sprintf(`{"id":2,"type":"story","by":"bob","title":"Low score","url":"https://ex.com/low","score":10,"time":%d}`, recent),
		3: fmt.Sprintf(`{"id":3,"type":"story","by":"carol","title":"Too old","url":"https://ex.com/old","score":500,"time":%d}`, old),
		4: fmt.Sprintf(`{"id":4,"type":"story","by":"dave","title":"Ask HN: tips?","text":"<p>Hello &amp; welcome</p>","score":120,"time":%d}`, recent),
		6: fmt.Sprintf(`{"id":6,"type":"job","by":"corp","title":"Hiring","url":"https://ex.com/job","score":300,"time":%d}`, recent),
		// 5 returns HTTP 500: must be skipped without failing the whole fetch.
	}
	srv := newServer(t, items, `[1,2,3,4,5,6]`, http.StatusOK)

	src := New(Params{SourceID: 9, Name: "hn", BaseURL: srv.URL, Client: srv.Client(), Config: Config{MinScore: intp(100), Limit: 30}})
	got, err := src.Fetch(context.Background(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 items, got %d: %+v", len(got), got)
	}
	byID := map[string]int{}
	for i, it := range got {
		byID[it.ExternalID] = i
		if it.SourceID != 9 {
			t.Fatalf("source id not set: %+v", it)
		}
	}
	a := got[byID["1"]]
	if a.URL != "https://go.dev/blog/go1.27?utm_source=hn" || a.Title != "Go 1.27 released" || a.Author != "alice" || !a.PublishedAt.Equal(time.Unix(recent, 0)) {
		t.Fatalf("bad mapping: %+v", a)
	}
	ask := got[byID["4"]]
	if ask.URL != "https://news.ycombinator.com/item?id=4" {
		t.Fatalf("text post must use discussion url, got %q", ask.URL)
	}
	if ask.Content != "Hello & welcome" {
		t.Fatalf("html not stripped: %q", ask.Content)
	}
}

func TestFetchRespectsLimit(t *testing.T) {
	recent := now.Add(-time.Hour).Unix()
	items := map[int]string{}
	for i := 1; i <= 5; i++ {
		items[i] = fmt.Sprintf(`{"id":%d,"type":"story","title":"t%d","url":"https://ex.com/%d","score":200,"time":%d}`, i, i, i, recent)
	}
	srv := newServer(t, items, `[1,2,3,4,5]`, http.StatusOK)
	src := New(Params{Name: "hn", BaseURL: srv.URL, Client: srv.Client(), Config: Config{MinScore: intp(0), Limit: 2}})
	got, err := src.Fetch(context.Background(), now.Add(-24*time.Hour))
	if err != nil || len(got) != 2 {
		t.Fatalf("want 2 items, got %d (err %v)", len(got), err)
	}
}

func TestFetchTopStoriesError(t *testing.T) {
	srv := newServer(t, nil, `nope`, http.StatusServiceUnavailable)
	src := New(Params{Name: "hn", BaseURL: srv.URL, Client: srv.Client()})
	_, err := src.Fetch(context.Background(), now)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("want 503 error, got %v", err)
	}
}

func TestFetchAllItemsFailing(t *testing.T) {
	srv := newServer(t, map[int]string{}, `[1,2]`, http.StatusOK)
	src := New(Params{Name: "hn", BaseURL: srv.URL, Client: srv.Client()})
	if _, err := src.Fetch(context.Background(), now); err == nil {
		t.Fatal("want error when every item request fails")
	}
}

func TestDefaults(t *testing.T) {
	src := New(Params{Name: "hn"})
	if src.minScore != 100 || src.limit != 30 || src.base != DefaultBaseURL {
		t.Fatalf("unexpected defaults: %d %d base=%s", src.minScore, src.limit, src.base)
	}
	if src.Name() != "hn" {
		t.Fatal("name")
	}
}

func intp(v int) *int { return &v }
