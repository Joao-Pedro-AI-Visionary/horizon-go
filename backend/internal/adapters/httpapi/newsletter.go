package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jpdev/horizon-go/internal/newsletter"
)

// NewsletterService handles subscriptions and daily digest dispatches.
type NewsletterService interface {
	Subscribe(ctx context.Context, email string) (newsletter.SubscribeResult, error)
	Confirm(ctx context.Context, token string) (newsletter.SubscribeResult, error)
	Unsubscribe(ctx context.Context, token string) (newsletter.SubscribeResult, error)
	SendDaily(ctx context.Context, at time.Time, dryRun, force bool) (newsletter.SendReport, error)
}

func (s *Server) newsletterRateLimiter() *LoginLimiter {
	if s.NewsletterLimiter != nil {
		return s.NewsletterLimiter
	}
	return nil
}

func (s *Server) subscribeNewsletter(w http.ResponseWriter, r *http.Request) {
	if s.Newsletter == nil {
		writeError(w, http.StatusServiceUnavailable, "newsletter indisponível")
		return
	}
	if lim := s.newsletterRateLimiter(); lim != nil {
		ip := clientIP(r)
		if wait := lim.Blocked(ip); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds()+0.999)))
			writeError(w, http.StatusTooManyRequests, "muitas tentativas em sequência, tente em instantes")
			return
		}
		lim.Fail(ip)
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := decode(r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := s.Newsletter.Subscribe(r.Context(), body.Email)
	if err != nil {
		if errors.Is(err, newsletter.ErrMailerDisabled) {
			writeError(w, http.StatusServiceUnavailable, "envio de e-mail não configurado neste ambiente")
			return
		}
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) confirmNewsletter(w http.ResponseWriter, r *http.Request) {
	if s.Newsletter == nil {
		writeError(w, http.StatusServiceUnavailable, "newsletter indisponível")
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if r.Method == http.MethodPost {
		var body struct {
			Token string `json:"token"`
		}
		if r.ContentLength != 0 {
			if err := decode(r, &body); err != nil {
				s.fail(w, r, err)
				return
			}
			if body.Token != "" {
				token = strings.TrimSpace(body.Token)
			}
		}
	}
	res, err := s.Newsletter.Confirm(r.Context(), token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) unsubscribeNewsletter(w http.ResponseWriter, r *http.Request) {
	if s.Newsletter == nil {
		writeError(w, http.StatusServiceUnavailable, "newsletter indisponível")
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if r.Method == http.MethodPost && r.ContentLength != 0 {
		var body struct {
			Token string `json:"token"`
		}
		if err := decode(r, &body); err != nil {
			s.fail(w, r, err)
			return
		}
		if body.Token != "" {
			token = strings.TrimSpace(body.Token)
		}
	}
	res, err := s.Newsletter.Unsubscribe(r.Context(), token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) sendNewsletter(w http.ResponseWriter, r *http.Request) {
	if s.Newsletter == nil {
		writeError(w, http.StatusServiceUnavailable, "newsletter indisponível")
		return
	}
	var body struct {
		DryRun bool `json:"dry_run"`
		Force  bool `json:"force"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &body); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	rep, err := s.Newsletter.SendDaily(r.Context(), time.Now(), body.DryRun, body.Force)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
