package console

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// At 80 columns the approval banner used to be cut off before its keys; it
// now drops the command text first so "y allow" and "2" always show.
func TestApprovalBannerKeepsAnswerKeysAtNarrowWidths(t *testing.T) {
	m := &dashboard{approvals: []row{{
		"session_name": "approve-me", "tool_name": "Bash",
		"input": map[string]any{"command": "rm -rf build && npm test"},
	}}}
	for _, width := range []int{120, 80, 50} {
		b := m.approvalBanner(width)
		if w := ansi.StringWidth(b); w > width {
			t.Fatalf("width %d: banner is %d columns: %q", width, w, b)
		}
		if !strings.Contains(b, "y allow") {
			t.Fatalf("width %d: banner lost its answer keys: %q", width, b)
		}
	}
	if b := m.approvalBanner(120); !strings.Contains(b, "rm -rf build") {
		t.Fatalf("wide banner should keep the command: %q", b)
	}
	if b := m.approvalBanner(80); !strings.Contains(b, "2 Approvals") {
		t.Fatalf("80-column banner should still point at Approvals: %q", b)
	}
}

func TestSessionAgeReadsLastActivity(t *testing.T) {
	now := float64(time.Now().Unix())
	cases := map[string]struct {
		r    row
		want string
	}{
		"fresh":      {row{"last_activity_at": now - 5}, "now"},
		"minutes":    {row{"last_activity_at": now - 12*60}, "12m"},
		"falls back": {row{"created_at": now - 3*3600 - 5*60}, "3h 5m"},
		"none":       {row{}, ""},
	}
	for name, c := range cases {
		if got := sessionAge(c.r); got != c.want {
			t.Errorf("%s: sessionAge = %q, want %q", name, got, c.want)
		}
	}
}
