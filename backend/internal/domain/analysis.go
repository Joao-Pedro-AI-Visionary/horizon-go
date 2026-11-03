package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Analysis is the LLM verdict for one item (rubric score 0–10).
type Analysis struct {
	Score   float64  `json:"score"`
	Reason  string   `json:"reason"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags"`
}

// ContentHash identifies the analyzable content of an item. It keys the AI
// cache, so the same text is never sent to the model twice.
func ContentHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(strings.TrimSpace(p)))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Similarity verdicts for a pair of embeddings.
type Verdict int

const (
	Distinct  Verdict = iota // below the ambiguous band
	Ambiguous                // inside the band: the LLM decides
	Duplicate                // at or above the duplicate threshold
)

// Thresholds are the cosine-similarity cut points of the dedup stage.
type Thresholds struct {
	Duplicate float64 // >= Duplicate is a direct duplicate
	Ambiguous float64 // [Ambiguous, Duplicate) asks the LLM
}

// DefaultThresholds are the values from PLANO.md §5.
var DefaultThresholds = Thresholds{Duplicate: 0.88, Ambiguous: 0.80}

// Classify maps a cosine similarity to a verdict.
func (t Thresholds) Classify(sim float64) Verdict {
	switch {
	case sim >= t.Duplicate:
		return Duplicate
	case sim >= t.Ambiguous:
		return Ambiguous
	default:
		return Distinct
	}
}

// Member is an item as seen by the clustering logic.
type Member struct {
	ItemID      int64
	ClusterID   int64 // 0 when not clustered yet
	Score       float64
	PublishedAt time.Time
}

// PickPrimary returns the member with the highest score; ties go to the
// earliest publication, then to the lowest id. It panics on an empty slice.
func PickPrimary(ms []Member) Member {
	best := ms[0]
	for _, m := range ms[1:] {
		switch {
		case m.Score > best.Score:
			best = m
		case m.Score < best.Score:
		case !m.PublishedAt.Equal(best.PublishedAt):
			if !m.PublishedAt.IsZero() && (best.PublishedAt.IsZero() || m.PublishedAt.Before(best.PublishedAt)) {
				best = m
			}
		case m.ItemID < best.ItemID:
			best = m
		}
	}
	return best
}

// MergePlan says how a new item joins the existing clusters.
type MergePlan struct {
	// Target is the cluster that survives; 0 means create a new one.
	Target int64
	// Absorb lists other clusters whose members move into Target.
	Absorb []int64
}

// PlanMerge decides the cluster for an item that matched the given
// neighbours: none -> new cluster; otherwise the lowest matched cluster id
// survives and the others are absorbed (the item bridges them).
func PlanMerge(matched []Member) MergePlan {
	seen := map[int64]bool{}
	var ids []int64
	for _, m := range matched {
		if m.ClusterID != 0 && !seen[m.ClusterID] {
			seen[m.ClusterID] = true
			ids = append(ids, m.ClusterID)
		}
	}
	if len(ids) == 0 {
		return MergePlan{}
	}
	target := ids[0]
	for _, id := range ids[1:] {
		if id < target {
			target = id
		}
	}
	var absorb []int64
	for _, id := range ids {
		if id != target {
			absorb = append(absorb, id)
		}
	}
	return MergePlan{Target: target, Absorb: absorb}
}
