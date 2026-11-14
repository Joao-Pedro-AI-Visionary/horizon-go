package newsletter

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
)

type sentMail struct {
	to, subject, html, text string
}

type fakeMailer struct {
	mu   sync.Mutex
	sent []sentMail
}

func (f *fakeMailer) Send(_ context.Context, to, subject, htmlBody, textBody string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMail{to, subject, htmlBody, textBody})
	return "msg-1", nil
}

type memStore struct {
	mu         sync.Mutex
	subs       map[string]Subscriber
	confirmIdx map[string]string
	items      []domain.FeedItem
	deliveries map[string]Delivery
}

func newMemStore() *memStore {
	return &memStore{
		subs:       map[string]Subscriber{},
		confirmIdx: map[string]string{},
		deliveries: map[string]Delivery{},
	}
}

func (m *memStore) GetSettings(context.Context) (domain.Settings, error) {
	return domain.Settings{ScoreThreshold: 6, Timezone: Timezone}, nil
}

func (m *memStore) Subscribe(_ context.Context, email, confirmHash, unsubToken string, expiresAt, _ time.Time) (Subscriber, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.subs[email]; ok && cur.Status == StatusActive {
		return cur, true, nil
	}
	sub := Subscriber{
		ID:             int64(len(m.subs) + 1),
		Email:          email,
		Status:         StatusPending,
		UnsubToken:     unsubToken,
		ConfirmExpires: &expiresAt,
	}
	m.subs[email] = sub
	m.confirmIdx[confirmHash] = email
	return sub, false, nil
}

func (m *memStore) Confirm(_ context.Context, confirmHash string, now time.Time) (Subscriber, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	email, ok := m.confirmIdx[confirmHash]
	if !ok {
		return Subscriber{}, false, nil
	}
	sub := m.subs[email]
	if sub.ConfirmExpires != nil && !sub.ConfirmExpires.After(now) {
		return Subscriber{}, false, nil
	}
	sub.Status = StatusActive
	sub.ConfirmedAt = &now
	m.subs[email] = sub
	delete(m.confirmIdx, confirmHash)
	return sub, true, nil
}

func (m *memStore) Unsubscribe(_ context.Context, unsubToken string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for email, s := range m.subs {
		if s.UnsubToken == unsubToken {
			s.Status = StatusUnsubscribed
			s.UnsubscribedAt = &now
			m.subs[email] = s
			return true, nil
		}
	}
	return false, nil
}

func (m *memStore) ActiveSubscribers(context.Context) ([]Subscriber, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Subscriber
	for _, s := range m.subs {
		if s.Status == StatusActive {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *memStore) TopCandidates(_ context.Context, _ time.Time, threshold float64, limit int) ([]domain.FeedItem, error) {
	var out []domain.FeedItem
	for _, it := range m.items {
		if it.Score >= threshold {
			out = append(out, it)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *memStore) HasDelivery(_ context.Context, editionDate string) (bool, error) {
	d, ok := m.deliveries[editionDate]
	return ok && (d.Status == "ok" || d.Status == "partial"), nil
}

func (m *memStore) RecordDelivery(_ context.Context, d Delivery) error {
	m.deliveries[d.EditionDate] = d
	return nil
}

func extractToken(body, path string) string {
	idx := strings.Index(body, path+"?token=")
	if idx < 0 {
		return ""
	}
	rest := body[idx+len(path+"?token="):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '"' || rest[i] == '\'' || rest[i] == '\n' || rest[i] == ' ' || rest[i] == '<' {
			rest = rest[:i]
			break
		}
	}
	v, _ := url.QueryUnescape(rest)
	return v
}

func TestDoubleOptInDailyTopAndUnsubscribe(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	for i := 1; i <= 8; i++ {
		store.items = append(store.items, domain.FeedItem{
			ItemID:     int64(i),
			SourceName: "HN",
			Title:      "Item " + string(rune('A'+i-1)),
			URL:        "https://news.example/" + string(rune('a'+i-1)),
			Score:      float64(9) - float64(i)*0.1,
			Summary:    "Resumo relevante",
		})
	}
	mailer := &fakeMailer{}
	now := time.Date(2026, 11, 14, 11, 0, 0, 0, time.UTC) // 08:00 BRT
	svc := &Service{
		Store:    store,
		Mailer:   mailer,
		FrontURL: "https://horizon.example",
		Now:      func() time.Time { return now },
	}

	// Rejects invalid email.
	if _, err := svc.Subscribe(ctx, "not-an-email"); err == nil {
		t.Fatal("expected invalid email error")
	}

	// Subscribe -> sends confirm email containing BOTH confirm link and unsubscribe link.
	res, err := svc.Subscribe(ctx, "User@Example.com")
	if err != nil || res.Status != StatusPending {
		t.Fatalf("subscribe: %+v err=%v", res, err)
	}
	if len(mailer.sent) != 1 || !strings.Contains(mailer.sent[0].html, "/newsletter/unsubscribe?token=") {
		t.Fatalf("confirm mail must contain unsubscribe link: %+v", mailer.sent)
	}

	// Pending subscribers must not receive daily edition yet.
	rep, err := svc.SendDaily(ctx, now, true, false)
	if err != nil || rep.ItemCount != 7 || rep.RecipientCount != 0 {
		t.Fatalf("dry run before confirm: %+v err=%v", rep, err)
	}

	// Confirm activates subscriber.
	confirmTok := extractToken(mailer.sent[0].text, "/newsletter/confirm")
	if confirmTok == "" {
		t.Fatal("missing confirm token in text email")
	}
	res, err = svc.Confirm(ctx, confirmTok)
	if err != nil || res.Status != StatusActive {
		t.Fatalf("confirm: %+v err=%v", res, err)
	}

	// Re-using same single-use confirm token fails.
	if _, err := svc.Confirm(ctx, confirmTok); err == nil {
		t.Fatal("expected single-use confirm token rejection")
	}

	// Daily send delivers Top 7 to active subscriber with unsubscribe link; second run same day skips.
	rep, err = svc.SendDaily(ctx, now, false, false)
	if err != nil || rep.SentCount != 1 || rep.ItemCount != 7 {
		t.Fatalf("send daily: %+v err=%v", rep, err)
	}
	if len(mailer.sent) != 2 || !strings.Contains(mailer.sent[1].text, "/newsletter/unsubscribe?token=") {
		t.Fatalf("edition mail missing unsubscribe link: %+v", mailer.sent)
	}
	rep2, err := svc.SendDaily(ctx, now, false, false)
	if err != nil || !rep2.Skipped || rep2.SkipReason != "edition_already_sent" {
		t.Fatalf("expected duplicate daily send skip: %+v err=%v", rep2, err)
	}

	// Unsubscribe stops future sends.
	unsubTok := extractToken(mailer.sent[1].text, "/newsletter/unsubscribe")
	if unsubTok == "" {
		t.Fatal("missing unsubscribe token")
	}
	if res, err := svc.Unsubscribe(ctx, unsubTok); err != nil || res.Status != StatusUnsubscribed {
		t.Fatalf("unsubscribe: %+v err=%v", res, err)
	}
	rep3, err := svc.SendDaily(ctx, now.Add(24*time.Hour), true, false)
	if err != nil || rep3.RecipientCount != 0 {
		t.Fatalf("expected 0 recipients after unsub: %+v err=%v", rep3, err)
	}
}
