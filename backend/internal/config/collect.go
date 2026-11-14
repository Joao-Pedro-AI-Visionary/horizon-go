package config

import (
	"fmt"
	"strings"
	"time"
)

// DefaultCollectInterval is used by cmd/worker when COLLECT_INTERVAL is unset.
const DefaultCollectInterval = 30 * time.Minute

// CollectInterval parses COLLECT_INTERVAL: a non-negative duration, or
// "0"/"off"/"none"/"false" to disable worker periodic collection; anything else
// falls back to def.
func CollectInterval(raw string, def time.Duration) time.Duration {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return def
	case "0", "off", "none", "false":
		return 0
	}
	if d, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil && d >= 0 {
		return d
	}
	return def
}

// IsCollectIntervalOff reports whether COLLECT_INTERVAL disables the worker's
// own periodic collection loop.
func IsCollectIntervalOff(raw string) bool {
	return CollectInterval(raw, DefaultCollectInterval) <= 0
}

// ResolveInternalScheduler decides whether cmd/api should run the internal
// cron + catch-up scheduler. When COLLECT_INTERVAL is off (as on Render),
// the internal scheduler is enabled by default; when COLLECT_INTERVAL is active
// (e.g. local dev default of 30m in cmd/worker), it stays disabled so the two
// clocks never run together. INTERNAL_SCHEDULER can explicitly disable ("off",
// "false", "0", "no", "none") or request enabling ("on", "true", "1", "yes").
func ResolveInternalScheduler(lookup func(string) string) (enabled bool, reason string, err error) {
	intervalRaw := lookup("COLLECT_INTERVAL")
	intervalOff := IsCollectIntervalOff(intervalRaw)
	override := strings.ToLower(strings.TrimSpace(lookup("INTERNAL_SCHEDULER")))

	switch override {
	case "":
		if intervalOff {
			return true, "COLLECT_INTERVAL is off", nil
		}
		return false, "COLLECT_INTERVAL is active", nil
	case "0", "false", "off", "no", "none":
		return false, "INTERNAL_SCHEDULER is off", nil
	case "1", "true", "on", "yes":
		if !intervalOff {
			return false, "", fmt.Errorf("INTERNAL_SCHEDULER=%q cannot be enabled while COLLECT_INTERVAL is active (%q); set COLLECT_INTERVAL=off", override, intervalRaw)
		}
		return true, "INTERNAL_SCHEDULER is on", nil
	default:
		return false, "", fmt.Errorf("invalid INTERNAL_SCHEDULER %q (expected true/false or on/off)", override)
	}
}
