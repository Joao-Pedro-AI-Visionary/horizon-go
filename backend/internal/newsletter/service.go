// Package newsletter manages double opt-in email subscriptions and the
// daily "Top 5-7" digest newsletter dispatched via Resend at 08:00 BRT.
package newsletter

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/mail"
	"net/url"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/jpdev/horizon-go/internal/digest"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/retention"
)

const (
	StatusPending      = "pending"
	StatusActive       = "active"
	StatusUnsubscribed = "unsubscribed"

	DefaultMinItems   = 5
	DefaultMaxItems   = 7
	DefaultConfirmTTL = 48 * time.Hour
	Timezone          = retention.Timezone
)

// ErrMailerDisabled is returned when Resend/FrontURL credentials are absent.
var ErrMailerDisabled = errors.New("newsletter email delivery is not configured")

// Subscriber represents one email subscription record.
type Subscriber struct {
	ID              int64
	Email           string
	Status          string
	UnsubToken      string
	ConfirmExpires  *time.Time
	ConfirmedAt     *time.Time
	UnsubscribedAt  *time.Time
}

// Delivery records one daily edition attempt.
type Delivery struct {
	EditionDate string
	ItemIDs     []int64
	SentCount   int
	FailedCount int
	Status      string
}

// SendReport summarizes a newsletter dispatch (or dry run).
type SendReport struct {
	EditionDate    string  `json:"edition_date"`
	ItemIDs        []int64 `json:"item_ids"`
	ItemCount      int     `json:"item_count"`
	RecipientCount int     `json:"recipient_count"`
	SentCount      int     `json:"sent_count"`
	FailedCount    int     `json:"failed_count"`
	DryRun         bool    `json:"dry_run"`
	Skipped        bool    `json:"skipped"`
	SkipReason     string  `json:"skip_reason,omitempty"`
}

// SubscribeResult is returned after subscribing.
type SubscribeResult struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Mailer sends one email and returns the provider ID.
type Mailer interface {
	Send(ctx context.Context, to, subject, htmlBody, textBody string) (string, error)
}

// Store persists subscribers, candidates, and edition delivery history.
type Store interface {
	GetSettings(ctx context.Context) (domain.Settings, error)
	Subscribe(ctx context.Context, email, confirmHash, unsubToken string, expiresAt, now time.Time) (sub Subscriber, alreadyActive bool, err error)
	Confirm(ctx context.Context, confirmHash string, now time.Time) (sub Subscriber, ok bool, err error)
	Unsubscribe(ctx context.Context, unsubToken string, now time.Time) (ok bool, err error)
	ActiveSubscribers(ctx context.Context) ([]Subscriber, error)
	TopCandidates(ctx context.Context, since time.Time, threshold float64, limit int) ([]domain.FeedItem, error)
	HasDelivery(ctx context.Context, editionDate string) (bool, error)
	RecordDelivery(ctx context.Context, d Delivery) error
}

// Service owns subscription lifecycle and daily Top 5-7 digest dispatch.
type Service struct {
	Store      Store
	Mailer     Mailer
	FrontURL   string
	ConfirmTTL time.Duration
	Now        func() time.Time
	Log        *slog.Logger
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

func (s *Service) confirmTTL() time.Duration {
	if s.ConfirmTTL > 0 {
		return s.ConfirmTTL
	}
	return DefaultConfirmTTL
}

// EmailEnabled reports whether outbound confirmation and digest emails can be sent.
func (s *Service) EmailEnabled() bool {
	return s != nil && s.Mailer != nil && strings.TrimSpace(s.FrontURL) != ""
}

// NormalizeEmail validates and canonicalizes an email address.
func NormalizeEmail(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 254 {
		return "", domain.Invalidf("e-mail inválido")
	}
	addr, err := mail.ParseAddress(trimmed)
	if err != nil || strings.ContainsAny(addr.Address, " <>\r\n\t") {
		return "", domain.Invalidf("e-mail inválido")
	}
	email := strings.ToLower(addr.Address)
	parts := strings.Split(email, "@")
	if len(parts) != 2 || parts[0] == "" || !strings.Contains(parts[1], ".") || strings.HasPrefix(parts[1], ".") || strings.HasSuffix(parts[1], ".") {
		return "", domain.Invalidf("e-mail inválido")
	}
	return email, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tok)))
	return hex.EncodeToString(sum[:])
}

func (s *Service) link(path, token string) string {
	base := strings.TrimRight(strings.TrimSpace(s.FrontURL), "/")
	return base + path + "?token=" + url.QueryEscape(token)
}

// Subscribe creates or refreshes a pending subscription and emails the double opt-in confirmation link.
func (s *Service) Subscribe(ctx context.Context, rawEmail string) (SubscribeResult, error) {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return SubscribeResult{}, err
	}
	if !s.EmailEnabled() {
		return SubscribeResult{}, ErrMailerDisabled
	}
	rawConfirm, err := randomHex(24)
	if err != nil {
		return SubscribeResult{}, err
	}
	unsubTok, err := randomHex(24)
	if err != nil {
		return SubscribeResult{}, err
	}
	now := s.now().UTC()
	sub, alreadyActive, err := s.Store.Subscribe(ctx, email, hashToken(rawConfirm), unsubTok, now.Add(s.confirmTTL()), now)
	if err != nil {
		return SubscribeResult{}, err
	}
	if alreadyActive {
		return SubscribeResult{
			Status:  StatusActive,
			Message: "Este e-mail já está confirmado na newsletter.",
		}, nil
	}
	confirmURL := s.link("/newsletter/confirm", rawConfirm)
	unsubURL := s.link("/newsletter/unsubscribe", sub.UnsubToken)
	htmlBody, textBody := renderConfirmEmail(confirmURL, unsubURL)
	if _, err := s.Mailer.Send(ctx, email, "Confirme sua inscrição — "+digest.Brand, htmlBody, textBody); err != nil {
		return SubscribeResult{}, fmt.Errorf("send confirm email: %w", err)
	}
	return SubscribeResult{
		Status:  StatusPending,
		Message: "Enviamos um link de confirmação para o seu e-mail.",
	}, nil
}

// Confirm activates a pending subscriber from their single-use confirmation token.
func (s *Service) Confirm(ctx context.Context, rawToken string) (SubscribeResult, error) {
	tok := strings.TrimSpace(rawToken)
	if tok == "" {
		return SubscribeResult{}, domain.Invalidf("token de confirmação obrigatório")
	}
	sub, ok, err := s.Store.Confirm(ctx, hashToken(tok), s.now().UTC())
	if err != nil {
		return SubscribeResult{}, err
	}
	if !ok {
		return SubscribeResult{}, domain.Invalidf("link de confirmação inválido ou expirado")
	}
	return SubscribeResult{
		Status:  sub.Status,
		Message: "Inscrição confirmada! Você receberá o Top 5–7 todo dia às 08h.",
	}, nil
}

// Unsubscribe opts out a subscriber from their permanent unsubscribe token.
func (s *Service) Unsubscribe(ctx context.Context, rawToken string) (SubscribeResult, error) {
	tok := strings.TrimSpace(rawToken)
	if tok == "" {
		return SubscribeResult{}, domain.Invalidf("token de descadastro obrigatório")
	}
	ok, err := s.Store.Unsubscribe(ctx, tok, s.now().UTC())
	if err != nil {
		return SubscribeResult{}, err
	}
	if !ok {
		return SubscribeResult{}, domain.Invalidf("link de descadastro inválido")
	}
	return SubscribeResult{
		Status:  StatusUnsubscribed,
		Message: "Descadastro concluído. Você não receberá novos envios.",
	}, nil
}

// selectTopItems gathers 5..7 top items, preferring last 36h above threshold and widening if scarce.
func (s *Service) selectTopItems(ctx context.Context, now time.Time) ([]domain.FeedItem, error) {
	st, err := s.Store.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	items, err := s.Store.TopCandidates(ctx, now.Add(-36*time.Hour), st.ScoreThreshold, DefaultMaxItems)
	if err != nil {
		return nil, err
	}
	if len(items) < DefaultMinItems {
		wider, err := s.Store.TopCandidates(ctx, now.Add(-72*time.Hour), st.ScoreThreshold, DefaultMaxItems)
		if err != nil {
			return nil, err
		}
		if len(wider) > len(items) {
			items = wider
		}
	}
	if len(items) < DefaultMinItems {
		relaxed, err := s.Store.TopCandidates(ctx, now.Add(-72*time.Hour), 0, DefaultMaxItems)
		if err != nil {
			return nil, err
		}
		if len(relaxed) > len(items) {
			items = relaxed
		}
	}
	if len(items) > DefaultMaxItems {
		items = items[:DefaultMaxItems]
	}
	return items, nil
}

// SendDaily sends today's Top 5-7 edition once per edition_date (used by scheduler and admin trigger).
func (s *Service) SendDaily(ctx context.Context, at time.Time, dryRun, force bool) (SendReport, error) {
	if at.IsZero() {
		at = s.now()
	}
	loc, err := time.LoadLocation(Timezone)
	if err != nil {
		loc = time.FixedZone("BRT", -3*3600)
	}
	local := at.In(loc)
	editionDate := local.Format(time.DateOnly)
	rep := SendReport{EditionDate: editionDate, DryRun: dryRun, ItemIDs: []int64{}}

	if !dryRun && !force {
		sent, err := s.Store.HasDelivery(ctx, editionDate)
		if err != nil {
			return rep, err
		}
		if sent {
			rep.Skipped = true
			rep.SkipReason = "edition_already_sent"
			return rep, nil
		}
	}

	items, err := s.selectTopItems(ctx, at.UTC())
	if err != nil {
		return rep, err
	}
	rep.ItemCount = len(items)
	for _, it := range items {
		rep.ItemIDs = append(rep.ItemIDs, it.ItemID)
	}
	subs, err := s.Store.ActiveSubscribers(ctx)
	if err != nil {
		return rep, err
	}
	rep.RecipientCount = len(subs)

	if dryRun {
		return rep, nil
	}
	if !s.EmailEnabled() {
		rep.Skipped = true
		rep.SkipReason = "mailer_unconfigured"
		return rep, nil
	}
	if len(items) == 0 {
		rep.Skipped = true
		rep.SkipReason = "no_items"
		_ = s.Store.RecordDelivery(ctx, Delivery{EditionDate: editionDate, ItemIDs: []int64{}, Status: "skipped"})
		return rep, nil
	}
	if len(subs) == 0 {
		rep.Skipped = true
		rep.SkipReason = "no_subscribers"
		_ = s.Store.RecordDelivery(ctx, Delivery{EditionDate: editionDate, ItemIDs: rep.ItemIDs, Status: "skipped"})
		return rep, nil
	}

	subject := fmt.Sprintf("%s — Top %d do dia (%s)", digest.Brand, len(items), local.Format("02/01"))
	var lastErr error
	for _, sub := range subs {
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
		unsubURL := s.link("/newsletter/unsubscribe", sub.UnsubToken)
		htmlBody, textBody := renderEditionEmail(local.Format("02/01/2006"), s.FrontURL, items, unsubURL)
		if _, err := s.Mailer.Send(ctx, sub.Email, subject, htmlBody, textBody); err != nil {
			rep.FailedCount++
			lastErr = err
			s.log().Warn("newsletter send failed", "subscriber_id", sub.ID, "err", err)
			continue
		}
		rep.SentCount++
	}

	status := "ok"
	if rep.SentCount == 0 && rep.FailedCount > 0 {
		status = "error"
	} else if rep.FailedCount > 0 {
		status = "partial"
	}
	_ = s.Store.RecordDelivery(context.WithoutCancel(ctx), Delivery{
		EditionDate: editionDate,
		ItemIDs:     rep.ItemIDs,
		SentCount:   rep.SentCount,
		FailedCount: rep.FailedCount,
		Status:      status,
	})
	if status == "error" && lastErr != nil {
		return rep, lastErr
	}
	return rep, nil
}

func renderConfirmEmail(confirmURL, unsubURL string) (string, string) {
	escConfirm := html.EscapeString(confirmURL)
	escUnsub := html.EscapeString(unsubURL)
	htmlBody := fmt.Sprintf(`<!doctype html><html lang="pt-BR"><body style="margin:0;padding:24px;background:#eef2ee;font-family:Inter,system-ui,-apple-system,sans-serif;color:#121a14;">
<div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #d5ded6;border-radius:20px;padding:28px;">
  <p style="margin:0 0 8px;font-size:12px;font-weight:700;letter-spacing:0.08em;text-transform:uppercase;color:#276b43;">%s</p>
  <h1 style="margin:0 0 12px;font-size:22px;line-height:1.25;">Confirme sua inscrição no resumo diário</h1>
  <p style="margin:0 0 20px;font-size:15px;line-height:1.55;color:#415045;">Clique abaixo para ativar o envio diário do <strong>Top 5–7</strong> todo dia às 08:00 (horário de Brasília).</p>
  <p style="margin:0 0 24px;"><a href="%s" style="display:inline-block;padding:12px 22px;border-radius:999px;background:#276b43;color:#ffffff;font-weight:700;text-decoration:none;">Confirmar e-mail</a></p>
  <p style="margin:0;font-size:12px;line-height:1.5;color:#5a695e;border-top:1px solid #e4ebe5;padding-top:14px;">Se não quiser receber nada ou cancelar depois, acesse <a href="%s" style="color:#276b43;">descadastrar</a>.</p>
</div></body></html>`, digest.Brand, escConfirm, escUnsub)

	textBody := fmt.Sprintf("%s — Confirme sua inscrição\n\nPara confirmar o envio diário às 08h, abra:\n%s\n\nPara se descadastrar: %s\n", digest.Brand, confirmURL, unsubURL)
	return htmlBody, textBody
}

func renderEditionEmail(dateStr, frontURL string, items []domain.FeedItem, unsubURL string) (string, string) {
	base := strings.TrimRight(strings.TrimSpace(frontURL), "/")
	var hb, tb bytes.Buffer
	fmt.Fprintf(&hb, `<!doctype html><html lang="pt-BR"><body style="margin:0;padding:20px;background:#eef2ee;font-family:Inter,system-ui,-apple-system,sans-serif;color:#121a14;">
<div style="max-width:600px;margin:0 auto;background:#ffffff;border:1px solid #d5ded6;border-radius:20px;padding:28px;">
  <p style="margin:0 0 6px;font-size:12px;font-weight:700;letter-spacing:0.08em;text-transform:uppercase;color:#276b43;">%s · %s</p>
  <h1 style="margin:0 0 20px;font-size:22px;line-height:1.25;">Top %d do dia</h1>`, digest.Brand, html.EscapeString(dateStr), len(items))

	fmt.Fprintf(&tb, "%s — Top %d do dia (%s)\n\n", digest.Brand, len(items), dateStr)

	for i, it := range items {
		itemLink := fmt.Sprintf("%s/items/%d", base, it.ItemID)
		origURL := digest.SafeURL(it.URL)
		if origURL == "" {
			origURL = itemLink
		}
		summary := strings.TrimSpace(it.DisplaySummary())
		if summary == "" {
			summary = strings.TrimSpace(it.ScoreReason)
		}
		fmt.Fprintf(&hb, `
  <div style="padding:14px 0;border-top:1px solid #e4ebe5;">
    <p style="margin:0 0 4px;font-size:12px;color:#5a695e;">#%d · %s · <strong>Nota %.1f/10</strong></p>
    <p style="margin:0 0 6px;font-size:16px;font-weight:700;line-height:1.35;"><a href="%s" style="color:#121a14;text-decoration:none;">%s</a></p>`,
			i+1, html.EscapeString(it.SourceName), it.Score, html.EscapeString(itemLink), html.EscapeString(it.DisplayTitle()))
		if summary != "" {
			fmt.Fprintf(&hb, `<p style="margin:0 0 6px;font-size:14px;line-height:1.5;color:#415045;">%s</p>`, html.EscapeString(summary))
		}
		fmt.Fprintf(&hb, `<p style="margin:0;font-size:12px;"><a href="%s" style="color:#276b43;text-decoration:none;">Ler no Horizon</a> · <a href="%s" style="color:#5a695e;">Original</a></p></div>`,
			html.EscapeString(itemLink), html.EscapeString(origURL))

		fmt.Fprintf(&tb, "%d. [%s · %.1f/10] %s\n", i+1, it.SourceName, it.Score, it.DisplayTitle())
		if summary != "" {
			fmt.Fprintf(&tb, "   %s\n", summary)
		}
		fmt.Fprintf(&tb, "   Horizon: %s | Original: %s\n\n", itemLink, origURL)
	}

	escUnsub := html.EscapeString(unsubURL)
	fmt.Fprintf(&hb, `
  <p style="margin:20px 0 0;padding-top:14px;border-top:1px solid #e4ebe5;font-size:12px;color:#5a695e;">Você recebe este resumo diário por ter confirmado sua inscrição. <a href="%s" style="color:#276b43;">Cancelar inscrição (descadastro)</a>.</p>
</div></body></html>`, escUnsub)
	fmt.Fprintf(&tb, "---\nDescadastrar: %s\n", unsubURL)
	return hb.String(), tb.String()
}
