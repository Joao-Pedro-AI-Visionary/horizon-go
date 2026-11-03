package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/domain"
)

const (
	defaultMetricsDays = 14
	maxMetricsDays     = 90
	defaultDigestLimit = 60
	maxDigestLimit     = 365
	maxQueryRunes      = 200
)

var sourceKinds = map[string]bool{domain.KindHackerNews: true, domain.KindReddit: true, domain.KindRSS: true}

var periods = map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

func intParam(v url.Values, key string, def, lo, hi int) (int, error) {
	raw := strings.TrimSpace(v.Get(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		return 0, domain.Invalidf("%s must be an integer within %d..%d", key, lo, hi)
	}
	return n, nil
}

func timeParam(v url.Values, key string) (time.Time, error) {
	raw := strings.TrimSpace(v.Get(key))
	if raw == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.DateOnly, raw); err == nil {
		return t, nil
	}
	return time.Time{}, domain.Invalidf("%s must be RFC 3339 or YYYY-MM-DD", key)
}

// parseFeedQuery validates the /api/feed query string.
func parseFeedQuery(v url.Values, now time.Time) (domain.FeedQuery, error) {
	q := domain.FeedQuery{
		SourceKind: strings.ToLower(strings.TrimSpace(v.Get("source"))),
		Tag:        strings.TrimSpace(v.Get("tag")),
		Q:          strings.TrimSpace(v.Get("q")),
		Sort:       strings.TrimSpace(v.Get("sort")),
	}
	if q.SourceKind != "" && !sourceKinds[q.SourceKind] {
		return q, domain.Invalidf("source must be one of hackernews, reddit, rss")
	}
	if raw := strings.TrimSpace(v.Get("source_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return q, domain.Invalidf("source_id must be a positive integer")
		}
		q.SourceID = id
	}
	if len([]rune(q.Q)) > maxQueryRunes || len([]rune(q.Tag)) > maxQueryRunes {
		return q, domain.Invalidf("q and tag are limited to %d characters", maxQueryRunes)
	}
	if raw := strings.TrimSpace(v.Get("min_score")); raw != "" {
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil || f < domain.MinScore || f > domain.MaxScore {
			return q, domain.Invalidf("min_score must be a number within 0..10")
		}
		q.MinScore = &f
	}
	switch q.Sort {
	case "":
		q.Sort = domain.SortScore
	case domain.SortScore, domain.SortRecent:
	default:
		return q, domain.Invalidf("sort must be score or recent")
	}
	if p := strings.TrimSpace(v.Get("period")); p != "" && p != "all" {
		d, ok := periods[p]
		if !ok {
			return q, domain.Invalidf("period must be 24h, 7d, 30d or all")
		}
		q.From = now.Add(-d)
	}
	from, err := timeParam(v, "from")
	if err != nil {
		return q, err
	}
	to, err := timeParam(v, "to")
	if err != nil {
		return q, err
	}
	if !from.IsZero() {
		q.From = from
	}
	q.To = to
	if !q.From.IsZero() && !q.To.IsZero() && !q.From.Before(q.To) {
		return q, domain.Invalidf("from must be before to")
	}
	if q.Page, err = intParam(v, "page", 1, 1, 100_000); err != nil {
		return q, err
	}
	if q.PageSize, err = intParam(v, "page_size", domain.DefaultPageSize, 1, domain.MaxPageSize); err != nil {
		return q, err
	}
	return q, nil
}

func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	q, err := parseFeedQuery(r.URL.Query(), time.Now())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.Store.Feed(r.Context(), q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// feedTimeline is the per-day histogram of the feed for the date scrubber.
// It takes the same filters as /api/feed except the time window (period,
// from, to), which is what the scrubber selects.
func (s *Server) feedTimeline(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	days, err := intParam(v, "days", domain.DefaultTimelineDays, 1, domain.MaxTimelineDays)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, k := range []string{"period", "from", "to", "page", "page_size", "sort"} {
		v.Del(k)
	}
	q, err := parseFeedQuery(v, time.Now())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tl, err := s.Store.FeedTimeline(r.Context(), q, days)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tl)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, domain.Invalidf("id must be a positive integer")
	}
	return id, nil
}

func (s *Server) item(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	it, err := s.Store.Item(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) listDigests(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r.URL.Query(), "limit", defaultDigestLimit, 1, maxDigestLimit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ds, err := s.Store.ListDigests(r.Context(), limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"digests": ds})
}

func (s *Server) getDigest(w http.ResponseWriter, r *http.Request) {
	date := chi.URLParam(r, "date")
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		s.fail(w, r, domain.Invalidf("date must be YYYY-MM-DD"))
		return
	}
	lang := r.URL.Query().Get("language")
	if lang == "" {
		lang = "pt-BR"
	}
	d, err := s.Store.GetDigest(r.Context(), date, lang)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	switch r.URL.Query().Get("format") {
	case "", "json":
		writeJSON(w, http.StatusOK, d)
	case "md", "markdown":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(d.Markdown))
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src https:")
		_, _ = w.Write([]byte(d.HTML))
	default:
		s.fail(w, r, domain.Invalidf("format must be json, md or html"))
	}
}

func (s *Server) generateDigest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Date string `json:"date"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &body); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	d, err := s.Digests.Generate(r.Context(), strings.TrimSpace(body.Date))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

// fetchRunDTO is the JSON form of a fetch run.
type fetchRunDTO struct {
	SourceID   int64     `json:"source_id"`
	SourceName string    `json:"source_name"`
	RunID      string    `json:"run_id"`
	Status     string    `json:"status"`
	ItemCount  int       `json:"item_count"`
	Error      string    `json:"error"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

func toFetchRun(f domain.FetchRun) fetchRunDTO {
	return fetchRunDTO{SourceID: f.SourceID, SourceName: f.SourceName, RunID: f.RunID, Status: f.Status,
		ItemCount: f.ItemCount, Error: f.Error, StartedAt: f.StartedAt, FinishedAt: f.FinishedAt}
}

// sourceDTO is the JSON form of a source.
type sourceDTO struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Config    json.RawMessage `json:"config"`
	Enabled   bool            `json:"enabled"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	ItemCount *int            `json:"item_count,omitempty"`
	LastFetch *fetchRunDTO    `json:"last_fetch"`
}

func toSource(c domain.SourceConfig) sourceDTO {
	cfg := c.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage("{}")
	}
	return sourceDTO{ID: c.ID, Kind: c.Kind, Name: c.Name, Config: cfg, Enabled: c.Enabled, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.ListSourceStatus(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]sourceDTO, len(rows))
	for i, row := range rows {
		out[i] = toSource(row.SourceConfig)
		n := row.Items
		out[i].ItemCount = &n
		if row.LastFetch != nil {
			f := toFetchRun(*row.LastFetch)
			out[i].LastFetch = &f
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": out})
}

// sourceInput is the body of POST (all required but enabled) and PATCH
// (all optional) /api/sources.
type sourceInput struct {
	Kind    *string          `json:"kind"`
	Name    *string          `json:"name"`
	Config  *json.RawMessage `json:"config"`
	Enabled *bool            `json:"enabled"`
}

func (in sourceInput) apply(c *domain.SourceConfig) {
	if in.Kind != nil {
		c.Kind = strings.TrimSpace(*in.Kind)
	}
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	if in.Config != nil {
		c.Config = *in.Config
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
}

func (s *Server) validateSource(c domain.SourceConfig) error {
	if !sourceKinds[c.Kind] {
		return domain.Invalidf("kind must be one of hackernews, reddit, rss")
	}
	if c.Name == "" || len([]rune(c.Name)) > 100 {
		return domain.Invalidf("name is required (up to 100 characters)")
	}
	if len(c.Config) > 0 {
		var obj map[string]any
		if err := json.Unmarshal(c.Config, &obj); err != nil || obj == nil {
			return domain.Invalidf("config must be a JSON object")
		}
	}
	if s.ValidateSource != nil {
		if err := s.ValidateSource(c); err != nil {
			return domain.Invalidf("%v", err)
		}
	}
	return nil
}

func (s *Server) createSource(w http.ResponseWriter, r *http.Request) {
	var in sourceInput
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Kind == nil || in.Name == nil {
		s.fail(w, r, domain.Invalidf("kind and name are required"))
		return
	}
	c := domain.SourceConfig{Enabled: true}
	in.apply(&c)
	if err := s.validateSource(c); err != nil {
		s.fail(w, r, err)
		return
	}
	created, err := s.Sources.Create(r.Context(), c)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSource(created))
}

func (s *Server) patchSource(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var in sourceInput
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	c, err := s.Sources.Get(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	in.apply(&c)
	if err := s.validateSource(c); err != nil {
		s.fail(w, r, err)
		return
	}
	updated, err := s.Sources.Update(r.Context(), c)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSource(updated))
}

func (s *Server) deleteSource(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.Sources.Delete(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reportDTO is the JSON form of a collector run.
type reportDTO struct {
	RunID      string        `json:"run_id"`
	Sources    []fetchRunDTO `json:"sources"`
	Fetched    int           `json:"fetched"`
	Invalid    int           `json:"invalid"`
	Duplicates int           `json:"duplicates"`
	Published  int           `json:"published"`
	Error      string        `json:"error,omitempty"`
}

func toReport(rep app.Report, err error) reportDTO {
	out := reportDTO{RunID: rep.RunID, Sources: make([]fetchRunDTO, len(rep.Sources)),
		Fetched: rep.Fetched, Invalid: rep.Invalid, Duplicates: rep.Duplicates, Published: rep.Published}
	for i, f := range rep.Sources {
		out.Sources[i] = toFetchRun(f)
	}
	if err != nil {
		out.Error = err.Error()
	}
	return out
}

func (s *Server) window() time.Duration {
	if s.Window <= 0 {
		return 24 * time.Hour
	}
	return s.Window
}

// collect runs one collection at a time; a second request gets 409.
func (s *Server) collect(w http.ResponseWriter, r *http.Request, run func() (app.Report, error)) {
	if !s.collecting.TryLock() {
		writeError(w, http.StatusConflict, "a collection is already running")
		return
	}
	defer s.collecting.Unlock()
	rep, err := run()
	if err != nil && rep.RunID == "" {
		s.fail(w, r, err)
		return
	}
	// Per-source failures are in the report (and in fetch_runs); a partial
	// failure to publish or record is reported as 502 with the report.
	status := http.StatusOK
	if err != nil {
		s.log().Error("collection finished with errors", "run", rep.RunID, "err", err)
		status = http.StatusBadGateway
	}
	writeJSON(w, status, toReport(rep, err))
}

func (s *Server) fetchSource(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	since := time.Now().Add(-s.window())
	s.collect(w, r, func() (app.Report, error) { return s.Collector.CollectSource(r.Context(), id, since) })
}

func (s *Server) runPipeline(w http.ResponseWriter, r *http.Request) {
	since := time.Now().Add(-s.window())
	s.collect(w, r, func() (app.Report, error) { return s.Collector.Collect(r.Context(), since) })
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.GetSettings(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ScoreThreshold  *float64 `json:"score_threshold"`
		DigestMaxItems  *int     `json:"digest_max_items"`
		DigestPerSource *int     `json:"digest_per_source"`
		DigestCron      *string  `json:"digest_cron"`
		Timezone        *string  `json:"timezone"`
		UpdatedAt       *string  `json:"updated_at"` // ignored, so a GET body can be sent back
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.ScoreThreshold == nil || in.DigestMaxItems == nil || in.DigestPerSource == nil || in.DigestCron == nil {
		s.fail(w, r, domain.Invalidf("score_threshold, digest_max_items, digest_per_source and digest_cron are required"))
		return
	}
	st := domain.Settings{ScoreThreshold: *in.ScoreThreshold, DigestMaxItems: *in.DigestMaxItems,
		DigestPerSource: *in.DigestPerSource, DigestCron: strings.TrimSpace(*in.DigestCron)}
	if in.Timezone != nil {
		st.Timezone = strings.TrimSpace(*in.Timezone)
	} else {
		cur, err := s.Store.GetSettings(r.Context())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		st.Timezone = cur.Timezone
	}
	if err := st.Validate(); err != nil {
		s.fail(w, r, err)
		return
	}
	if s.ValidateCron != nil {
		if err := s.ValidateCron(st.DigestCron); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	saved, err := s.Store.PutSettings(r.Context(), st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	days, err := intParam(r.URL.Query(), "days", defaultMetricsDays, 1, maxMetricsDays)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	m, err := s.Store.Metrics(r.Context(), days)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

var _ Store = postgres.ReadRepo{}
