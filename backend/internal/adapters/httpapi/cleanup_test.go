package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jpdev/horizon-go/internal/retention"
)

type fakeCleanup struct {
	calls  []bool
	report retention.Report
}

func (f *fakeCleanup) Cleanup(_ context.Context, dryRun bool) (retention.Report, error) {
	f.calls = append(f.calls, dryRun)
	return f.report, nil
}

func TestCleanupEndpointDefaultsToDryRunAndAcceptsExplicitMode(t *testing.T) {
	cleaner := &fakeCleanup{report: retention.Report{DryRun: true, RetentionDays: 32, Timezone: retention.Timezone, Counts: retention.Counts{"items": 4}}}
	s := &Server{Store: &fakeStore{}, Cleanup: cleaner, AdminToken: "secret", RequireAdmin: true}
	h := s.Handler()

	for _, tc := range []struct {
		path    string
		wantDry bool
	}{
		{path: "/api/pipeline/cleanup", wantDry: true},
		{path: "/api/pipeline/cleanup?dry_run=true", wantDry: true},
		{path: "/api/pipeline/cleanup?dry_run=false", wantDry: false},
	} {
		rec := doAuth(h, http.MethodPost, tc.path, "Bearer secret", "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":4`) {
			t.Fatalf("%s: got %d %s", tc.path, rec.Code, rec.Body.String())
		}
		if got := cleaner.calls[len(cleaner.calls)-1]; got != tc.wantDry {
			t.Fatalf("%s: dry_run=%v want %v", tc.path, got, tc.wantDry)
		}
	}
	if rec := doAuth(h, http.MethodPost, "/api/pipeline/cleanup?dry_run=maybe", "Bearer secret", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid dry_run: got %d", rec.Code)
	}
}

func TestCleanupEndpointRequiresAdminToken(t *testing.T) {
	s := &Server{Store: &fakeStore{}, Cleanup: &fakeCleanup{}, AdminToken: "secret", RequireAdmin: true}
	for _, auth := range []string{"", "Bearer wrong", "Basic secret"} {
		if rec := doAuth(s.Handler(), http.MethodPost, "/api/pipeline/cleanup?dry_run=true", auth, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("auth=%q: got %d want 401", auth, rec.Code)
		}
	}
}
