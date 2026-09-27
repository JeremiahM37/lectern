package ciloop

import (
	"regexp"
	"strings"
)

// Bounds on what one fix request carries. A failing CI log can be megabytes;
// the agent needs the end of it, where the error is, not the whole thing.
const (
	jobTailLines  = 80
	jobTailBytes  = 4000
	reportBytes   = 14000
	maxJobsLogged = 4
	// maxChecksListed bounds the failing-check list; a matrix build can
	// fail dozens of jobs at once.
	maxChecksListed = 20
)

var (
	ansiRe      = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	timestampRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z ?`)
)

// cleanLog turns `gh run view --log-failed` output ("job\tstep\ttimestamp
// text" per line) into plain "step | text" lines: colour codes, timestamps
// and the repeated job name only cost the agent tokens.
func cleanLog(raw string) string {
	raw = ansiRe.ReplaceAllString(raw, "")
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		step := ""
		if parts := strings.SplitN(line, "\t", 3); len(parts) == 3 {
			step, line = parts[1], parts[2]
		}
		// GitHub starts each step's log with a UTF-8 byte order mark, so the
		// first line of every step has it in front of its timestamp.
		line = strings.TrimPrefix(line, "\ufeff")
		line = timestampRe.ReplaceAllString(line, "")
		if strings.HasPrefix(line, "##[group]") || strings.HasPrefix(line, "##[endgroup]") {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if step != "" {
			line = step + " | " + line
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// tail keeps the last n lines, then the last max bytes of those, cut on a
// line boundary where one exists.
func tail(s string, n, max int) string {
	lines := strings.Split(s, "\n")
	cut := false
	if len(lines) > n {
		lines, cut = lines[len(lines)-n:], true
	}
	s = strings.Join(lines, "\n")
	if len(s) > max {
		s, cut = s[len(s)-max:], true
		if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)-1 {
			s = s[i+1:]
		}
	}
	if cut {
		s = "… (earlier output trimmed)\n" + s
	}
	return s
}

// redactions are deliberately broad: a CI log is echoed into an agent's
// context and stored as a task message, so a false positive only hides a
// few characters while a miss leaks a credential. GitHub already masks the
// secrets it knows about as ***; these catch the ones it does not.
var redactions = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`), "[REDACTED PRIVATE KEY]"},
	{regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`), "[REDACTED]"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), "[REDACTED]"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), "[REDACTED]"},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), "[REDACTED]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "[REDACTED]"},
	{regexp.MustCompile(`(?i)\b(authorization:\s*(?:bearer|basic|token)\s+)\S+`), "${1}[REDACTED]"},
	{regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]{16,}`), "${1}[REDACTED]"},
	{regexp.MustCompile(`(://[^/\s:@]+:)[^@\s/]+@`), "${1}[REDACTED]@"},
	{regexp.MustCompile(`(?i)\b([A-Z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)[A-Z0-9_.-]*["']?\s*[:=]\s*)(["']?)[^\s"',;]{4,}`), "${1}${2}[REDACTED]"},
}

// Redact masks obvious credentials in text headed for an agent.
func Redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return s
}

// trimJobLog is the whole per-job pipeline: clean, redact, tail. Redacting
// before the cut matters: tailing first could slice a private key block so
// its BEGIN line no longer matches. The second pass is for anything the cut
// itself exposed at the new first line.
func trimJobLog(raw string) string {
	return Redact(tail(Redact(cleanLog(raw)), jobTailLines, jobTailBytes))
}

// Bounds on a log shown to a person rather than sent to an agent: a person
// can scroll, so it keeps far more, but a multi-megabyte log still is not
// worth shipping to a phone.
const (
	viewTailLines = 2000
	viewTailBytes = 200000
)

// ViewLog is trimJobLog for a person: the same cleaning and redaction with a
// much larger tail. Redacted because the page travels to phones and relay
// clients like everything else.
func ViewLog(raw string) string {
	return Redact(tail(Redact(cleanLog(raw)), viewTailLines, viewTailBytes))
}

// TrimForAgent is the loop's own per-job trimming, for a failure report
// built outside the loop (a GitLab job's log in "Fix checks with agent").
func TrimForAgent(raw string) string { return trimJobLog(raw) }
