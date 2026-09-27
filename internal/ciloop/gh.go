package ciloop

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// Every GitHub call is the target's own `gh` CLI, run through the same
// executor the commit/PR button uses (internal/api/session_review.go), so
// the loop needs no token of its own and no inbound webhook.

// prInfo is what `gh pr view --json state,headRefOid` reports.
type prInfo struct {
	State   string `json:"state"` // OPEN | CLOSED | MERGED
	HeadSHA string `json:"headRefOid"`
}

// check is one row of `gh pr checks`.
type check struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"` // pass | fail | pending | skipping | cancel
	Link   string `json:"link,omitempty"`
}

func (c check) failed() bool { return c.Bucket == "fail" || c.Bucket == "cancel" }

// ghError is a gh call that ran and failed. fatal means retrying cannot
// help (not signed in, not installed, no such PR); anything else is treated
// as transient and retried with backoff.
type ghError struct {
	fatal bool
	msg   string
}

func (e *ghError) Error() string { return e.msg }

var prURLRe = regexp.MustCompile(`^https?://([^/\s]+)/([^/\s]+)/([^/\s]+)/pull/(\d+)/?$`)

// repoArg turns a PR URL into gh's -R argument ([HOST/]OWNER/REPO), so run
// logs can be fetched without a checkout on the target.
func repoArg(prURL string) string {
	m := prURLRe.FindStringSubmatch(prURL)
	if m == nil {
		return ""
	}
	if m[1] == "github.com" {
		return m[2] + "/" + m[3]
	}
	return m[1] + "/" + m[2] + "/" + m[3]
}

// ValidPRURL reports whether s looks like a pull request URL.
func ValidPRURL(s string) bool { return prURLRe.MatchString(strings.TrimSpace(s)) }

var anyPRURLRe = regexp.MustCompile(`https?://[^/\s]+/[^/\s]+/[^/\s]+/pull/\d+`)

// FindPRURL pulls a PR URL out of gh output — `gh pr create` prints it on
// success, and names the existing one when a PR for the branch already exists.
func FindPRURL(output string) string {
	all := anyPRURLRe.FindAllString(output, -1)
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1]
}

// classify turns a failed gh call into a ghError.
func classify(what string, rc int, output string) *ghError {
	low := strings.ToLower(output)
	msg := strings.TrimSpace(output)
	if len(msg) > 300 {
		msg = msg[:300]
	}
	switch {
	case strings.Contains(low, "gh auth login") || strings.Contains(low, "not logged in") ||
		strings.Contains(low, "authentication required") || strings.Contains(low, "bad credentials") ||
		strings.Contains(low, "http 401"):
		return &ghError{fatal: true, msg: "gh is not signed in to GitHub on the target — run gh auth login there"}
	case rc == 127 || strings.Contains(low, "command not found"):
		return &ghError{fatal: true, msg: "gh is not installed on the target"}
	case strings.Contains(low, "could not resolve to a pullrequest") ||
		strings.Contains(low, "no pull requests found") || strings.Contains(low, "http 404"):
		return &ghError{fatal: true, msg: "pull request not found: " + msg}
	}
	return &ghError{msg: fmt.Sprintf("%s failed (rc %d): %s", what, rc, msg)}
}

func viewPR(ctx context.Context, ex executor.Executor, url string) (prInfo, error) {
	res, err := ex.Run(ctx, "gh pr view "+executor.ShellQuote(url)+" --json state,headRefOid",
		executor.RunOpts{Timeout: 30})
	if err != nil {
		return prInfo{}, err
	}
	var info prInfo
	if !res.OK() {
		return info, classify("gh pr view", res.RC, res.Stdout+res.Stderr)
	}
	if json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &info) != nil || info.State == "" {
		return info, &ghError{msg: "gh pr view returned unexpected output: " + clip(res.Stdout, 200)}
	}
	return info, nil
}

// listChecks reads `gh pr checks` in its plain (non-TTY) form — one
// tab-separated row per check: name, bucket, elapsed, link, description.
// That form predates `--json` (gh 2.48), so it works with the gh a target
// already has. gh exits non-zero when checks fail or are pending, so the
// exit code alone is not an error; rows that parse are the answer.
func listChecks(ctx context.Context, ex executor.Executor, url string) ([]check, error) {
	res, err := ex.Run(ctx, "gh pr checks "+executor.ShellQuote(url), executor.RunOpts{Timeout: 30})
	if err != nil {
		return nil, err
	}
	checks := parseChecks(res.Stdout)
	if len(checks) > 0 {
		return checks, nil
	}
	out := res.Stdout + res.Stderr
	if strings.Contains(strings.ToLower(out), "no checks reported") {
		return []check{}, nil
	}
	if res.OK() && strings.TrimSpace(out) == "" {
		return []check{}, nil
	}
	return nil, classify("gh pr checks", res.RC, out)
}

func parseChecks(stdout string) []check {
	var out []check
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) < 2 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		c := check{Name: strings.TrimSpace(fields[0]), Bucket: strings.ToLower(strings.TrimSpace(fields[1]))}
		if len(fields) > 3 {
			c.Link = strings.TrimSpace(fields[3])
		}
		switch c.Bucket {
		case "pass", "fail", "pending", "skipping", "cancel":
		default:
			c.Bucket = "pending" // unknown: wait rather than guess
		}
		out = append(out, c)
	}
	return out
}

var runLinkRe = regexp.MustCompile(`/actions/runs/(\d+)(?:/job/(\d+))?`)

// failedLog fetches `gh run view --log-failed` for a GitHub Actions check.
// A check from another CI system has no Actions run; ok is false and the
// report links to it instead.
func failedLog(ctx context.Context, ex executor.Executor, prURL, link string) (string, bool) {
	m := runLinkRe.FindStringSubmatch(link)
	repo := repoArg(prURL)
	if m == nil || repo == "" {
		return "", false
	}
	cmd := "gh run view " + m[1] + " -R " + executor.ShellQuote(repo) + " --log-failed"
	if m[2] != "" {
		cmd += " --job " + m[2]
	}
	res, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 60})
	if err != nil {
		return "(log unavailable: " + clip(err.Error(), 200) + ")", true
	}
	if !res.OK() || strings.TrimSpace(res.Stdout) == "" {
		return "(log unavailable: " + clip(strings.TrimSpace(res.Stdout+res.Stderr), 200) + ")", true
	}
	return res.Stdout, true
}

// JobLog fetches one GitHub Actions job's log for a person to read (the
// pull request page's check drill-down): the failed steps when there are
// any, else the whole job. It shares failedLog's command and repository
// resolution; ok is false for a check that is not an Actions job.
func JobLog(ctx context.Context, ex executor.Executor, prURL, link string) (string, bool) {
	raw, ok := failedLog(ctx, ex, prURL, link)
	if !ok || !strings.HasPrefix(raw, "(log unavailable") {
		return raw, ok
	}
	m := runLinkRe.FindStringSubmatch(link)
	cmd := "gh run view " + m[1] + " -R " + executor.ShellQuote(repoArg(prURL)) + " --log"
	if m[2] != "" {
		cmd += " --job " + m[2]
	}
	res, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 60})
	if err != nil || !res.OK() || strings.TrimSpace(res.Stdout) == "" {
		return raw, true
	}
	return res.Stdout, true
}

// IsActionsLink reports whether a check link is a GitHub Actions job, the
// only kind JobLog can read.
func IsActionsLink(link string) bool { return runLinkRe.MatchString(link) }

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
