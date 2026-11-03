// Package digest selects the best items of a day (score threshold, per-source
// quota, item cap — a port of apply_balanced_digest in the upstream
// orchestrator, grouping by source) and renders Markdown and HTML by code
// (like upstream/src/ai/summarizer.py: no model call, untrusted text escaped).
package digest

import (
	"sort"

	"github.com/jpdev/horizon-go/internal/domain"
)

// Rules are the selection knobs.
type Rules struct {
	Threshold float64 // minimum score (inclusive)
	PerSource int     // max items per source; 0 = no quota
	MaxItems  int     // final cap; 0 = no cap
}

// RulesFrom reads the rules from the settings.
func RulesFrom(s domain.Settings) Rules {
	return Rules{Threshold: s.ScoreThreshold, PerSource: s.DigestPerSource, MaxItems: s.DigestMaxItems}
}

// Select keeps the items at or above the threshold, sorted by score
// (then most recent, then id), at most PerSource per source and MaxItems in
// total. The input is not modified.
func Select(items []domain.FeedItem, r Rules) []domain.FeedItem {
	sorted := make([]domain.FeedItem, 0, len(items))
	for _, it := range items {
		if it.Score >= r.Threshold {
			sorted = append(sorted, it)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if ta, tb := a.EventTime(), b.EventTime(); !ta.Equal(tb) {
			return ta.After(tb)
		}
		return a.ItemID < b.ItemID
	})
	perSource := map[int64]int{}
	out := make([]domain.FeedItem, 0, len(sorted))
	for _, it := range sorted {
		if r.PerSource > 0 && perSource[it.SourceID] >= r.PerSource {
			continue
		}
		perSource[it.SourceID]++
		out = append(out, it)
		if r.MaxItems > 0 && len(out) == r.MaxItems {
			break
		}
	}
	return out
}
