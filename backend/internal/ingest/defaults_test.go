package ingest

import (
	"testing"

	"encoding/json"
	"github.com/jpdev/horizon-go/internal/adapters/source"
	"github.com/jpdev/horizon-go/internal/adapters/source/rss"
)

func TestDefaultSourcesAreValidAndCapped(t *testing.T) {
	names := map[string]bool{}
	total := 0
	for _, s := range DefaultSources() {
		if names[s.Name] {
			t.Errorf("duplicate source name %q", s.Name)
		}
		names[s.Name] = true
		if _, err := source.Build(s, nil, source.BaseURLs{}); err != nil {
			t.Errorf("%s: %v", s.Name, err)
		}
		var c rss.Config
		if err := json.Unmarshal(s.Config, &c); err != nil {
			t.Fatal(err)
		}
		if c.Limit <= 0 {
			t.Errorf("%s: every default feed needs a limit", s.Name)
		}
		total += c.Limit
	}
	// Upper bound of new items per collection; two collections a day share
	// most entries (URL dedup), so the daily scored volume stays near this.
	if total > 120 {
		t.Errorf("default limits add up to %d items per collection", total)
	}
}
