package httpapi

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/opsalerts"
)

type testAlertDTO struct {
	opsalerts.NotifyResult
	Error string `json:"error,omitempty"`
}

// DefaultAlertStale is how long without a successful fetch raises an alert
// (the schedule collects at 07:30 and 16:30 BRT, ~9h and ~15h apart).
const DefaultAlertStale = 14 * time.Hour

type alertsDTO struct {
	Count  int            `json:"count"`
	Since  time.Time      `json:"since"`
	Alerts []domain.Alert `json:"alerts"`
}

// adminAlerts (GET /api/admin/alerts?hours=48, admin only) lists the open
// failures of the window, newest first, one per kind and source/name.
func (s *Server) adminAlerts(w http.ResponseWriter, r *http.Request) {
	if s.Alerts == nil {
		writeError(w, http.StatusNotFound, "alerts not enabled")
		return
	}
	hours := 48
	if v := r.URL.Query().Get("hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 168 {
			s.fail(w, r, domain.Invalidf("hours must be 1..168"))
			return
		}
		hours = n
	}
	stale := s.AlertStale
	if stale == 0 {
		stale = DefaultAlertStale
	}
	now := time.Now()
	since := now.Add(-time.Duration(hours) * time.Hour)
	list, err := s.Alerts.Alerts(r.Context(), since, now, stale)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	list = dedupeAlerts(list)
	writeJSON(w, http.StatusOK, alertsDTO{Count: len(list), Since: since, Alerts: list})
}

// adminAlertTest (POST /api/admin/alerts/test, admin only) records one test
// operational alert in the admin badge and attempts to send a test e-mail.
func (s *Server) adminAlertTest(w http.ResponseWriter, r *http.Request) {
	if s.AlertTester == nil {
		writeError(w, http.StatusNotFound, "alert test not enabled")
		return
	}
	res := s.AlertTester.SendTest(r.Context())
	out := testAlertDTO{NotifyResult: res}
	if res.Err != nil {
		out.Error = opsalerts.SanitizeText(res.Err.Error(), 240)
		writeJSON(w, http.StatusBadGateway, out)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// dedupeAlerts keeps the newest alert per kind and source (or name) and
// orders the result newest first.
func dedupeAlerts(in []domain.Alert) []domain.Alert {
	sort.SliceStable(in, func(i, j int) bool { return in[i].At.After(in[j].At) })
	seen := map[string]bool{}
	out := make([]domain.Alert, 0, len(in))
	for _, a := range in {
		key := a.Kind + "\x00" + a.Name
		if a.SourceID != nil {
			key = a.Kind + "\x00#" + strconv.FormatInt(*a.SourceID, 10)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, a)
	}
	return out
}
