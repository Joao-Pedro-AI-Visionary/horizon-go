package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/newsletter"
)

type fakeNewsletter struct {
	lastEmail   string
	lastDryRun  bool
	lastForce   bool
	confirmTok  string
	unsubTok    string
	disabledErr bool
}

func (f *fakeNewsletter) Subscribe(_ context.Context, email string) (newsletter.SubscribeResult, error) {
	if f.disabledErr {
		return newsletter.SubscribeResult{}, newsletter.ErrMailerDisabled
	}
	f.lastEmail = email
	return newsletter.SubscribeResult{Status: newsletter.StatusPending, Message: "ok"}, nil
}

func (f *fakeNewsletter) Confirm(_ context.Context, tok string) (newsletter.SubscribeResult, error) {
	f.confirmTok = tok
	return newsletter.SubscribeResult{Status: newsletter.StatusActive, Message: "confirmed"}, nil
}

func (f *fakeNewsletter) Unsubscribe(_ context.Context, tok string) (newsletter.SubscribeResult, error) {
	f.unsubTok = tok
	return newsletter.SubscribeResult{Status: newsletter.StatusUnsubscribed, Message: "unsubscribed"}, nil
}

func (f *fakeNewsletter) SendDaily(_ context.Context, _ time.Time, dryRun, force bool) (newsletter.SendReport, error) {
	f.lastDryRun, f.lastForce = dryRun, force
	return newsletter.SendReport{EditionDate: "2026-11-14", ItemCount: 5, DryRun: dryRun}, nil
}

func TestNewsletterRoutesAndAuthProtection(t *testing.T) {
	s, _, srv := newTestServer(t)
	fn := &fakeNewsletter{}
	s.Newsletter = fn
	s.NewsletterLimiter = &LoginLimiter{Max: 2, Window: time.Minute, MaxBlock: time.Minute}
	s.RequireAdmin = true
	s.AdminToken = "top-secret-token"

	// Public subscribe works without admin token.
	resp, body := do(t, http.MethodPost, srv.URL+"/api/newsletter/subscribe", `{"email":"dev@example.com"}`)
	if resp.StatusCode != 200 || fn.lastEmail != "dev@example.com" {
		t.Fatalf("subscribe: %d %s", resp.StatusCode, body)
	}
	// Second subscribe succeeds, third hits rate limit 429.
	resp, _ = do(t, http.MethodPost, srv.URL+"/api/newsletter/subscribe", `{"email":"dev2@example.com"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("subscribe 2: %d", resp.StatusCode)
	}
	resp, _ = do(t, http.MethodPost, srv.URL+"/api/newsletter/subscribe", `{"email":"dev3@example.com"}`)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("subscribe rate limit expected 429, got %d", resp.StatusCode)
	}

	// Public confirm & unsubscribe work without admin token.
	confirmCode := "tok_conf"
	confirmPayload, _ := json.Marshal(map[string]string{"token": confirmCode})
	resp, body = do(t, http.MethodPost, srv.URL+"/api/newsletter/confirm", string(confirmPayload))
	if resp.StatusCode != 200 || fn.confirmTok != confirmCode {
		t.Fatalf("confirm: %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, http.MethodGet, srv.URL+"/api/newsletter/unsubscribe?token=tok_unsub", "")
	if resp.StatusCode != 200 || fn.unsubTok != "tok_unsub" {
		t.Fatalf("unsubscribe: %d %s", resp.StatusCode, body)
	}

	// Protected POST /api/newsletter/send requires admin Bearer token.
	resp, _ = do(t, http.MethodPost, srv.URL+"/api/newsletter/send", `{"dry_run":true}`)
	if resp.StatusCode != 401 {
		t.Fatalf("send without admin auth: got %d want 401", resp.StatusCode)
	}

	resp, body = do(t, http.MethodPost, srv.URL+"/api/newsletter/send", `{"dry_run":true}`, "Authorization", "Bearer top-secret-token")
	if resp.StatusCode != 200 || !fn.lastDryRun || !strings.Contains(body, `"item_count":5`) {
		t.Fatalf("send with admin auth: %d %s", resp.StatusCode, body)
	}

	// Disabled mailer returns 503 cleanly.
	fn.disabledErr = true
	s.NewsletterLimiter = nil
	resp, body = do(t, http.MethodPost, srv.URL+"/api/newsletter/subscribe", `{"email":"x@example.com"}`)
	if resp.StatusCode != 503 {
		t.Fatalf("disabled mailer expected 503, got %d %s", resp.StatusCode, body)
	}
	var eb map[string]string
	_ = json.Unmarshal([]byte(body), &eb)
}
