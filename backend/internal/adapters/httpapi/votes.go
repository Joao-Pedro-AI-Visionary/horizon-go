package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
)

const (
	deviceCookieName      = "horizon_did"
	deviceHeaderName      = "X-Device-Id"
	defaultVoteMinDwell   = 3 * time.Second
	defaultVoteRateWindow = 5 * time.Minute
	defaultVoteRateMax    = 30
	defaultVoteRateTTL    = 24 * time.Hour
)

var (
	fallbackVoteSecretOnce sync.Once
	fallbackVoteSecret     []byte
)

func (s *Server) voteSecret() []byte {
	if len(s.VoteHashSecret) > 0 {
		return s.VoteHashSecret
	}
	fallbackVoteSecretOnce.Do(func() {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			buf = []byte("horizon-ephemeral-vote-key-fallback")
		}
		fallbackVoteSecret = buf
	})
	return fallbackVoteSecret
}

func (s *Server) voteMinDwell() time.Duration {
	if s.VoteMinDwell < 0 {
		return 0
	}
	if s.VoteMinDwell == 0 {
		return defaultVoteMinDwell
	}
	return s.VoteMinDwell
}

func (s *Server) voteRateLimits() (window time.Duration, maxVotes int, ttl time.Duration) {
	window = s.VoteRateWindow
	if window <= 0 {
		window = defaultVoteRateWindow
	}
	maxVotes = s.VoteRateMax
	if maxVotes <= 0 {
		maxVotes = defaultVoteRateMax
	}
	ttl = s.VoteRateTTL
	if ttl <= 0 {
		ttl = defaultVoteRateTTL
	}
	return window, maxVotes, ttl
}

func validDeviceID(raw string) bool {
	if len(raw) < 16 || len(raw) > 128 {
		return false
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

func readDeviceID(r *http.Request) string {
	if c, err := r.Cookie(deviceCookieName); err == nil {
		if v := strings.TrimSpace(c.Value); validDeviceID(v) {
			return v
		}
	}
	if v := strings.TrimSpace(r.Header.Get(deviceHeaderName)); validDeviceID(v) {
		return v
	}
	return ""
}

func (s *Server) hmacHex(purpose, raw string) string {
	mac := hmac.New(sha256.New, s.voteSecret())
	mac.Write([]byte(purpose))
	mac.Write([]byte{0})
	mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) deviceHashFromReq(r *http.Request) string {
	raw := readDeviceID(r)
	if raw == "" {
		return ""
	}
	return s.hmacHex("did", raw)
}

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func (s *Server) ensureDeviceID(w http.ResponseWriter, r *http.Request) string {
	raw := readDeviceID(r)
	if raw == "" {
		buf := make([]byte, 16)
		_, _ = rand.Read(buf)
		raw = hex.EncodeToString(buf)
	}
	cookieVal := ""
	if c, err := r.Cookie(deviceCookieName); err == nil {
		cookieVal = strings.TrimSpace(c.Value)
	}
	if cookieVal != raw {
		secure := isHTTPS(r)
		sameSite := http.SameSiteLaxMode
		if secure {
			sameSite = http.SameSiteNoneMode
		}
		http.SetCookie(w, &http.Cookie{
			Name:     deviceCookieName,
			Value:    raw,
			Path:     "/",
			MaxAge:   365 * 24 * 60 * 60,
			HttpOnly: true,
			Secure:   secure,
			SameSite: sameSite,
		})
	}
	return raw
}

type voteReq struct {
	Vote    int   `json:"vote"`
	DwellMS int64 `json:"dwell_ms"`
}

func (s *Server) voteItem(w http.ResponseWriter, r *http.Request) {
	if s.Votes == nil {
		writeError(w, http.StatusServiceUnavailable, "voting unavailable")
		return
	}
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var req voteReq
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if req.Vote != -1 && req.Vote != 0 && req.Vote != 1 {
		s.fail(w, r, domain.Invalidf("vote must be -1, 0 or 1"))
		return
	}
	minDwell := s.voteMinDwell()
	if req.Vote != 0 && req.DwellMS < minDwell.Milliseconds() {
		s.fail(w, r, domain.Invalidf("leia um pouco mais antes de votar"))
		return
	}
	win, maxVotes, ttl := s.voteRateLimits()
	ip := clientIP(r)
	if ip != "" {
		retryAfter, allowed, err := s.Votes.CheckAndRecordRate(r.Context(), s.hmacHex("ip", ip), win, maxVotes, ttl, time.Now().UTC())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if !allowed {
			secs := int(retryAfter.Seconds() + 0.999)
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeError(w, http.StatusTooManyRequests, "muitos votos em pouco tempo, tente novamente em instantes")
			return
		}
	}
	rawID := s.ensureDeviceID(w, r)
	res, err := s.Votes.Vote(r.Context(), id, s.hmacHex("did", rawID), req.Vote)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
