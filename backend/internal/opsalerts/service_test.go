package opsalerts

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/retention"
)

type recordedEvent struct {
	kind, name, msg string
	at              time.Time
}

type memAlertStore struct {
	mu        sync.Mutex
	events    []recordedEvent
	cooldowns map[string]time.Time
}

func newMemAlertStore() *memAlertStore {
	return &memAlertStore{cooldowns: map[string]time.Time{}}
}

func (m *memAlertStore) RecordAlertEvent(_ context.Context, kind, name, message string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, recordedEvent{kind: kind, name: name, msg: message, at: at})
	return nil
}

func (m *memAlertStore) HasRecentAlertEvent(_ context.Context, kind, name string, since time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ev := range m.events {
		if ev.kind == kind && ev.name == name && !ev.at.Before(since) {
			return true, nil
		}
	}
	return false, nil
}

func (m *memAlertStore) ClaimEmailCooldown(_ context.Context, kind string, now time.Time, cooldown time.Duration) (*time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, ok := m.cooldowns[kind]
	if ok && prev.After(now.Add(-cooldown)) {
		cp := prev
		return &cp, false, nil
	}
	var prevPtr *time.Time
	if ok {
		cp := prev
		prevPtr = &cp
	}
	m.cooldowns[kind] = now
	return prevPtr, true, nil
}

func (m *memAlertStore) ReleaseEmailCooldown(_ context.Context, kind string, claimedAt time.Time, prev *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.cooldowns[kind]
	if !ok || !cur.Equal(claimedAt) {
		return nil
	}
	if prev == nil {
		delete(m.cooldowns, kind)
	} else {
		m.cooldowns[kind] = *prev
	}
	return nil
}

type sentMail struct {
	to, subject, html, text string
}

type fakeMailer struct {
	mu   sync.Mutex
	sent []sentMail
	err  error
}

func (f *fakeMailer) Send(_ context.Context, to, subject, htmlBody, textBody string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.sent = append(f.sent, sentMail{to: to, subject: subject, html: htmlBody, text: textBody})
	return "msg-ops-1", nil
}

func TestOperationalAlertTriggersAndSaoPauloFormatting(t *testing.T) {
	loc, err := time.LoadLocation(DefaultTimezone)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 14, 15, 0, 0, loc)
	st := newMemAlertStore()
	mail := &fakeMailer{}
	svc := &Service{
		Store:    st,
		Mailer:   mail,
		To:       "admin@example.com",
		Cooldown: 6 * time.Hour,
		Now:      func() time.Time { return now },
		Log:      slog.New(slog.DiscardHandler),
	}

	slot := time.Date(2026, 11, 14, 7, 30, 0, 0, loc)
	r1 := svc.JobFailed(context.Background(), "collect", slot, now, errors.New("dial tcp: timeout"))
	if !r1.Recorded || !r1.Emailed {
		t.Fatalf("job_failed: %+v", r1)
	}
	r2 := svc.JobRecovered(context.Background(), "collect", slot, now.Add(time.Minute))
	if !r2.Recorded || !r2.Emailed {
		t.Fatalf("job_recovered: %+v", r2)
	}
	r3 := svc.JobMissed(context.Background(), "cleanup", time.Date(2026, 11, 14, 1, 0, 0, 0, loc), now, true)
	if !r3.Recorded || !r3.Emailed {
		t.Fatalf("job_missed: %+v", r3)
	}
	r4 := svc.CleanupBlocked(context.Background(), time.Date(2026, 11, 14, 1, 0, 0, 0, loc), now, retention.DeleteDecision{
		Allow: false, Reason: "candidates_gt_80pct", Items: 85, Total: 100, Feed: 10,
	})
	if !r4.Recorded || !r4.Emailed {
		t.Fatalf("cleanup_blocked: %+v", r4)
	}
	lastX := now.Add(-38 * time.Hour)
	r5 := svc.XIngestStale(context.Background(), &lastX, nil, now)
	if !r5.Recorded || !r5.Emailed {
		t.Fatalf("x_ingest_stale: %+v", r5)
	}

	if len(st.events) != 5 || len(mail.sent) != 5 {
		t.Fatalf("events=%d sent=%d want 5/5", len(st.events), len(mail.sent))
	}
	if !strings.Contains(mail.sent[0].subject, "Falha na coleta") || !strings.Contains(mail.sent[0].text, "07:30") {
		t.Errorf("job_failed mail: %+v", mail.sent[0])
	}
	if !strings.Contains(mail.sent[3].text, "candidates_gt_80pct") || !strings.Contains(mail.sent[3].text, "85") || !strings.Contains(mail.sent[3].text, "100") {
		t.Errorf("cleanup_blocked mail missing counts/reason: %+v", mail.sent[3])
	}
	if !strings.Contains(mail.sent[4].text, "38h") {
		t.Errorf("x_ingest_stale mail missing age: %+v", mail.sent[4])
	}
	for _, m := range mail.sent {
		if m.to != "admin@example.com" {
			t.Errorf("unexpected recipient %q", m.to)
		}
		if !strings.Contains(m.text, "14/11/2026") {
			t.Errorf("expected BRT formatted date in %q", m.text)
		}
	}
}

func TestSixHourCooldownSuppressesEmailWhileRecordingBadge(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	st := newMemAlertStore()
	mail := &fakeMailer{}
	svc := &Service{
		Store:    st,
		Mailer:   mail,
		To:       "admin@example.com",
		Cooldown: 6 * time.Hour,
		Now:      func() time.Time { return now },
		Log:      slog.New(slog.DiscardHandler),
	}

	slot := now.Add(-10 * time.Minute)
	res1 := svc.JobFailed(context.Background(), "collect", slot, now, errors.New("err 1"))
	if !res1.Emailed || !res1.Recorded {
		t.Fatalf("first failed: %+v", res1)
	}

	// 2 hours later, cleanup fails (same kind: job_failed) -> email suppressed, badge recorded!
	now = now.Add(2 * time.Hour)
	res2 := svc.JobFailed(context.Background(), "cleanup", slot, now, errors.New("err 2"))
	if res2.Emailed || !res2.Suppressed || !res2.Recorded {
		t.Fatalf("within cooldown: %+v", res2)
	}
	if len(mail.sent) != 1 || len(st.events) != 2 {
		t.Fatalf("sent=%d events=%d want 1/2", len(mail.sent), len(st.events))
	}

	// Different kind (job_recovered) inside job_failed's cooldown is independent and sends.
	resRec := svc.JobRecovered(context.Background(), "collect", slot, now)
	if !resRec.Emailed || !resRec.Recorded {
		t.Fatalf("different kind should email: %+v", resRec)
	}

	// After 6 hours from first job_failed, next job_failed sends again.
	now = now.Add(4*time.Hour + time.Minute)
	res3 := svc.JobFailed(context.Background(), "collect", slot, now, errors.New("err 3"))
	if !res3.Emailed || !res3.Recorded {
		t.Fatalf("after cooldown expired: %+v", res3)
	}
}

func TestSendFailureReleasesCooldownAndUnconfiguredMailerStillRecordsBadge(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	st := newMemAlertStore()
	mail := &fakeMailer{err: errors.New("resend 502")}
	var logBuf bytes.Buffer
	svc := &Service{
		Store:  st,
		Mailer: mail,
		To:     "secret-owner@example.com",
		Now:    func() time.Time { return now },
		Log:    slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	res := svc.JobFailed(context.Background(), "collect", now, now, errors.New("postgres://user:supersecret@db.host/app failed Bearer tok123 re_secret99999999\ngoroutine 1 [running]:\n\t/app/main.go:1"))
	if res.Emailed || !res.Recorded || res.Err == nil {
		t.Fatalf("expected recorded=true, emailed=false, err!=nil: %+v", res)
	}
	if strings.Contains(logBuf.String(), "secret-owner@example.com") {
		t.Fatalf("log leaked recipient email: %s", logBuf.String())
	}
	for _, ev := range st.events {
		for _, secret := range []string{"supersecret", "tok123", "re_secret99999999", "goroutine 1", "secret-owner@example.com"} {
			if strings.Contains(ev.msg, secret) {
				t.Fatalf("badge event leaked %q in %q", secret, ev.msg)
			}
		}
	}

	// Because send failed, cooldown was released so next attempt succeeds when Resend recovers.
	mail.err = nil
	res2 := svc.JobFailed(context.Background(), "collect", now, now.Add(time.Minute), errors.New("still down"))
	if !res2.Emailed {
		t.Fatalf("expected retry email after released cooldown, got %+v", res2)
	}

	// Unconfigured mailer still records badge and succeeds silently without error.
	svcNoMail := &Service{Store: st, Now: func() time.Time { return now }, Log: slog.New(slog.DiscardHandler)}
	res3 := svcNoMail.SendTest(context.Background())
	if !res3.Recorded || res3.Emailed || res3.Reason != "mailer_not_configured" {
		t.Fatalf("unconfigured mailer: %+v", res3)
	}
	if st.events[len(st.events)-1].kind != domain.AlertTest {
		t.Fatalf("expected test alert recorded, got %+v", st.events[len(st.events)-1])
	}
}

func TestPollingDedupeBadgeWindow(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	st := newMemAlertStore()
	mail := &fakeMailer{}
	svc := &Service{
		Store:  st,
		Mailer: mail,
		To:     "admin@example.com",
		Now:    func() time.Time { return now },
		Log:    slog.New(slog.DiscardHandler),
	}

	lastX := now.Add(-40 * time.Hour)
	r1 := svc.XIngestStale(context.Background(), &lastX, nil, now)
	r2 := svc.XIngestStale(context.Background(), &lastX, nil, now.Add(15*time.Minute))
	if !r1.Emailed || r2.Emailed || !r2.Suppressed {
		t.Fatalf("r1=%+v r2=%+v", r1, r2)
	}
	if len(st.events) != 1 {
		t.Fatalf("expected polling dedupe to avoid inserting duplicate badge row within 6h, got %d", len(st.events))
	}
}
