package app

import "testing"

// A subagent's token thresholds use the model window capped at 200 000, so a
// 1M-token model does not let a subagent fill 800 000 tokens before its
// nudge. A smaller window is used as it is, and an unknown one gets the cap.
func TestSubagentWindowCapsAt200k(t *testing.T) {
	cases := []struct {
		name   string
		window int
		want   int
	}{
		{"unknown window gets the cap", 0, 200_000},
		{"negative window gets the cap", -1, 200_000},
		{"small window used as is", 128_000, 128_000},
		{"exactly the cap", 200_000, 200_000},
		{"million-token model is capped", 1_000_000, 200_000},
		{"just over the cap is capped", 200_001, 200_000},
	}
	for _, c := range cases {
		if got := subagentWindow(c.window); got != c.want {
			t.Errorf("%s: subagentWindow(%d) = %d, want %d", c.name, c.window, got, c.want)
		}
	}
}
