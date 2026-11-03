package domain

import (
	"reflect"
	"testing"
	"time"
)

func TestThresholdsClassify(t *testing.T) {
	th := DefaultThresholds
	cases := []struct {
		sim  float64
		want Verdict
	}{
		{1.0, Duplicate},
		{0.88, Duplicate},
		{0.8799, Ambiguous},
		{0.84, Ambiguous},
		{0.80, Ambiguous},
		{0.7999, Distinct},
		{0.1, Distinct},
		{-0.5, Distinct},
	}
	for _, c := range cases {
		if got := th.Classify(c.sim); got != c.want {
			t.Errorf("Classify(%v) = %v, want %v", c.sim, got, c.want)
		}
	}
}

func TestPickPrimary(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		ms   []Member
		want int64
	}{
		{"highest score wins", []Member{{ItemID: 1, Score: 6}, {ItemID: 2, Score: 8.5}, {ItemID: 3, Score: 7}}, 2},
		{"tie goes to earliest publication", []Member{{ItemID: 1, Score: 7, PublishedAt: t0.Add(time.Hour)}, {ItemID: 2, Score: 7, PublishedAt: t0}}, 2},
		{"known date beats unknown", []Member{{ItemID: 1, Score: 7}, {ItemID: 2, Score: 7, PublishedAt: t0}}, 2},
		{"full tie goes to lowest id", []Member{{ItemID: 9, Score: 7, PublishedAt: t0}, {ItemID: 4, Score: 7, PublishedAt: t0}}, 4},
		{"single", []Member{{ItemID: 5, Score: 1}}, 5},
	}
	for _, c := range cases {
		if got := PickPrimary(c.ms).ItemID; got != c.want {
			t.Errorf("%s: primary = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestPlanMerge(t *testing.T) {
	cases := []struct {
		name    string
		matched []Member
		want    MergePlan
	}{
		{"no match creates cluster", nil, MergePlan{}},
		{"single cluster", []Member{{ItemID: 1, ClusterID: 7}, {ItemID: 2, ClusterID: 7}}, MergePlan{Target: 7}},
		{"bridge merges into lowest id", []Member{{ItemID: 1, ClusterID: 9}, {ItemID: 2, ClusterID: 3}, {ItemID: 3, ClusterID: 5}},
			MergePlan{Target: 3, Absorb: []int64{9, 5}}},
		{"unclustered neighbours ignored", []Member{{ItemID: 1}}, MergePlan{}},
	}
	for _, c := range cases {
		if got := PlanMerge(c.matched); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: plan = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestContentHashStable(t *testing.T) {
	a := ContentHash("Title", "body")
	if a != ContentHash(" Title ", "body\n") {
		t.Error("hash should ignore surrounding whitespace")
	}
	if a == ContentHash("Titlebody") || a == ContentHash("Title", "other") {
		t.Error("hash must separate fields and change with content")
	}
}
