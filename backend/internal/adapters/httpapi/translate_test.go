package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/jpdev/horizon-go/internal/domain"
)

type fakeTranslations struct {
	requests []int
	status   domain.TranslationStatus
	err      error
}

func (f *fakeTranslations) TranslationStatus(context.Context) (domain.TranslationStatus, error) {
	return f.status, nil
}

func (f *fakeTranslations) RequestTranslation(_ context.Context, limit int) error {
	if f.err != nil {
		return f.err
	}
	f.requests = append(f.requests, limit)
	return nil
}

func TestTranslateEndpointQueuesAndReportsStatus(t *testing.T) {
	tr := &fakeTranslations{status: domain.TranslationStatus{Remaining: 116, Translated: 3, Copied: 2, Failed: 1}}
	s := &Server{Store: &fakeStore{}, Translations: tr, AdminToken: "secret", RequireAdmin: true}
	h := s.Handler()

	for _, c := range []struct {
		body      string
		wantLimit int
	}{{"", 0}, {"  ", 0}, {`{}`, 0}, {`{"limit":20}`, 20}, {`{"limit":500}`, 500}} {
		rec := doAuth(h, http.MethodPost, "/api/pipeline/translate", "Bearer secret", c.body)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("body %q: got %d %s", c.body, rec.Code, rec.Body.String())
		}
		var out struct {
			domain.TranslationStatus
			Queued bool `json:"queued"`
			Limit  int  `json:"limit"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if !out.Queued || out.Limit != c.wantLimit || out.TranslationStatus != tr.status {
			t.Fatalf("body %q: response %+v", c.body, out)
		}
		if got := tr.requests[len(tr.requests)-1]; got != c.wantLimit {
			t.Fatalf("body %q: queued limit %d", c.body, got)
		}
	}
	n := len(tr.requests)
	for _, body := range []string{`{"limit":-1}`, `{"limit":501}`, `{"limit":"x"}`, `{"max":3}`, `[`} {
		if rec := doAuth(h, http.MethodPost, "/api/pipeline/translate", "Bearer secret", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: got %d want 400", body, rec.Code)
		}
	}
	if len(tr.requests) != n {
		t.Fatal("an invalid request was queued")
	}

	rec := doAuth(h, http.MethodGet, "/api/pipeline/translate", "Bearer secret", "")
	var st domain.TranslationStatus
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil || st != tr.status {
		t.Fatalf("GET status: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTranslateEndpointRequiresAdmin(t *testing.T) {
	tr := &fakeTranslations{}
	s := &Server{Store: &fakeStore{}, Translations: tr, AdminToken: "secret", RequireAdmin: true}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, auth := range []string{"", "Bearer wrong", "Basic secret"} {
			if rec := doAuth(s.Handler(), method, "/api/pipeline/translate", auth, ""); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s auth=%q: got %d want 401", method, auth, rec.Code)
			}
		}
	}
	if len(tr.requests) != 0 {
		t.Fatal("unauthorized request was queued")
	}
}

func TestTranslateEndpointNotConfiguredAndQueueError(t *testing.T) {
	h := (&Server{Store: &fakeStore{}, AdminToken: "secret", RequireAdmin: true}).Handler()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if rec := doAuth(h, method, "/api/pipeline/translate", "Bearer secret", ""); rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: got %d want 501", method, rec.Code)
		}
	}
	tr := &fakeTranslations{err: errors.New("nats down")}
	h = (&Server{Store: &fakeStore{}, Translations: tr, AdminToken: "secret", RequireAdmin: true}).Handler()
	if rec := doAuth(h, http.MethodPost, "/api/pipeline/translate", "Bearer secret", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("queue error: got %d", rec.Code)
	}
}
