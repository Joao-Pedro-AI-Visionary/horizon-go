package digest

import (
	"context"
	"fmt"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
)

// Store is what the generator reads and writes.
type Store interface {
	GetSettings(ctx context.Context) (domain.Settings, error)
	// DigestCandidates returns the feed_view rows whose event time is in [from, to).
	DigestCandidates(ctx context.Context, from, to time.Time) ([]domain.FeedItem, error)
	// SaveDigest upserts by (date, language).
	SaveDigest(ctx context.Context, d domain.Digest) (domain.Digest, error)
}

// Service generates digests.
type Service struct {
	Store Store
	Now   func() time.Time // defaults to time.Now
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// Window is the 24 hours covered by the digest of date (in loc): they end at
// the end of that day, or now when the day is not over yet. The cron at 07:00
// thus covers 07:00 of the previous day to 07:00 today.
func Window(date time.Time, now time.Time, loc *time.Location) (from, to time.Time) {
	y, m, d := date.In(loc).Date()
	end := time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	if now.Before(end) {
		end = now
	}
	return end.Add(-24 * time.Hour), end
}

// Generate builds, renders and saves the digest of date (YYYY-MM-DD in the
// settings timezone; "" = today). Generating the same date again replaces it.
func (s *Service) Generate(ctx context.Context, date string) (domain.Digest, error) {
	st, err := s.Store.GetSettings(ctx)
	if err != nil {
		return domain.Digest{}, err
	}
	loc, err := time.LoadLocation(st.Timezone)
	if err != nil {
		return domain.Digest{}, fmt.Errorf("timezone %q: %w", st.Timezone, err)
	}
	now := s.now()
	day := now.In(loc)
	if date != "" {
		if day, err = time.ParseInLocation(time.DateOnly, date, loc); err != nil {
			return domain.Digest{}, domain.Invalidf("date must be YYYY-MM-DD")
		}
		if day.After(now) {
			return domain.Digest{}, domain.Invalidf("date %s is in the future", date)
		}
	}
	from, to := Window(day, now, loc)
	cands, err := s.Store.DigestCandidates(ctx, from, to)
	if err != nil {
		return domain.Digest{}, err
	}
	picked := Select(cands, RulesFrom(st))
	v := View{Date: day.Format(time.DateOnly), Threshold: st.ScoreThreshold, Considered: len(cands), Items: picked, Loc: loc}
	page, err := HTML(v)
	if err != nil {
		return domain.Digest{}, fmt.Errorf("render html: %w", err)
	}
	ids := make([]int64, len(picked))
	for i, it := range picked {
		ids[i] = it.ItemID
	}
	return s.Store.SaveDigest(ctx, domain.Digest{
		Date: v.Date, Language: Language, Title: v.Title(), Markdown: Markdown(v), HTML: page,
		ItemIDs: ids, ItemCount: len(ids), Considered: len(cands), WindowStart: from, WindowEnd: to,
	})
}
