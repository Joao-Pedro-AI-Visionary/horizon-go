package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/ports"
	"github.com/jpdev/horizon-go/internal/retry"
)

func vecs(n, dims int) string {
	one := "[" + strings.TrimSuffix(strings.Repeat("0.1,", dims), ",") + "]"
	rows := make([]string, n)
	for i := range rows {
		rows[i] = one
	}
	return fmt.Sprintf(`{"success":true,"errors":[],"messages":[],"result":{"shape":[%d,%d],"data":[%s],"pooling":"cls"}}`, n, dims, strings.Join(rows, ","))
}

func newAt(url string) *Embedder {
	return New(Config{BaseURL: url, AccountID: "acc123", APIToken: "cf-test", Retries: 2})
}

func TestRequestShapeAndBatching(t *testing.T) {
	var calls atomic.Int32
	var sizes []int
	var path, auth string
	var pooling string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		var req runRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		sizes = append(sizes, len(req.Text))
		pooling = req.Pooling
		w.Write([]byte(vecs(len(req.Text), 768)))
	}))
	defer srv.Close()

	texts := make([]string, 150)
	for i := range texts {
		texts[i] = fmt.Sprint("text ", i)
	}
	texts[0] = strings.Repeat("x", 5000) // cut to bge's input size
	vs, err := newAt(srv.URL).Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/accounts/acc123/ai/run/@cf/baai/bge-base-en-v1.5" || auth != "Bearer cf-test" || pooling != "cls" {
		t.Errorf("path=%s auth=%s pooling=%s", path, auth, pooling)
	}
	if calls.Load() != 2 || sizes[0] != 100 || sizes[1] != 50 {
		t.Errorf("batches = %v", sizes)
	}
	if len(vs) != 150 || len(vs[149]) != 768 {
		t.Errorf("got %d vectors", len(vs))
	}
}

func TestRetries429ThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(vecs(1, 768)))
	}))
	defer srv.Close()
	if _, err := newAt(srv.URL).Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d", calls.Load())
	}
}

func TestPersistent429DegradesToUnavailable(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	_, err := newAt(srv.URL).Embed(context.Background(), []string{"a"})
	if !errors.Is(err, ports.ErrEmbeddingUnavailable) {
		t.Fatalf("want ErrEmbeddingUnavailable, got %v", err)
	}
	if calls.Load() != 3 { // first call + 2 retries
		t.Errorf("calls = %d", calls.Load())
	}
}

func TestDailyQuotaBlocksUntilUTCMidnight(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"success":false,"errors":[{"code":4006,"message":"AiError: you have used up your daily free allocation of 10,000 neurons, please upgrade to Cloudflare's Workers Paid plan"}],"result":null}`))
	}))
	defer srv.Close()
	e := newAt(srv.URL)
	e.cfg.Now = func() time.Time { return time.Date(2026, 11, 12, 22, 0, 0, 0, time.UTC) }
	e.cfg.Limiter.Now = e.cfg.Now
	_, err := e.Embed(context.Background(), []string{"a"})
	if !errors.Is(err, ports.ErrEmbeddingUnavailable) || !strings.Contains(err.Error(), "2026-11-13T00:00:00Z") {
		t.Fatalf("err = %v", err)
	}
	// Later calls do not hit the API until the reset.
	if _, err := e.Embed(context.Background(), []string{"b"}); !errors.Is(err, ports.ErrEmbeddingUnavailable) || calls.Load() != 1 {
		t.Errorf("err=%v calls=%d", err, calls.Load())
	}
}

func TestBadTokenIsPermanentAndWrongDims(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
	}))
	defer srv.Close()
	if _, err := newAt(srv.URL).Embed(context.Background(), []string{"a"}); err == nil || !retry.IsPermanent(err) {
		t.Fatalf("401 must be permanent: %v", err)
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(vecs(1, 384)))
	}))
	defer srv2.Close()
	if _, err := newAt(srv2.URL).Embed(context.Background(), []string{"a"}); err == nil || !retry.IsPermanent(err) {
		t.Fatalf("wrong dims must be permanent: %v", err)
	}
}
