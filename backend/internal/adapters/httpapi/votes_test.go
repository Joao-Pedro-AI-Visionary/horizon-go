package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/domain"
)

type fakeVoteStore struct {
	mu       sync.Mutex
	votes    map[string]int // key: deviceHash
	rateHits int
	maxRate  int
}

func (f *fakeVoteStore) CheckAndRecordRate(_ context.Context, _ string, window time.Duration, maxVotes int, _ time.Duration, _ time.Time) (time.Duration, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.maxRate > 0 {
		maxVotes = f.maxRate
	}
	if f.rateHits >= maxVotes {
		return window, false, nil
	}
	f.rateHits++
	return 0, true, nil
}

func (f *fakeVoteStore) Vote(_ context.Context, itemID int64, deviceHash string, vote int) (domain.VoteResult, error) {
	if itemID != 1 {
		return domain.VoteResult{}, postgres.ErrNotFound
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.votes == nil {
		f.votes = map[string]int{}
	}
	if vote == 0 {
		delete(f.votes, deviceHash)
	} else {
		f.votes[deviceHash] = vote
	}
	likes := 0
	for _, v := range f.votes {
		if v == 1 {
			likes++
		}
	}
	return domain.VoteResult{ItemID: itemID, Likes: likes, UserVote: vote}, nil
}

func (f *fakeVoteStore) UserVote(_ context.Context, itemID int64, deviceHash string) (int, error) {
	if itemID != 1 {
		return 0, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.votes[deviceHash], nil
}

func TestVotePublicWriteAndValidations(t *testing.T) {
	s, _, srv := newTestServer(t)
	vs := &fakeVoteStore{}
	s.Votes = vs
	s.VoteHashSecret = []byte("0123456789abcdef0123456789abcdef")
	s.RequireAdmin = true
	s.AdminToken = "secret-admin-token"

	// Private ingest must remain 401 without admin token.
	resp, _ := do(t, http.MethodPost, srv.URL+"/api/items/ingest", `{"items":[]}`)
	if resp.StatusCode != 401 {
		t.Fatalf("ingest without auth: got %d want 401", resp.StatusCode)
	}

	// Voting before dwell >= 3s is rejected (400).
	resp, body := do(t, http.MethodPost, srv.URL+"/api/items/1/vote", `{"vote":1,"dwell_ms":1500}`, "X-Device-Id", "dev_1234567890abcdef")
	if resp.StatusCode != 400 || !strings.Contains(body, "leia um pouco") {
		t.Fatalf("short dwell: got %d %s", resp.StatusCode, body)
	}

	// Valid vote succeeds publicly and reflects in UserVote + Likes (dislikes stay hidden).
	resp, body = do(t, http.MethodPost, srv.URL+"/api/items/1/vote", `{"vote":1,"dwell_ms":3500}`, "X-Device-Id", "dev_1234567890abcdef")
	if resp.StatusCode != 200 {
		t.Fatalf("like vote: got %d %s", resp.StatusCode, body)
	}
	var vr domain.VoteResult
	if err := json.Unmarshal([]byte(body), &vr); err != nil || vr.Likes != 1 || vr.UserVote != 1 {
		t.Fatalf("unexpected vote result: %s (%v)", body, err)
	}

	// Switching to dislike hides dislikes (likes drops to 0, no dislikes field exposed).
	resp, body = do(t, http.MethodPost, srv.URL+"/api/items/1/vote", `{"vote":-1,"dwell_ms":4000}`, "X-Device-Id", "dev_1234567890abcdef")
	if resp.StatusCode != 200 {
		t.Fatalf("dislike vote: got %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "dislike") {
		t.Fatalf("dislikes must stay hidden in JSON: %s", body)
	}
	if err := json.Unmarshal([]byte(body), &vr); err != nil || vr.Likes != 0 || vr.UserVote != -1 {
		t.Fatalf("unexpected switch result: %+v", vr)
	}

	// GET /api/items/1 returns UserVote for caller device.
	resp, body = do(t, http.MethodGet, srv.URL+"/api/items/1", "", "X-Device-Id", "dev_1234567890abcdef")
	if resp.StatusCode != 200 {
		t.Fatalf("get item: %d %s", resp.StatusCode, body)
	}
	var it domain.ItemDetail
	if err := json.Unmarshal([]byte(body), &it); err != nil || it.UserVote != -1 {
		t.Fatalf("item user_vote mismatch: %+v (%v)", it, err)
	}

	// Clearing vote works even with 0 dwell.
	resp, body = do(t, http.MethodPost, srv.URL+"/api/items/1/vote", `{"vote":0,"dwell_ms":0}`, "X-Device-Id", "dev_1234567890abcdef")
	if resp.StatusCode != 200 {
		t.Fatalf("clear vote: %d %s", resp.StatusCode, body)
	}
}

func TestVoteRateLimitAndCookieAttrs(t *testing.T) {
	s, _, srv := newTestServer(t)
	s.Votes = &fakeVoteStore{maxRate: 1}
	s.VoteHashSecret = []byte("0123456789abcdef0123456789abcdef")

	// Simulate HTTPS behind Render proxy -> SameSite=None; Secure; HttpOnly.
	resp, body := do(t, http.MethodPost, srv.URL+"/api/items/1/vote", `{"vote":1,"dwell_ms":3500}`,
		"X-Forwarded-Proto", "https", "Origin", "http://localhost:5173")
	if resp.StatusCode != 200 {
		t.Fatalf("first vote: %d %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Access-Control-Allow-Credentials = %q", got)
	}
	var didCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == deviceCookieName {
			didCookie = c
		}
	}
	if didCookie == nil || !didCookie.HttpOnly || !didCookie.Secure || didCookie.SameSite != http.SameSiteNoneMode {
		t.Fatalf("HTTPS cookie attributes wrong: %+v", didCookie)
	}

	// Second vote hits fake maxRate=1 -> 429 with Retry-After.
	resp, body = do(t, http.MethodPost, srv.URL+"/api/items/1/vote", `{"vote":1,"dwell_ms":3500}`)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("rate limit expected 429 + Retry-After, got %d hdr=%q body=%s", resp.StatusCode, resp.Header.Get("Retry-After"), body)
	}

	// Trending query parsing accepted cleanly.
	q, err := parseFeedQuery(url.Values{"sort": {"trending"}}, time.Now())
	if err != nil || q.Sort != domain.SortTrending {
		t.Fatalf("sort=trending parse failed: %+v %v", q, err)
	}

	// CORS preflight allows X-Device-Id + credentials.
	resp, _ = do(t, http.MethodOptions, srv.URL+"/api/items/1/vote", "",
		"Origin", "http://localhost:5173", "Access-Control-Request-Method", "POST")
	if resp.StatusCode != 204 || !strings.Contains(resp.Header.Get("Access-Control-Allow-Headers"), "X-Device-Id") {
		t.Fatalf("preflight headers: %d %v", resp.StatusCode, resp.Header)
	}
}
