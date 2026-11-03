// Package httpapi is the REST API (chi) consumed by the Vue dashboard.
//
// Reads of the feed go to the feed_view projection only; commands (sources,
// settings, fetch, pipeline run, digest generation) go to the stores and the
// collector, whose items reach the worker through NATS.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
)

// Store is the read side plus settings.
type Store interface {
	Ping(ctx context.Context) error
	Feed(ctx context.Context, q domain.FeedQuery) (domain.FeedPage, error)
	FeedTimeline(ctx context.Context, q domain.FeedQuery, days int) (domain.FeedTimeline, error)
	Item(ctx context.Context, id int64) (domain.ItemDetail, error)
	ListDigests(ctx context.Context, limit int) ([]domain.Digest, error)
	GetDigest(ctx context.Context, date, language string) (domain.Digest, error)
	ListSourceStatus(ctx context.Context) ([]postgres.SourceStatus, error)
	GetSettings(ctx context.Context) (domain.Settings, error)
	PutSettings(ctx context.Context, s domain.Settings) (domain.Settings, error)
	Metrics(ctx context.Context, days int) (domain.Metrics, error)
}

// Digester generates a digest ("" = today).
type Digester interface {
	Generate(ctx context.Context, date string) (domain.Digest, error)
}

// Collector triggers real collections (items go to NATS items.raw).
type Collector interface {
	Collect(ctx context.Context, since time.Time) (app.Report, error)
	CollectSource(ctx context.Context, id int64, since time.Time) (app.Report, error)
}

// Check is a named dependency probe for /healthz.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// Server wires the handlers.
type Server struct {
	Store     Store
	Sources   ports.SourceRepo
	Digests   Digester
	Collector Collector
	// ValidateSource builds the adapter to reject bad kinds/configs (400).
	ValidateSource func(domain.SourceConfig) error
	// ValidateCron checks the digest cron spec.
	ValidateCron func(string) error
	Checks       []Check
	// Window is how far back a triggered collection looks (default 24h).
	Window         time.Duration
	AllowedOrigins []string
	Log            *slog.Logger

	collecting sync.Mutex
}

func (s *Server) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

// Handler returns the router.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, s.requestLog, middleware.Recoverer, s.cors)
	r.Get("/healthz", s.healthz)
	r.Route("/api", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				req.Body = http.MaxBytesReader(w, req.Body, 1<<20)
				next.ServeHTTP(w, req)
			})
		})
		r.Get("/feed", s.feed)
		r.Get("/feed/timeline", s.feedTimeline)
		r.Get("/items/{id}", s.item)
		r.Get("/digests", s.listDigests)
		r.Post("/digests/generate", s.generateDigest)
		r.Get("/digests/{date}", s.getDigest)
		r.Get("/sources", s.listSources)
		r.Post("/sources", s.createSource)
		r.Patch("/sources/{id}", s.patchSource)
		r.Delete("/sources/{id}", s.deleteSource)
		r.Post("/sources/{id}/fetch", s.fetchSource)
		r.Get("/settings", s.getSettings)
		r.Put("/settings", s.putSettings)
		r.Get("/metrics", s.metrics)
		r.Post("/pipeline/run", s.runPipeline)
	})
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	})
	return r
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.log().Info("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(),
			"ms", time.Since(start).Milliseconds(), "req", middleware.GetReqID(r.Context()))
	})
}

// cors allows the configured origins (the Vite dev server) and answers
// preflight requests.
func (s *Server) cors(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range s.AllowedOrigins {
		allowed[strings.TrimRight(strings.TrimSpace(o), "/")] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		ok := origin != "" && (allowed[origin] || allowed["*"])
		if origin != "" {
			w.Header().Add("Vary", "Origin")
		}
		if ok {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if !ok {
				writeError(w, http.StatusForbidden, "origin not allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-Request-Id")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// fail maps store/domain errors to a status; unexpected errors are logged
// and hidden behind a generic 500.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, postgres.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, postgres.ErrConflict):
		writeError(w, http.StatusConflict, "already exists")
	case errors.Is(err, context.Canceled):
		writeError(w, 499, "request cancelled")
	default:
		s.log().Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err, "req", middleware.GetReqID(r.Context()))
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// decode reads a JSON body strictly (unknown fields are a 400).
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return domain.Invalidf("body: %v", err)
	}
	if dec.More() {
		return domain.Invalidf("body: trailing data")
	}
	return nil
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	out := map[string]string{"status": "ok"}
	status := http.StatusOK
	for _, c := range s.Checks {
		if err := c.Fn(ctx); err != nil {
			out[c.Name] = "error: " + err.Error()
			out["status"] = "degraded"
			status = http.StatusServiceUnavailable
		} else {
			out[c.Name] = "ok"
		}
	}
	writeJSON(w, status, out)
}
