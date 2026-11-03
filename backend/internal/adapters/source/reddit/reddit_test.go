package reddit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func listingJSON(recent, old int64) string {
	return fmt.Sprintf(`{"kind":"Listing","data":{"children":[
	 {"kind":"t3","data":{"id":"aaa","title":"Link post","url":"https://blog.example.com/x?utm_source=reddit","permalink":"/r/golang/comments/aaa/link_post/","author":"gopher","created_utc":%d,"score":42,"is_self":false,"selftext":"","subreddit":"golang"}},
	 {"kind":"t3","data":{"id":"bbb","title":"Self post","url":"https://www.reddit.com/r/golang/comments/bbb/self_post/","permalink":"/r/golang/comments/bbb/self_post/","author":"asker","created_utc":%d,"score":15,"is_self":true,"selftext":"How do I &lt;b&gt;x&lt;/b&gt;?","subreddit":"golang"}},
	 {"kind":"t3","data":{"id":"ccc","title":"Low","url":"https://ex.com/low","permalink":"/r/golang/comments/ccc/low/","created_utc":%d,"score":1,"is_self":false}},
	 {"kind":"t3","data":{"id":"ddd","title":"Old","url":"https://ex.com/old","permalink":"/r/golang/comments/ddd/old/","created_utc":%d,"score":900,"is_self":false}},
	 {"kind":"t1","data":{"id":"eee","body":"a comment, not a post"}}
	]}}`, recent, recent, recent, old)
}

func TestFetchMapsPostsAndSendsUserAgent(t *testing.T) {
	var gotUA, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotPath, gotQuery = r.UserAgent(), r.URL.Path, r.URL.RawQuery
		fmt.Fprint(w, listingJSON(now.Add(-time.Hour).Unix(), now.Add(-96*time.Hour).Unix()))
	}))
	defer srv.Close()

	src := New(Params{SourceID: 3, Name: "r/golang", BaseURL: srv.URL, Client: srv.Client(),
		Config: Config{Subreddit: "golang", Sort: "hot", Limit: 25, MinScore: 5}})
	items, err := src.Fetch(context.Background(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/r/golang/hot.json" {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "limit=25") || !strings.Contains(gotQuery, "raw_json=1") {
		t.Fatalf("query = %q", gotQuery)
	}
	if gotUA != UserAgent || !strings.Contains(gotUA, "horizon-go") {
		t.Fatalf("user agent = %q", gotUA)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d: %+v", len(items), items)
	}
	link, self := items[0], items[1]
	if link.ExternalID != "aaa" || link.URL != "https://blog.example.com/x?utm_source=reddit" || link.Author != "gopher" || link.SourceID != 3 {
		t.Fatalf("link post mapping: %+v", link)
	}
	if self.URL != "https://www.reddit.com/r/golang/comments/bbb/self_post/" {
		t.Fatalf("self post must use discussion url: %q", self.URL)
	}
	if self.Content != "How do I x ?" {
		t.Fatalf("selftext not cleaned: %q", self.Content)
	}
}

func TestTopSortSendsTimeFilter(t *testing.T) {
	var q string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		fmt.Fprint(w, `{"data":{"children":[]}}`)
	}))
	defer srv.Close()
	src := New(Params{Name: "r/xx", BaseURL: srv.URL, Client: srv.Client(), Config: Config{Subreddit: "xx", Sort: "top"}})
	if _, err := src.Fetch(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, "t=day") {
		t.Fatalf("query = %q", q)
	}
}

func TestRetriesOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, listingJSON(now.Add(-time.Hour).Unix(), now.Add(-time.Hour).Unix()))
	}))
	defer srv.Close()
	src := New(Params{Name: "r/golang", BaseURL: srv.URL, Client: srv.Client(), Config: Config{Subreddit: "golang"}})
	items, err := src.Fetch(context.Background(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Fetch after 429: %v", err)
	}
	if calls.Load() != 2 || len(items) == 0 {
		t.Fatalf("calls=%d items=%d", calls.Load(), len(items))
	}
}

func TestPersistent429ReturnsRateLimited(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	src := New(Params{Name: "r/golang", BaseURL: srv.URL, Client: srv.Client(), Config: Config{Subreddit: "golang"}})
	_, err := src.Fetch(context.Background(), now)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
	if calls.Load() != int32(maxAttempts) {
		t.Fatalf("calls = %d, want %d", calls.Load(), maxAttempts)
	}
}

func TestRetryAfterWaitIsCapped(t *testing.T) {
	if d := retryAfter("3600", 2*time.Second); d != 2*time.Second {
		t.Fatalf("cap not applied: %v", d)
	}
	if d := retryAfter("", 5*time.Second); d != defaultRetryAfter {
		t.Fatalf("missing header: %v", d)
	}
	if d := retryAfter("1", 5*time.Second); d != time.Second {
		t.Fatalf("seconds: %v", d)
	}
}

func TestForbiddenIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	src := New(Params{Name: "r/golang", BaseURL: srv.URL, Client: srv.Client(), Config: Config{Subreddit: "golang"}})
	if _, err := src.Fetch(context.Background(), now); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("want 403 error, got %v", err)
	}
}

func TestInvalidConfig(t *testing.T) {
	if err := (Config{}).Validate(); err == nil {
		t.Fatal("empty subreddit must be invalid")
	}
	if err := (Config{Subreddit: "golang", Sort: "weird"}).Validate(); err == nil {
		t.Fatal("unknown sort must be invalid")
	}
	if err := (Config{Subreddit: "../etc"}).Validate(); err == nil {
		t.Fatal("subreddit with path chars must be invalid")
	}
}
