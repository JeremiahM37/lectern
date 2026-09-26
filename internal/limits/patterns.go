// Package limits makes Lectern usage-limit aware: it recognises when an agent
// CLI has been stopped by its provider's usage limit, records a hold with the
// reset time, and applies the session's or project's policy — notify, wait and
// resume at the reset, or hand the work to another agent. See
// docs/rate-limits.md.
package limits

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Pattern is one CLI limit message Lectern recognises. Every entry carries the
// real text it was taken from, so the table doubles as its own test fixture
// (patterns_test.go) and a changed CLI message fails a test rather than
// silently disabling detection.
type Pattern struct {
	Name string
	// Agent is the CLI whose message this is. Detection does not filter by
	// it: custom and catalog agents often wrap one of these CLIs or print a
	// provider's message verbatim, and every phrase here is specific enough to
	// mean the same thing wherever it appears.
	Agent string
	// Re must match at the start of a line, after terminal decoration
	// (bullets, box edges, prompts) is stripped — an agent quoting the phrase
	// mid-sentence, or source code containing it, is not a limit.
	Re *regexp.Regexp
	// SelfResume marks the CLI's own "I will continue when it resets" banner.
	// Lectern then waits for the CLI instead of typing into it.
	SelfResume bool
	// Source is where the message text was read from.
	Source string
	// Samples are verbatim messages; patterns_test.go matches every one.
	Samples []string
}

// Table is every recognised limit message, most specific first.
var Table = []Pattern{
	{
		Name: "claude-auto-continue", Agent: "claude", SelfResume: true,
		Re:     regexp.MustCompile(`^Usage limit reached\s*[·•∙]\s*continuing (automatically|shortly)`),
		Source: "Claude Code 2.1.283 bundle: the rate-limit wait banner (\"Usage limit reached · continuing automatically at …\")",
		Samples: []string{
			"Usage limit reached · continuing automatically at 3:40pm · esc to cancel",
			"Usage limit reached · continuing automatically when it resets · esc to cancel",
			"Usage limit reached · continuing shortly · esc to cancel",
		},
	},
	{
		Name: "claude-limit", Agent: "claude",
		// "You've hit your fast limit" only turns fast mode off; the session
		// keeps working, so it is deliberately not matched.
		Re:     regexp.MustCompile(`^You(?:'|’)ve hit your ((?:session|weekly|Opus|Sonnet|Fable|org's monthly (?:spend|usage)|monthly spend|usage credit|usage) )?limit(?:\s*[·•∙]|$)`),
		Source: "Claude Code 2.1.283 bundle: rate-limit error text (\"You've hit your ${limit} · resets ${time}\"; reset time rendered as 3pm, 3:40pm or Oct 3, 9am, with the IANA zone in parentheses)",
		Samples: []string{
			"You've hit your session limit · resets 3:40pm (America/Chicago)",
			"You've hit your weekly limit · resets Oct 3, 9am (America/Chicago)",
			"You've hit your limit · resets 11pm (UTC)",
			"You've hit your Opus limit · resets Oct 3, 9:30am (Europe/Berlin)",
			"You've hit your session limit · resets 3pm (UTC) · progress saved",
		},
	},
	{
		Name: "claude-out-of-credits", Agent: "claude",
		Re:      regexp.MustCompile(`^You(?:'|’)re out of usage credits(?:\s*[·•∙]|$)`),
		Source:  "Claude Code 2.1.283 bundle: overage-rejected error text",
		Samples: []string{"You're out of usage credits · resets Oct 3, 9am (UTC)"},
	},
	{
		Name: "claude-legacy", Agent: "claude",
		Re:      regexp.MustCompile(`^Claude AI usage limit reached\|(\d{9,11})`),
		Source:  "Claude Code 1.x print-mode result text (\"Claude AI usage limit reached|<unix reset>\")",
		Samples: []string{"Claude AI usage limit reached|1790200800"},
	},
	{
		Name: "codex-usage-limit", Agent: "codex",
		Re:     regexp.MustCompile(`^You(?:'|’)ve hit your usage limit(?: for [^.]+)?\.`),
		Source: "codex 0.157.0 binary / codex-rs core error.rs UsageLimitReachedError (\" Try again at %-I:%M %p\" today, \"%b %-d<suffix>, %Y %-I:%M %p\" otherwise)",
		Samples: []string{
			"You've hit your usage limit. Try again at 3:40 PM.",
			"You've hit your usage limit. Upgrade to Plus to continue using Codex (https://chatgpt.com/explore/plus), or try again at Oct 3rd, 2026 9:05 AM.",
			"You've hit your usage limit. Visit https://chatgpt.com/codex/settings/usage to purchase more credits or try again at 11:15 AM.",
			"You've hit your usage limit. Try again in 2 days 3 hours 4 minutes.",
			"You've hit your usage limit. Try again later.",
		},
	},
	{
		Name: "gemini-usage-limit", Agent: "gemini",
		Re:     regexp.MustCompile(`^Usage limit reached for [\w.\-]+\.`),
		Source: "gemini-cli 0.61.0 interactiveCli bundle: TerminalQuotaError message (\"Usage limit reached for ${model}.\" then \"Access resets at ${h:mm AM zone}.\")",
		Samples: []string{
			"Usage limit reached for gemini-2.5-pro.\nAccess resets at 3:40 PM PST.\n/stats model for usage details",
			"Usage limit reached for gemini-2.5-flash.",
		},
	},
	{
		Name: "gemini-quota-exhausted", Agent: "gemini",
		Re:     regexp.MustCompile(`^You have exhausted your (capacity|daily quota) on this model\.`),
		Source: "gemini-cli-core 0.61.0 googleQuotaErrors.js and its tests (the Code Assist API's own message)",
		Samples: []string{
			"You have exhausted your capacity on this model. Your quota will reset after 19h14m47s.",
			"You have exhausted your capacity on this model. Your quota will reset after 10m.",
			"You have exhausted your daily quota on this model.",
		},
	},
}

// Hit is one detected limit message.
type Hit struct {
	Pattern    string
	Agent      string
	Message    string
	ResetAt    time.Time // zero when the message did not say
	SelfResume bool
	// Line is the index of the matched line in the text it was found in.
	Line int
}

// decoration is what terminals put before a message: tree and box edges,
// status bullets, prompt glyphs and indentation.
const decoration = " \t⎿■●•✕✗✘│┃|>*⏺◆◇▶►-–—!⚠️"

func clean(line string) string {
	line = strings.TrimRight(line, " \t\r│┃|")
	return strings.TrimLeft(line, decoration)
}

// DetectLines scans text line by line and returns the LAST limit message in
// it. Only the matched line and the one after it (a narrow terminal wraps the
// reset time onto the next row) are read for the reset time.
func DetectLines(text string, now time.Time) (Hit, bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := clean(lines[i])
		if line == "" {
			continue
		}
		for _, p := range Table {
			loc := p.Re.FindStringSubmatchIndex(line)
			if loc == nil {
				continue
			}
			context := line
			if i+1 < len(lines) {
				context += " " + clean(lines[i+1])
			}
			hit := Hit{Pattern: p.Name, Agent: p.Agent, Message: clip(line, 300),
				SelfResume: p.SelfResume, Line: i}
			if p.Name == "claude-legacy" {
				if n, err := strconv.ParseInt(line[loc[2]:loc[3]], 10, 64); err == nil {
					hit.ResetAt = time.Unix(n, 0)
				}
			} else {
				hit.ResetAt = ParseReset(context, now)
			}
			return hit, true
		}
	}
	return Hit{}, false
}

// DetectTail looks only at the bottom of a terminal: the last n non-blank
// lines. A limit message the agent has since scrolled past is not a limit.
func DetectTail(pane string, n int, now time.Time) (Hit, bool) {
	lines := strings.Split(strings.TrimRight(pane, " \t\r\n"), "\n")
	kept, start := 0, len(lines)
	for start > 0 && kept < n {
		start--
		if strings.TrimSpace(lines[start]) != "" {
			kept++
		}
	}
	return DetectLines(strings.Join(lines[start:], "\n"), now)
}

// ---- reset times -------------------------------------------------------------

var (
	epochRe    = regexp.MustCompile(`\|(\d{9,11})\b`)
	afterDurRe = regexp.MustCompile(`(?i)reset after ((?:\d+h)?(?:\d+m)?(?:\d+(?:\.\d+)?s)?)\b`)
	inDurRe    = regexp.MustCompile(`(?i)try again in ((?:\d+ (?:days?|hours?|minutes?|seconds?)[ ,]*(?:and )?)+)`)
	// "resets 3:40pm (America/Chicago)", "continuing automatically at 3pm",
	// "try again at Oct 3rd, 2026 9:05 AM", "Access resets at 3:40 PM PST"
	clockRe = regexp.MustCompile(`(?i)(?:resets(?: at)?|automatically at|try again at)\s+` +
		`(?:([A-Z][a-z]{2}) (\d{1,2})(?:st|nd|rd|th)?,?(?: (\d{4}),?)?(?: at)? )?` +
		`(\d{1,2})(?::(\d{2}))?\s*([ap]m)` +
		`(?:\s*\(([A-Za-z_]+(?:/[A-Za-z_+\-]+)*)\)|\s+([A-Z]{2,5})\b)?`)
	unitRe = regexp.MustCompile(`(\d+) (day|hour|minute|second)`)
)

// zoneOffsets covers the abbreviations gemini-cli's Intl "short" zone names
// print in the locales Lectern is used from. Go resolves an abbreviation only
// against the process's own zone, so anything else is looked up here; an
// unknown one falls back to the local zone.
var zoneOffsets = map[string]int{
	"UTC": 0, "GMT": 0, "PST": -8, "PDT": -7, "MST": -7, "MDT": -6, "CST": -6, "CDT": -5,
	"EST": -5, "EDT": -4, "AKST": -9, "AKDT": -8, "HST": -10, "BST": 1, "CET": 1, "CEST": 2,
	"EET": 2, "EEST": 3, "JST": 9, "KST": 9, "AEST": 10, "AEDT": 11,
}

var months = map[string]time.Month{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

// ParseReset reads a reset time out of a limit message, relative to now (when
// the message was seen). It returns the zero time when the message names none
// or names one it cannot place.
func ParseReset(text string, now time.Time) time.Time {
	if m := epochRe.FindStringSubmatch(text); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return time.Unix(n, 0)
		}
	}
	if m := afterDurRe.FindStringSubmatch(text); m != nil && m[1] != "" {
		if d, err := time.ParseDuration(m[1]); err == nil && d > 0 {
			return now.Add(d)
		}
	}
	if m := inDurRe.FindStringSubmatch(text); m != nil {
		var d time.Duration
		for _, u := range unitRe.FindAllStringSubmatch(m[1], -1) {
			n, _ := strconv.Atoi(u[1])
			switch u[2] {
			case "day":
				d += time.Duration(n) * 24 * time.Hour
			case "hour":
				d += time.Duration(n) * time.Hour
			case "minute":
				d += time.Duration(n) * time.Minute
			case "second":
				d += time.Duration(n) * time.Second
			}
		}
		if d > 0 {
			return now.Add(d)
		}
	}
	m := clockRe.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}
	}
	loc := now.Location()
	if m[7] != "" {
		if l, err := time.LoadLocation(m[7]); err == nil {
			loc = l
		}
	} else if m[8] != "" {
		if off, ok := zoneOffsets[strings.ToUpper(m[8])]; ok {
			loc = time.FixedZone(m[8], off*3600)
		}
	}
	hour, _ := strconv.Atoi(m[4])
	minute := 0
	if m[5] != "" {
		minute, _ = strconv.Atoi(m[5])
	}
	if hour < 1 || hour > 12 || minute > 59 {
		return time.Time{}
	}
	hour %= 12
	if strings.EqualFold(m[6], "pm") {
		hour += 12
	}
	local := now.In(loc)
	if m[1] == "" {
		t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
		// A bare clock time is the next time the clock reads that — a CLI
		// never announces a reset that has already happened.
		if t.Before(local.Add(-10 * time.Minute)) {
			t = t.AddDate(0, 0, 1)
		}
		return t
	}
	month, ok := months[strings.ToLower(m[1])]
	day, _ := strconv.Atoi(m[2])
	if !ok || day < 1 || day > 31 {
		return time.Time{}
	}
	year := local.Year()
	if m[3] != "" {
		year, _ = strconv.Atoi(m[3])
	}
	t := time.Date(year, month, day, hour, minute, 0, 0, loc)
	if m[3] == "" && t.Before(local.AddDate(0, 0, -1)) {
		t = t.AddDate(1, 0, 0)
	}
	return t
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
