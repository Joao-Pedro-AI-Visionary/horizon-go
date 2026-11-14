package config

import (
	"testing"
	"time"
)

func TestCollectInterval(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", DefaultCollectInterval},
		{"off", 0},
		{"OFF", 0},
		{"0", 0},
		{"none", 0},
		{"false", 0},
		{"15m", 15 * time.Minute},
		{"0s", 0},
		{"-5m", DefaultCollectInterval},
		{"bogus", DefaultCollectInterval},
	}
	for _, tc := range cases {
		if got := CollectInterval(tc.raw, DefaultCollectInterval); got != tc.want {
			t.Errorf("CollectInterval(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestResolveInternalScheduler(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name    string
		env     map[string]string
		want    bool
		wantErr bool
	}{
		{"render default (COLLECT_INTERVAL=off)", map[string]string{"COLLECT_INTERVAL": "off"}, true, false},
		{"COLLECT_INTERVAL=0", map[string]string{"COLLECT_INTERVAL": "0"}, true, false},
		{"local default (unset)", map[string]string{}, false, false},
		{"explicit interval", map[string]string{"COLLECT_INTERVAL": "30m"}, false, false},
		{"explicitly disabled with off interval", map[string]string{"COLLECT_INTERVAL": "off", "INTERNAL_SCHEDULER": "false"}, false, false},
		{"explicitly enabled with off interval", map[string]string{"COLLECT_INTERVAL": "off", "INTERNAL_SCHEDULER": "true"}, true, false},
		{"conflict: interval active and scheduler forced on", map[string]string{"COLLECT_INTERVAL": "30m", "INTERNAL_SCHEDULER": "true"}, false, true},
		{"conflict: unset interval (worker 30m) and scheduler forced on", map[string]string{"INTERNAL_SCHEDULER": "on"}, false, true},
		{"invalid override", map[string]string{"COLLECT_INTERVAL": "off", "INTERNAL_SCHEDULER": "maybe"}, false, true},
	}
	for _, tc := range cases {
		got, _, err := ResolveInternalScheduler(env(tc.env))
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
		if got != tc.want {
			t.Fatalf("%s: got=%v want=%v", tc.name, got, tc.want)
		}
	}
}
