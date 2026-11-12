package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/jpdev/horizon-go/internal/domain"
)

// CredentialStore keeps the reset tokens (sha256 only) and the admin
// password hash written by a reset.
type CredentialStore interface {
	GetCredential(ctx context.Context, username string) (domain.AdminCredential, bool, error)
	CreateReset(ctx context.Context, tokenHash string, expiresAt time.Time) error
	// ConsumeReset marks the token used (and every other open token), saves
	// newHash and bumps the session version, in one transaction. ok=false when
	// the token is unknown, used or expired.
	ConsumeReset(ctx context.Context, tokenHash, username, newHash string, now time.Time) (domain.AdminCredential, bool, error)
}

// Mailer sends one e-mail and returns the provider message id.
type Mailer interface {
	Send(ctx context.Context, to, subject, htmlBody, textBody string) (string, error)
}

// PasswordReset enables POST /api/auth/forgot and /api/auth/reset. The link
// goes only to To (never to an address from the request).
type PasswordReset struct {
	Mailer   Mailer
	To       string
	FrontURL string // e.g. https://horizon-web-eight.vercel.app
	TTL      time.Duration
	// Limiter counts every forgot request per IP; MaxPerHour caps the e-mails
	// sent overall.
	Limiter    *LoginLimiter
	MaxPerHour int
	Now        func() time.Time

	mu   sync.Mutex
	sent []time.Time
}

// DefaultResetTTL is how long a reset link works.
const DefaultResetTTL = 30 * time.Minute

// MinPasswordLen is the shortest new password a reset accepts.
const MinPasswordLen = 10

// Enabled reports whether password reset is configured.
func (p *PasswordReset) Enabled() bool {
	return p != nil && p.Mailer != nil && p.To != "" && p.FrontURL != ""
}

func (p *PasswordReset) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *PasswordReset) ttl() time.Duration {
	if p.TTL > 0 {
		return p.TTL
	}
	return DefaultResetTTL
}

// allowSend applies the global hourly cap.
func (p *PasswordReset) allowSend() bool {
	max := p.MaxPerHour
	if max <= 0 {
		max = 5
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	keep := p.sent[:0]
	for _, t := range p.sent {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	p.sent = keep
	if len(p.sent) >= max {
		return false
	}
	p.sent = append(p.sent, now)
	return true
}

// HashResetToken is the sha256 (hex) stored for a reset token.
func HashResetToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func newResetToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b64.EncodeToString(b), nil
}

const forgotReply = "Se o e-mail de recuperação estiver configurado, o link chega em instantes."

// forgot (POST /api/auth/forgot, public): always the same 200. When allowed
// it mails a single-use link, valid 30 min, to the configured address.
func (s *Server) forgot(w http.ResponseWriter, r *http.Request) {
	defer writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": forgotReply})
	if !s.Reset.Enabled() || !s.Login.Enabled() || s.Login.Creds == nil {
		s.log().Warn("forgot: password reset not configured")
		return
	}
	ip := clientIP(r)
	if s.Reset.Limiter != nil {
		if s.Reset.Limiter.Blocked(ip) > 0 {
			s.log().Warn("forgot: rate limited", "ip", ip)
			return
		}
		s.Reset.Limiter.Fail(ip) // every request counts
	}
	if !s.Reset.allowSend() {
		s.log().Warn("forgot: hourly e-mail cap reached")
		return
	}
	if err := s.sendReset(r.Context()); err != nil {
		s.log().Error("forgot: send failed", "err", err)
	}
}

func (s *Server) sendReset(ctx context.Context) error {
	tok, err := newResetToken()
	if err != nil {
		return err
	}
	p := s.Reset
	exp := p.now().Add(p.ttl())
	if err := s.Login.Creds.CreateReset(ctx, HashResetToken(tok), exp); err != nil {
		return fmt.Errorf("store token: %w", err)
	}
	link := strings.TrimRight(p.FrontURL, "/") + "/reset?token=" + tok
	mins := int(p.ttl().Minutes())
	text := fmt.Sprintf("Alguém pediu para redefinir a senha do admin do Radar by JpDev.\n\nPara criar uma senha nova, abra este link (vale %d minutos e só funciona uma vez):\n%s\n\nSe não foi você, ignore este e-mail: a senha atual continua valendo.\n", mins, link)
	body := fmt.Sprintf(`<!doctype html><html lang="pt-BR"><body style="margin:0;background:#eef2ee;font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;color:#141414">
<div style="max-width:520px;margin:0 auto;padding:32px 20px">
<div style="background:#fff;border-radius:24px;padding:28px">
<p style="margin:0 0 4px;font-size:12px;letter-spacing:.08em;text-transform:uppercase;color:#2f6b45">Radar by JpDev</p>
<h1 style="margin:0 0 16px;font-size:24px">Redefinir senha</h1>
<p style="margin:0 0 20px;line-height:1.5">Alguém pediu para redefinir a senha do admin. O link vale %d minutos e só funciona uma vez.</p>
<p style="margin:0 0 24px"><a href="%s" style="display:inline-block;background:#141414;color:#fff;text-decoration:none;padding:12px 22px;border-radius:999px;font-weight:600">Criar senha nova</a></p>
<p style="margin:0;font-size:13px;line-height:1.5;color:#55625a">Se não foi você, ignore este e-mail: a senha atual continua valendo.</p>
</div></div></body></html>`, mins, html.EscapeString(link))
	id, err := p.Mailer.Send(ctx, p.To, "Radar by JpDev: redefinir senha do admin", body, text)
	if err != nil {
		return err
	}
	s.log().Info("forgot: reset e-mail sent", "provider_id", id, "expires_at", exp.UTC())
	return nil
}

var errBadReset = errors.New("link inválido ou expirado")

// reset (POST /api/auth/reset {token, new_password}, public): consumes the
// token, saves the new bcrypt hash and ends every existing session.
func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	if !s.Login.Enabled() || s.Login.Creds == nil {
		writeError(w, http.StatusServiceUnavailable, "redefinição indisponível")
		return
	}
	ip := clientIP(r)
	if wait := s.Login.Limiter.Blocked(ip); wait > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds()+0.999)))
		writeError(w, http.StatusTooManyRequests, "muitas tentativas, tente de novo mais tarde")
		return
	}
	var in struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	n := utf8.RuneCountInString(in.NewPassword)
	if n < MinPasswordLen || len(in.NewPassword) > 200 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("a senha precisa ter de %d a 200 caracteres", MinPasswordLen))
		return
	}
	tok := strings.TrimSpace(in.Token)
	if tok == "" || len(tok) > 200 {
		s.Login.Limiter.Fail(ip)
		writeError(w, http.StatusBadRequest, errBadReset.Error())
		return
	}
	h, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), 12)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := time.Now()
	if s.Reset != nil {
		now = s.Reset.now()
	}
	c, ok, err := s.Login.Creds.ConsumeReset(r.Context(), HashResetToken(tok), s.Login.Username, string(h), now)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		s.Login.Limiter.Fail(ip)
		writeError(w, http.StatusBadRequest, errBadReset.Error())
		return
	}
	s.Login.set([]byte(c.PasswordHash), c.SessionVersion)
	s.Login.Limiter.Succeed(ip)
	s.log().Info("password reset: new admin password saved, sessions revoked", "version", c.SessionVersion)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
