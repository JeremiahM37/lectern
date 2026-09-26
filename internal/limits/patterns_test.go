package limits

import (
	"strings"
	"testing"
	"time"
)

var chicago = mustLoc("America/Chicago")

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

// Every sample in the table must be recognised by its own entry. A CLI that
// rewords its message fails here instead of silently disabling detection.
func TestEveryTableSampleMatchesItsPattern(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for _, p := range Table {
		if len(p.Samples) == 0 || p.Source == "" {
			t.Errorf("%s: every pattern needs a source and real samples", p.Name)
		}
		for _, sample := range p.Samples {
			hit, ok := DetectLines(sample, now)
			if !ok || hit.Pattern != p.Name {
				t.Errorf("%s: sample %q detected as %q (ok=%v)", p.Name, sample, hit.Pattern, ok)
			}
		}
	}
}

func TestResetTimesFromRealMessages(t *testing.T) {
	// 12:00 in Chicago on 2026-09-26.
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, chicago)
	cases := []struct {
		text string
		want time.Time
	}{
		{"  ⎿  You've hit your session limit · resets 3:40pm (America/Chicago)",
			time.Date(2026, 9, 26, 15, 40, 0, 0, chicago)},
		{"You've hit your limit · resets 11pm (UTC)",
			time.Date(2026, 9, 26, 23, 0, 0, 0, time.UTC)},
		// A clock time earlier than now is tomorrow's.
		{"You've hit your session limit · resets 9am (America/Chicago)",
			time.Date(2026, 9, 27, 9, 0, 0, 0, chicago)},
		{"You've hit your weekly limit · resets Oct 3, 9am (America/Chicago)",
			time.Date(2026, 10, 3, 9, 0, 0, 0, chicago)},
		{"You've hit your weekly limit · resets Jan 2, 2027, 9:30am (UTC)",
			time.Date(2027, 1, 2, 9, 30, 0, 0, time.UTC)},
		{"Usage limit reached · continuing automatically at 3pm · esc to cancel",
			time.Date(2026, 9, 26, 15, 0, 0, 0, chicago)},
		{"■ You've hit your usage limit. Try again at 3:40 PM.",
			time.Date(2026, 9, 26, 15, 40, 0, 0, chicago)},
		{"You've hit your usage limit. Upgrade to Plus to continue using Codex (https://chatgpt.com/explore/plus), or try again at Oct 3rd, 2026 9:05 AM.",
			time.Date(2026, 10, 3, 9, 5, 0, 0, chicago)},
		{"You've hit your usage limit. Try again in 2 days 3 hours 4 minutes.",
			now.Add(51*time.Hour + 4*time.Minute)},
		{"│ ✕ Usage limit reached for gemini-2.5-pro.        │\n│ Access resets at 3:40 PM PST.                    │",
			time.Date(2026, 9, 26, 15, 40, 0, 0, time.FixedZone("PST", -8*3600))},
		{"You have exhausted your capacity on this model. Your quota will reset after 19h14m47s.",
			now.Add(19*time.Hour + 14*time.Minute + 47*time.Second)},
		{"Claude AI usage limit reached|1790200800", time.Unix(1790200800, 0)},
	}
	for _, c := range cases {
		hit, ok := DetectLines(c.text, now)
		if !ok {
			t.Errorf("not detected: %q", c.text)
			continue
		}
		if !hit.ResetAt.Equal(c.want) {
			t.Errorf("%q: reset %v, want %v", c.text, hit.ResetAt, c.want)
		}
	}
}

func TestMessagesWithoutAResetTime(t *testing.T) {
	now := time.Now()
	for _, text := range []string{
		"You've hit your usage limit. Try again later.",
		"You have exhausted your daily quota on this model.",
		"Usage limit reached · continuing automatically when it resets · esc to cancel",
	} {
		hit, ok := DetectLines(text, now)
		if !ok || !hit.ResetAt.IsZero() {
			t.Errorf("%q: ok=%v reset=%v", text, ok, hit.ResetAt)
		}
	}
	if hit, _ := DetectLines("Usage limit reached · continuing shortly · esc to cancel", now); !hit.SelfResume {
		t.Error("the CLI's own auto-continue banner must be marked as self-resuming")
	}
}

// Text that merely mentions a limit — an agent explaining one, source code,
// Claude's fast-mode notice, a transient 429 — is not a usage-limit stop.
func TestNearMissesAreNotLimits(t *testing.T) {
	for _, text := range []string{
		`msg := "You've hit your usage limit. Try again later."`,
		"The CLI prints You've hit your session limit · resets 3pm when that happens.",
		"You've hit your fast limit",
		"Fast limit reached and temporarily disabled · resets in 4m",
		"API Error: Rate limit reached",
		"Context limit reached · /compact or /clear to continue",
		"Concurrent subagent limit reached. You can run 4 subagents at once.",
		"Please retry in 35.5s",
	} {
		if hit, ok := DetectLines(text, time.Now()); ok {
			t.Errorf("false positive %q -> %s", text, hit.Pattern)
		}
	}
}

func TestDetectTailIgnoresScrolledPastMessages(t *testing.T) {
	now := time.Now()
	old := "  ⎿  You've hit your session limit · resets 3pm (UTC)\n" + strings.Repeat("working on it\n", 30) + "❯ "
	if _, ok := DetectTail(old, 12, now); ok {
		t.Fatal("a limit the agent has since scrolled past was reported")
	}
	fresh := strings.Repeat("output\n", 30) + "  ⎿  You've hit your session limit · resets 3pm (UTC)\n\n╭────╮\n│ >  │\n╰────╯\n  ? for shortcuts\n\n\n"
	hit, ok := DetectTail(fresh, 12, now)
	if !ok || hit.Pattern != "claude-limit" {
		t.Fatalf("limit at the bottom of the pane not detected: %+v", hit)
	}
}

func TestDetectEventReadsTaskStreams(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	hit, ok := DetectEvent("rate_limit", map[string]any{"status": "rejected", "resets_at": float64(1790200800), "rate_limit_type": "seven_day"}, now)
	if !ok || hit.ResetAt.Unix() != 1790200800 || !strings.Contains(hit.Message, "seven_day") {
		t.Fatalf("rejected event: %+v %v", hit, ok)
	}
	if _, ok := DetectEvent("rate_limit", map[string]any{"status": "allowed_warning"}, now); ok {
		t.Fatal("a warning is not a stop")
	}
	for typ, payload := range map[string]map[string]any{
		"error":  {"message": "You've hit your usage limit. Try again at 3:40 PM."},
		"result": {"result": "You've hit your session limit · resets 3pm (UTC)"},
		"text":   {"text": "Usage limit reached for gemini-2.5-pro.\nAccess resets at 3:40 PM PST."},
	} {
		if _, ok := DetectEvent(typ, payload, now); !ok {
			t.Errorf("%s event not detected: %v", typ, payload)
		}
	}
	if _, ok := DetectEvent("tool_result", map[string]any{"content": "You've hit your usage limit."}, now); ok {
		t.Fatal("tool output (a file the agent read) is not the agent's own limit")
	}
}
