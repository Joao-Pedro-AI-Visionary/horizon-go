package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/opsalerts"
)

type fakeAlerts struct {
	list  []domain.Alert
	since time.Time
	stale time.Duration
}

func (f *fakeAlerts) Alerts(_ context.Context, since, _ time.Time, stale time.Duration) ([]domain.Alert, error) {
	f.since, f.stale = since, stale
	return append([]domain.Alert(nil), f.list...), nil
}

func id(n int64) *int64 { return &n }

func alertsServer(t *testing.T, fa *fakeAlerts) http.Handler {
	now := time.Now()
	s := &Server{
		Store:        &fakeStore{},
		Sources:      &fakeSources{},
		Collector:    &countingCollector{},
		AdminToken:   "static-token",
		RequireAdmin: true,
		Login:        testLogin(t, &now),
		Alerts:       fa,
	}
	return s.Handler()
}

func TestAdminAlertsNeedsAdmin(t *testing.T) {
	h := alertsServer(t, &fakeAlerts{})
	for _, auth := range []string{"", "Bearer wrong", "Basic x"} {
		if rec := doAuth(h, http.MethodGet, "/api/admin/alerts", auth, ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("auth %q: %d %s", auth, rec.Code, rec.Body.String())
		}
	}
	if rec := doAuth(h, http.MethodGet, "/api/admin/alerts", "Bearer static-token", ""); rec.Code != http.StatusOK {
		t.Fatalf("admin token: %d %s", rec.Code, rec.Body.String())
	}
	rec := doAuth(h, http.MethodPost, "/api/auth/login", "", loginBody("joao", testPassword))
	var out struct{ Token string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Token == "" {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doAuth(h, http.MethodGet, "/api/admin/alerts", "Bearer "+out.Token, ""); rec.Code != http.StatusOK {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	// Other reads stay public.
	if rec := doAuth(h, http.MethodGet, "/api/sources", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("public read: %d", rec.Code)
	}
}

func TestAdminAlertsNewestFirstDedupedPerSource(t *testing.T) {
	t0 := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	fa := &fakeAlerts{list: []domain.Alert{
		{Kind: domain.AlertSource, SourceID: id(7), Name: "r/ClaudeAI", Message: "old 429", At: t0.Add(-2 * time.Hour)},
		{Kind: domain.AlertDigest, Name: "Digest", Message: "db down", At: t0.Add(-time.Hour)},
		{Kind: domain.AlertSource, SourceID: id(7), Name: "r/ClaudeAI", Message: "new 429", At: t0},
		{Kind: domain.AlertScoring, SourceID: id(7), Name: "r/ClaudeAI", Message: "2 itens sem nota", At: t0.Add(-30 * time.Minute)},
		{Kind: domain.AlertCollection, Name: "Coleta", Message: "sem coleta", At: t0.Add(-15 * time.Hour)},
	}}
	h := alertsServer(t, fa)
	rec := doAuth(h, http.MethodGet, "/api/admin/alerts?hours=24", "Bearer static-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Count  int            `json:"count"`
		Alerts []domain.Alert `json:"alerts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := []string{"new 429", "2 itens sem nota", "db down", "sem coleta"}
	if out.Count != len(want) || len(out.Alerts) != len(want) {
		t.Fatalf("got %+v", out)
	}
	for i, m := range want {
		if out.Alerts[i].Message != m {
			t.Fatalf("alert %d = %q, want %q (%+v)", i, out.Alerts[i].Message, m, out.Alerts)
		}
	}
	if d := time.Since(fa.since); d < 23*time.Hour || d > 25*time.Hour {
		t.Errorf("since %v ago, want 24h", d)
	}
	if fa.stale != DefaultAlertStale {
		t.Errorf("stale %v", fa.stale)
	}
	if rec := doAuth(h, http.MethodGet, "/api/admin/alerts?hours=0", "Bearer static-token", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("hours=0: %d", rec.Code)
	}
}

type fakeAlertTester struct {
	res opsalerts.NotifyResult
}

func (f *fakeAlertTester) SendTest(context.Context) opsalerts.NotifyResult {
	return f.res
}

func TestAdminAlertTestAuthAndDisabled(t *testing.T) {
	h := alertsServer(t, &fakeAlerts{})
	for _, auth := range []string{"", "Bearer wrong", "Basic x"} {
		if rec := doAuth(h, http.MethodPost, "/api/admin/alerts/test", auth, ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("auth %q: %d %s", auth, rec.Code, rec.Body.String())
		}
	}
	if rec := doAuth(h, http.MethodPost, "/api/admin/alerts/test", "Bearer static-token", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled: got %d %s, want 404", rec.Code, rec.Body.String())
	}
}

func TestAdminAlertTestResponses(t *testing.T) {
	now := time.Now()
	t0 := time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)

	sampleKey := "re_" + "testkey0001"
	tests := []struct {
		name           string
		res            opsalerts.NotifyResult
		wantStatus     int
		wantEmailed    bool
		wantSuppressed bool
		wantReason     string
		wantRedacted   string
	}{
		{
			name:        "sent",
			res:         opsalerts.NotifyResult{Kind: domain.AlertTest, At: t0, Recorded: true, Emailed: true, MessageID: "msg_123"},
			wantStatus:  http.StatusOK,
			wantEmailed: true,
		},
		{
			name:           "cooldown suppressed",
			res:            opsalerts.NotifyResult{Kind: domain.AlertTest, At: t0, Recorded: true, Suppressed: true, Reason: "cooldown"},
			wantStatus:     http.StatusOK,
			wantSuppressed: true,
			wantReason:     "cooldown",
		},
		{
			name:       "mailer unconfigured",
			res:        opsalerts.NotifyResult{Kind: domain.AlertTest, At: t0, Recorded: true, Reason: "mailer_not_configured"},
			wantStatus: http.StatusOK,
			wantReason: "mailer_not_configured",
		},
		{
			name:         "send failed",
			res:          opsalerts.NotifyResult{Kind: domain.AlertTest, At: t0, Recorded: true, Reason: "send_failed", Err: errors.New("resend 403 Bearer fake_bearer_val " + sampleKey)},
			wantStatus:   http.StatusBadGateway,
			wantReason:   "send_failed",
			wantRedacted: sampleKey,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{
				Store:        &fakeStore{},
				Sources:      &fakeSources{},
				Collector:    &countingCollector{},
				AdminToken:   "static-token",
				RequireAdmin: true,
				Login:        testLogin(t, &now),
				Alerts:       &fakeAlerts{},
				AlertTester:  &fakeAlertTester{res: tc.res},
			}
			rec := doAuth(s.Handler(), http.MethodPost, "/api/admin/alerts/test", "Bearer static-token", "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			var got testAlertDTO
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Kind != domain.AlertTest || !got.Recorded || got.Emailed != tc.wantEmailed || got.Suppressed != tc.wantSuppressed || got.Reason != tc.wantReason {
				t.Fatalf("unexpected body: %+v", got)
			}
			if tc.wantRedacted != "" {
				if strings.Contains(got.Error, tc.wantRedacted) || strings.Contains(got.Error, "fake_bearer_val") {
					t.Fatalf("redaction leaked in error: %q", got.Error)
				}
				if !strings.Contains(got.Error, "[redacted]") {
					t.Fatalf("expected sanitized error, got %q", got.Error)
				}
			}
		})
	}
}
