package textutil

import "testing"

func TestStripHTML(t *testing.T) {
	cases := map[string]string{
		"<p>Hello &amp; welcome</p>":     "Hello & welcome",
		"<p>a</p><p>b</p>":               "a b",
		"&lt;script&gt;x&lt;/script&gt;": "x",
		"  plain\n\ttext ":               "plain text",
		"":                               "",
	}
	for in, want := range cases {
		if got := StripHTML(in); got != want {
			t.Errorf("StripHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("ação longa", 6); got != "açã..." {
		t.Fatalf("got %q", got)
	}
	if got := Truncate("curto", 10); got != "curto" {
		t.Fatalf("got %q", got)
	}
}
