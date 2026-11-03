package digest

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/jpdev/horizon-go/internal/domain"
)

// DefaultCron is 07:00 every day.
const DefaultCron = "0 7 * * *"

// DefaultReload is how often the scheduler rereads the settings.
const DefaultReload = time.Minute

// ValidateCron checks a standard 5-field spec (or a descriptor like @daily).
func ValidateCron(spec string) error {
	if _, err := cron.ParseStandard(spec); err != nil {
		return domain.Invalidf("digest_cron %q: %v", spec, err)
	}
	return nil
}

// Scheduler runs Generate on the cron spec of the settings, in the settings
// timezone, and picks up changes to either within Reload.
type Scheduler struct {
	Service *Service
	Log     *slog.Logger
	Reload  time.Duration

	mu    sync.Mutex
	cron  *cron.Cron
	entry cron.EntryID
	spec  string
	tz    string
}

func (s *Scheduler) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

// Next is the next planned run (zero before the first schedule).
func (s *Scheduler) Next() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron == nil {
		return time.Time{}
	}
	return s.cron.Entry(s.entry).Next
}

// Sync (re)schedules from the current settings when the spec or timezone
// changed.
func (s *Scheduler) Sync(ctx context.Context) error {
	st, err := s.Service.Store.GetSettings(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil && st.DigestCron == s.spec && st.Timezone == s.tz {
		return nil
	}
	loc, err := time.LoadLocation(st.Timezone)
	if err != nil {
		return err
	}
	sched, err := cron.ParseStandard(st.DigestCron)
	if err != nil {
		return err
	}
	if s.cron != nil {
		<-s.cron.Stop().Done()
	}
	c := cron.New(cron.WithLocation(loc))
	s.entry = c.Schedule(sched, cron.FuncJob(func() { s.fire(ctx) }))
	c.Start()
	s.cron, s.spec, s.tz = c, st.DigestCron, st.Timezone
	s.log().Info("digest scheduled", "cron", st.DigestCron, "tz", st.Timezone, "next", c.Entry(s.entry).Next)
	return nil
}

func (s *Scheduler) fire(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	d, err := s.Service.Generate(ctx, "")
	if err != nil {
		s.log().Error("scheduled digest failed", "err", err)
		return
	}
	s.log().Info("digest generated", "date", d.Date, "items", d.ItemCount, "considered", d.Considered)
}

// Run syncs at start and every Reload until ctx is done.
func (s *Scheduler) Run(ctx context.Context) error {
	if err := s.Sync(ctx); err != nil {
		return err
	}
	reload := s.Reload
	if reload <= 0 {
		reload = DefaultReload
	}
	t := time.NewTicker(reload)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			if s.cron != nil {
				<-s.cron.Stop().Done()
			}
			s.mu.Unlock()
			return nil
		case <-t.C:
			if err := s.Sync(ctx); err != nil {
				s.log().Error("digest schedule reload", "err", err)
			}
		}
	}
}
