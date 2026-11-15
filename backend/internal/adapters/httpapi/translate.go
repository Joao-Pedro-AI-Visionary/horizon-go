package httpapi

import (
	"io"
	"net/http"
	"strings"

	"github.com/jpdev/horizon-go/internal/domain"
)

// maxTranslateLimit mirrors enrich.MaxTranslateBatch (the worker clamps too).
const maxTranslateLimit = 500

// translateRequest is the optional body of POST /api/pipeline/translate.
type translateRequest struct {
	Limit int `json:"limit"`
}

// translateResponse is the answer of POST /api/pipeline/translate: the
// counters at request time plus what was queued. The sweep itself runs in
// the worker; poll GET /api/pipeline/translate for progress.
type translateResponse struct {
	domain.TranslationStatus
	Queued bool `json:"queued"`
	Limit  int  `json:"limit"`
}

// translationStatus (GET /api/pipeline/translate, admin only) reports the
// pt-BR backfill counters.
func (s *Server) translationStatus(w http.ResponseWriter, r *http.Request) {
	if s.Translations == nil {
		writeError(w, http.StatusNotImplemented, "translation is not configured")
		return
	}
	st, err := s.Translations.TranslationStatus(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// requestTranslation (POST /api/pipeline/translate, admin only) asks the
// worker for one translation sweep of up to limit items (body optional,
// {"limit": N}, 1..500; empty = the worker's batch). Items that already have
// title_pt are never translated again. Answers 202 with the counters.
func (s *Server) requestTranslation(w http.ResponseWriter, r *http.Request) {
	if s.Translations == nil {
		writeError(w, http.StatusNotImplemented, "translation is not configured")
		return
	}
	var req translateRequest
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.fail(w, r, domain.Invalidf("body: %v", err))
		return
	}
	if strings.TrimSpace(string(body)) != "" {
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		if err := decode(r, &req); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if req.Limit < 0 || req.Limit > maxTranslateLimit {
		s.fail(w, r, domain.Invalidf("limit must be between 1 and %d", maxTranslateLimit))
		return
	}
	if err := s.Translations.RequestTranslation(r.Context(), req.Limit); err != nil {
		s.fail(w, r, err)
		return
	}
	st, err := s.Translations.TranslationStatus(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.log().Info("translation sweep requested", "limit", req.Limit, "remaining", st.Remaining)
	writeJSON(w, http.StatusAccepted, translateResponse{TranslationStatus: st, Queued: true, Limit: req.Limit})
}
