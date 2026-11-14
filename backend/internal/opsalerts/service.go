// Package opsalerts dispatches operational alerts to the admin alert badge
// (alert_events) and via e-mail (Resend) with a persistent 6-hour per-kind
// cooldown.
package opsalerts

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // ensure America/Sao_Paulo works inside distroless images

	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/retention"
)

const (
	// DefaultCooldown is the minimum interval between e-mails of the same kind.
	DefaultCooldown = 6 * time.Hour
	// DefaultTimezone formats timestamps sent in operational alert e-mails.
	DefaultTimezone = retention.Timezone

	maxErrorSummaryRunes = 240
	maxBadgeMessageRunes = 500
	notifyTimeout        = 15 * time.Second
)

// Mailer sends one e-mail and returns the provider message id.
type Mailer interface {
	Send(ctx context.Context, to, subject, htmlBody, textBody string) (string, error)
}

// Store persists badge events and cross-instance e-mail cooldown state.
type Store interface {
	RecordAlertEvent(ctx context.Context, kind, name, message string, at time.Time) error
	HasRecentAlertEvent(ctx context.Context, kind, name string, since time.Time) (bool, error)
	ClaimEmailCooldown(ctx context.Context, kind string, now time.Time, cooldown time.Duration) (prev *time.Time, claimed bool, err error)
	ReleaseEmailCooldown(ctx context.Context, kind string, claimedAt time.Time, prev *time.Time) error
}

// Detail is one key-value line rendered in the e-mail body.
type Detail struct {
	Label string
	Value string
}

// Event describes one operational alert to record and optionally e-mail.
type Event struct {
	Kind              string
	Name              string
	Subject           string
	Title             string
	Summary           string
	Details           []Detail
	At                time.Time
	DedupeBadgeWindow time.Duration
}

// NotifyResult reports what happened for one alert notification.
type NotifyResult struct {
	Kind       string    `json:"kind"`
	At         time.Time `json:"at"`
	Recorded   bool      `json:"recorded"`
	Emailed    bool      `json:"emailed"`
	Suppressed bool      `json:"suppressed"`
	Reason     string    `json:"reason,omitempty"`
	MessageID  string    `json:"provider_id,omitempty"`
	Err        error     `json:"-"`
}

// Service records operational alerts and sends bounded e-mails via Mailer.
type Service struct {
	Store    Store
	Mailer   Mailer
	To       string
	Cooldown time.Duration
	Timezone string
	Now      func() time.Time
	Log      *slog.Logger
}

func (s *Service) log() *slog.Logger {
	if s == nil || s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) cooldown() time.Duration {
	if s != nil && s.Cooldown > 0 {
		return s.Cooldown
	}
	return DefaultCooldown
}

func (s *Service) location() *time.Location {
	tz := DefaultTimezone
	if s != nil && strings.TrimSpace(s.Timezone) != "" {
		tz = strings.TrimSpace(s.Timezone)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.FixedZone("BRT", -3*3600)
	}
	return loc
}

// Notify persists the event to the admin badge and sends at most one e-mail per
// kind every Cooldown window. Failures to send e-mail are logged and returned in
// NotifyResult without failing callers' main workflow.
func (s *Service) Notify(ctx context.Context, ev Event) NotifyResult {
	at := ev.At
	if at.IsZero() {
		at = s.now()
	}
	at = at.UTC()
	res := NotifyResult{Kind: ev.Kind, At: at}
	if s == nil {
		res.Reason = "service_nil"
		return res
	}

	actx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()

	summary := s.scrub(ev.Summary, maxBadgeMessageRunes)
	skipBadge := false
	if s.Store != nil {
		if ev.DedupeBadgeWindow > 0 {
			recent, err := s.Store.HasRecentAlertEvent(actx, ev.Kind, ev.Name, at.Add(-ev.DedupeBadgeWindow))
			if err != nil {
				s.log().Warn("operational alert: check recent event failed", "kind", ev.Kind, "name", ev.Name, "err", s.scrub(err.Error(), maxErrorSummaryRunes))
			} else if recent {
				skipBadge = true
				res.Recorded = true
			}
		}
		if !skipBadge {
			if err := s.Store.RecordAlertEvent(actx, ev.Kind, ev.Name, summary, at); err != nil {
				s.log().Error("operational alert: record badge event failed", "kind", ev.Kind, "name", ev.Name, "err", s.scrub(err.Error(), maxErrorSummaryRunes))
			} else {
				res.Recorded = true
			}
		}
	}

	to := strings.TrimSpace(s.To)
	if s.Mailer == nil || to == "" {
		if !skipBadge {
			s.log().Warn("operational alert: resend not configured, recorded in badge only", "kind", ev.Kind, "name", ev.Name)
		}
		res.Reason = "mailer_not_configured"
		return res
	}

	var (
		prev    *time.Time
		claimed bool
		err     error
	)
	if s.Store != nil {
		prev, claimed, err = s.Store.ClaimEmailCooldown(actx, ev.Kind, at, s.cooldown())
		if err != nil {
			s.log().Error("operational alert: claim cooldown failed", "kind", ev.Kind, "name", ev.Name, "err", s.scrub(err.Error(), maxErrorSummaryRunes))
			res.Reason = "cooldown_error"
			res.Err = err
			return res
		}
		if !claimed {
			if !skipBadge {
				s.log().Info("operational alert: e-mail suppressed by cooldown", "kind", ev.Kind, "name", ev.Name, "cooldown", s.cooldown())
			}
			res.Suppressed = true
			res.Reason = "cooldown"
			return res
		}
	}

	htmlBody, textBody := s.renderEmail(ev, summary, at)
	msgID, sendErr := s.Mailer.Send(actx, to, ev.Subject, htmlBody, textBody)
	if sendErr != nil {
		if s.Store != nil {
			rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if rerr := s.Store.ReleaseEmailCooldown(rctx, ev.Kind, at, prev); rerr != nil {
				s.log().Warn("operational alert: release cooldown failed", "kind", ev.Kind, "err", s.scrub(rerr.Error(), maxErrorSummaryRunes))
			}
			rcancel()
		}
		s.log().Error("operational alert: e-mail send failed", "kind", ev.Kind, "name", ev.Name, "err", s.scrub(sendErr.Error(), maxErrorSummaryRunes))
		res.Reason = "send_failed"
		res.Err = sendErr
		return res
	}

	res.Emailed = true
	res.MessageID = msgID
	s.log().Info("operational alert: e-mail sent", "kind", ev.Kind, "name", ev.Name, "provider_id", msgID)
	return res
}

func (s *Service) scrub(in string, maxRunes int) string {
	out := SanitizeText(in, maxRunes)
	if to := strings.TrimSpace(s.To); to != "" {
		out = strings.ReplaceAll(out, to, "[redacted-email]")
	}
	return out
}

func (s *Service) formatTime(t time.Time) string {
	return t.In(s.location()).Format("02/01/2006 15:04:05 MST")
}

func (s *Service) formatSlot(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(s.location()).Format("02/01/2006 15:04 MST")
}

func (s *Service) renderEmail(ev Event, summary string, at time.Time) (htmlBody, textBody string) {
	loc := s.location()
	atStr := at.In(loc).Format("02/01/2006 15:04:05 MST")
	title := ev.Title
	if title == "" {
		title = ev.Subject
	}

	var tb strings.Builder
	fmt.Fprintf(&tb, "%s\n\n%s\n\n", title, summary)
	for _, d := range ev.Details {
		fmt.Fprintf(&tb, "- %s: %s\n", d.Label, s.scrub(d.Value, maxBadgeMessageRunes))
	}
	fmt.Fprintf(&tb, "- Tipo: %s\n- Horário (BRT): %s\n", ev.Kind, atStr)

	var rows strings.Builder
	for _, d := range ev.Details {
		fmt.Fprintf(&rows,
			`<tr><td style="padding:6px 12px 6px 0;color:#55625a;font-size:13px;vertical-align:top">%s</td><td style="padding:6px 0;font-size:13px;font-weight:600;color:#141414">%s</td></tr>`,
			html.EscapeString(d.Label),
			html.EscapeString(s.scrub(d.Value, maxBadgeMessageRunes)),
		)
	}
	fmt.Fprintf(&rows,
		`<tr><td style="padding:6px 12px 6px 0;color:#55625a;font-size:13px;vertical-align:top">Tipo</td><td style="padding:6px 0;font-size:13px;font-weight:600;color:#141414">%s</td></tr>`,
		html.EscapeString(ev.Kind),
	)
	fmt.Fprintf(&rows,
		`<tr><td style="padding:6px 12px 6px 0;color:#55625a;font-size:13px;vertical-align:top">Horário (BRT)</td><td style="padding:6px 0;font-size:13px;font-weight:600;color:#141414">%s</td></tr>`,
		html.EscapeString(atStr),
	)

	hb := fmt.Sprintf(`<!doctype html><html lang="pt-BR"><body style="margin:0;background:#eef2ee;font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;color:#141414">
<div style="max-width:560px;margin:0 auto;padding:32px 20px">
<div style="background:#fff;border-radius:20px;padding:28px">
<p style="margin:0 0 6px;font-size:12px;letter-spacing:.08em;text-transform:uppercase;color:#2f6b45">Horizon · Radar by JpDev</p>
<h1 style="margin:0 0 12px;font-size:22px">%s</h1>
<p style="margin:0 0 18px;line-height:1.5;font-size:14px">%s</p>
<table style="width:100%%;border-collapse:collapse">%s</table>
</div></div></body></html>`,
		html.EscapeString(title),
		html.EscapeString(summary),
		rows.String(),
	)
	return hb, tb.String()
}

// JobDisplayName returns the human-readable badge/e-mail name for a scheduler job.
func JobDisplayName(job string) string {
	switch job {
	case "collect":
		return "Coleta"
	case "cleanup":
		return "Limpeza"
	default:
		return job
	}
}

// JobFailed records and notifies a scheduler job failure without secrets or stack traces.
func (s *Service) JobFailed(ctx context.Context, job string, slot, at time.Time, err error) NotifyResult {
	name := JobDisplayName(job)
	errMsg := "erro desconhecido"
	if err != nil {
		errMsg = SanitizeText(err.Error(), maxErrorSummaryRunes)
	}
	subject := fmt.Sprintf("[Horizon] Falha na %s", strings.ToLower(name))
	summary := fmt.Sprintf("Slot %s falhou: %s", s.formatSlot(slot), errMsg)
	return s.Notify(ctx, Event{
		Kind:    domain.AlertJobFailed,
		Name:    name,
		Subject: subject,
		Title:   fmt.Sprintf("Falha no job de %s", strings.ToLower(name)),
		Summary: summary,
		At:      at,
		Details: []Detail{
			{Label: "Job", Value: fmt.Sprintf("%s (%s)", name, job)},
			{Label: "Slot", Value: s.formatSlot(slot)},
			{Label: "Erro", Value: errMsg},
		},
	})
}

// JobRecovered records and notifies that a scheduler job succeeded after a prior failure.
func (s *Service) JobRecovered(ctx context.Context, job string, slot, at time.Time) NotifyResult {
	name := JobDisplayName(job)
	subject := fmt.Sprintf("[Horizon] %s recuperada", name)
	summary := fmt.Sprintf("Slot %s concluído com sucesso após falha anterior.", s.formatSlot(slot))
	return s.Notify(ctx, Event{
		Kind:    domain.AlertJobRecovered,
		Name:    name,
		Subject: subject,
		Title:   fmt.Sprintf("%s voltou a funcionar", name),
		Summary: summary,
		At:      at,
		Details: []Detail{
			{Label: "Job", Value: fmt.Sprintf("%s (%s)", name, job)},
			{Label: "Slot", Value: s.formatSlot(slot)},
			{Label: "Situação", Value: "Execução concluída sem erro após falha anterior"},
		},
	})
}

// JobMissed records and notifies a missed or >3h overdue scheduled slot.
func (s *Service) JobMissed(ctx context.Context, job string, slot, at time.Time, viaCatchUp bool) NotifyResult {
	name := JobDisplayName(job)
	if at.IsZero() {
		at = s.now()
	}
	delay := at.Sub(slot).Round(time.Minute)
	if delay < 0 {
		delay = 0
	}
	origin := "Atraso acima de 3h sem execução"
	summary := fmt.Sprintf("Slot %s atrasado há %s sem conclusão.", s.formatSlot(slot), formatDurationPT(delay))
	var dedupe time.Duration
	if viaCatchUp {
		origin = "Detectado no catch-up ao iniciar"
		summary = fmt.Sprintf("Slot %s perdeu o horário e entrou em catch-up com %s de atraso.", s.formatSlot(slot), formatDurationPT(delay))
	} else {
		dedupe = s.cooldown()
	}
	return s.Notify(ctx, Event{
		Kind:              domain.AlertJobMissed,
		Name:              name,
		Subject:           fmt.Sprintf("[Horizon] %s fora do horário", name),
		Title:             fmt.Sprintf("Slot perdido: %s", name),
		Summary:           summary,
		At:                at,
		DedupeBadgeWindow: dedupe,
		Details: []Detail{
			{Label: "Job", Value: fmt.Sprintf("%s (%s)", name, job)},
			{Label: "Slot esperado", Value: s.formatSlot(slot)},
			{Label: "Origem", Value: origin},
			{Label: "Atraso", Value: formatDurationPT(delay)},
		},
	})
}

// CleanupBlocked records and notifies when retention safety guards block real deletion.
func (s *Service) CleanupBlocked(ctx context.Context, slot, at time.Time, dec retention.DeleteDecision) NotifyResult {
	summary := fmt.Sprintf("Bloqueada (%s): items=%d, items_total=%d, feed_view=%d.", dec.Reason, dec.Items, dec.Total, dec.Feed)
	return s.Notify(ctx, Event{
		Kind:    domain.AlertCleanupBlocked,
		Name:    "Limpeza",
		Subject: "[Horizon] Limpeza bloqueada",
		Title:   "Limpeza de retenção bloqueada pelas travas",
		Summary: summary,
		At:      at,
		Details: []Detail{
			{Label: "Slot", Value: s.formatSlot(slot)},
			{Label: "Motivo", Value: dec.Reason},
			{Label: "items (candidatos)", Value: fmt.Sprint(dec.Items)},
			{Label: "items_total", Value: fmt.Sprint(dec.Total)},
			{Label: "feed_view", Value: fmt.Sprint(dec.Feed)},
		},
	})
}

// XIngestStale records and notifies when the X (push) source has received no posts for >36h.
func (s *Service) XIngestStale(ctx context.Context, lastIngestAt, anchorAt *time.Time, at time.Time) NotifyResult {
	if at.IsZero() {
		at = s.now()
	}
	var (
		refLabel string
		refVal   string
		elapsed  time.Duration
		summary  string
	)
	if lastIngestAt != nil {
		elapsed = at.Sub(*lastIngestAt).Round(time.Minute)
		refLabel = "Última ingestão X"
		refVal = s.formatTime(*lastIngestAt)
		summary = fmt.Sprintf("Sem novos posts do X (radar) há %s (último em %s; limite 36h).", formatDurationPT(elapsed), s.formatSlot(*lastIngestAt))
	} else if anchorAt != nil {
		elapsed = at.Sub(*anchorAt).Round(time.Minute)
		refLabel = "Instalação / âncora"
		refVal = s.formatTime(*anchorAt)
		summary = fmt.Sprintf("Nenhuma ingestão do X (radar) registrada desde %s (%s; limite 36h).", s.formatSlot(*anchorAt), formatDurationPT(elapsed))
	} else {
		refLabel = "Situação"
		refVal = "Sem ingestão registrada"
		summary = "Sem novos posts do X (radar) acima do limite de 36h."
	}

	return s.Notify(ctx, Event{
		Kind:              domain.AlertXIngestStale,
		Name:              "X (radar)",
		Subject:           "[Horizon] Ingestão do X parada",
		Title:             "Ingestão do X (radar) atrasada",
		Summary:           summary,
		At:                at,
		DedupeBadgeWindow: s.cooldown(),
		Details: []Detail{
			{Label: "Fonte", Value: "X (radar) [push]"},
			{Label: refLabel, Value: refVal},
			{Label: "Tempo sem posts", Value: formatDurationPT(elapsed)},
		},
	})
}

// SendTest records a test alert in the badge and attempts to send one e-mail.
func (s *Service) SendTest(ctx context.Context) NotifyResult {
	at := s.now()
	return s.Notify(ctx, Event{
		Kind:    domain.AlertTest,
		Name:    "Teste operacional",
		Subject: "[Horizon] Teste de alerta operacional",
		Title:   "Teste de alerta operacional",
		Summary: fmt.Sprintf("Teste manual disparado às %s via POST /api/admin/alerts/test.", s.formatTime(at)),
		At:      at,
		Details: []Detail{
			{Label: "Origem", Value: "POST /api/admin/alerts/test"},
			{Label: "Destino", Value: "RESET_EMAIL_TO"},
		},
	})
}

// SchedulerBridge adapts Service (whose methods return NotifyResult) to
// satisfy scheduler.Alerter without coupling opsalerts to scheduler.
type SchedulerBridge struct{ *Service }

func (b SchedulerBridge) JobFailed(ctx context.Context, job string, slot, at time.Time, err error) {
	b.Service.JobFailed(ctx, job, slot, at, err)
}

func (b SchedulerBridge) JobRecovered(ctx context.Context, job string, slot, at time.Time) {
	b.Service.JobRecovered(ctx, job, slot, at)
}

func (b SchedulerBridge) JobMissed(ctx context.Context, job string, slot, at time.Time, viaCatchUp bool) {
	b.Service.JobMissed(ctx, job, slot, at, viaCatchUp)
}

func (b SchedulerBridge) CleanupBlocked(ctx context.Context, slot, at time.Time, dec retention.DeleteDecision) {
	b.Service.CleanupBlocked(ctx, slot, at, dec)
}

func (b SchedulerBridge) XIngestStale(ctx context.Context, lastIngestAt, anchorAt *time.Time, at time.Time) {
	b.Service.XIngestStale(ctx, lastIngestAt, anchorAt, at)
}

var (
	reURLCreds  = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/@\s]+)@`)
	reBearer    = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)
	reAPIKey    = regexp.MustCompile(`\b(?:re|gsk)_[A-Za-z0-9_-]{8,}`)
	reParamAuth = regexp.MustCompile(`(?i)\b(token|api_key|apikey|password|passwd|secret|authorization)=([^\s&"']+)`)
	reSpace     = regexp.MustCompile(`\s+`)
)

// SanitizeText turns an error or operational message into a single-line summary
// free of stack traces and common secret/credential patterns.
func SanitizeText(s string, maxRunes int) string {
	for _, marker := range []string{"\ngoroutine ", "\r\ngoroutine ", "\n\t/", "\r\n\t/"} {
		if idx := strings.Index(s, marker); idx >= 0 {
			s = s[:idx]
		}
	}
	s = reURLCreds.ReplaceAllString(s, "${1}[redacted]@")
	s = reBearer.ReplaceAllString(s, "Bearer [redacted]")
	s = reAPIKey.ReplaceAllString(s, "[redacted]")
	s = reParamAuth.ReplaceAllString(s, "${1}=[redacted]")
	s = strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
	if maxRunes <= 0 || len([]rune(s)) <= maxRunes {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:maxRunes-1])) + "…"
}

func formatDurationPT(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	if h == 0 {
		return fmt.Sprintf("%dm", m)
	}
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%02dm", h, m)
}
