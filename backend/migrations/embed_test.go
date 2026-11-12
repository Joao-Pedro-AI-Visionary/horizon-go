package migrations

import (
	"strconv"
	"strings"
	"testing"
)

// TestVersionsAreUnique: goose panics at startup on two files with the same
// version (it happened when two branches both added a 00007).
func TestVersionsAreUnique(t *testing.T) {
	entries, err := FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(prefix)
		if !ok || err != nil {
			t.Fatalf("%s: want NNNNN_name.sql", name)
		}
		if prev, dup := seen[v]; dup {
			t.Fatalf("duplicate version %d: %s and %s", v, prev, name)
		}
		seen[v] = name
	}
	if len(seen) == 0 {
		t.Fatal("no migrations embedded")
	}
}
