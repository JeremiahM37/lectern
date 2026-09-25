package triggers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// ghResponse is one `gh api --include` call, header block and body split
// apart so callers can read a rate-limit header without re-parsing raw text.
type ghResponse struct {
	StatusCode    int
	RateRemaining int // -1 when the header was absent
	Body          []byte
}

// ghAPI runs `gh api --include <path>` on ex — the target's own `gh` CLI
// login, exactly like the existing `gh pr create` commit path
// (internal/api/session_review.go). --include prints the response status
// line and headers before a blank line and the body, which is how the
// rate-limit header is read without a second request.
func ghAPI(ctx context.Context, ex executor.Executor, path string) (ghResponse, error) {
	cmd := "gh api --include " + executor.ShellQuote(path)
	res, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 30})
	if err != nil {
		return ghResponse{}, err
	}
	out := parseGHInclude(res.Stdout)
	if !res.OK() {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = truncate(strings.TrimSpace(string(out.Body)), 300)
		}
		return out, fmt.Errorf("gh api %s: %s", path, msg)
	}
	return out, nil
}

// parseGHInclude splits `gh api --include`'s stdout into headers and body.
// Header lines are case-folded before matching, since HTTP header casing is
// not guaranteed stable across gh/transport versions.
func parseGHInclude(stdout string) ghResponse {
	out := ghResponse{RateRemaining: -1}
	head, body, found := strings.Cut(stdout, "\r\n\r\n")
	if !found {
		head, body, found = strings.Cut(stdout, "\n\n")
	}
	if !found {
		out.Body = []byte(stdout)
		return out
	}
	out.Body = []byte(body)
	for _, line := range strings.Split(head, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "HTTP/") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				out.StatusCode, _ = strconv.Atoi(fields[1])
			}
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), "x-ratelimit-remaining") {
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				out.RateRemaining = n
			}
		}
	}
	return out
}

// commitPushPR commits whatever a trigger-created task's attempt left
// uncommitted in its worktree, pushes the branch, and opens a PR referencing
// the source issue with "Closes #N" — the auto-postback half of requirement 1.
// It deliberately re-implements (rather than imports) the three git steps
// internal/api/session_review.go's gitCommitPushPR already runs for a human's
// Commit/PR button: that function lives in internal/api, which imports this
// package for wiring, so importing back would cycle. Both call the same
// underlying `git`/`gh` commands; a change to one's shape should be checked
// against the other.
func commitPushPR(ctx context.Context, ex executor.Executor, dir, branch, title, body string) (prURL string, err error) {
	q := executor.ShellQuote
	commit, err := ex.Run(ctx, "git add -A && git diff --cached --quiet || git commit -m "+q(title),
		executor.RunOpts{Cwd: dir, Timeout: 60})
	if err != nil {
		return "", err
	}
	if !commit.OK() {
		return "", fmt.Errorf("commit failed: %s", truncate(commit.Stdout+commit.Stderr, 400))
	}
	push, err := ex.Run(ctx, "git push -u origin "+q(branch), executor.RunOpts{Cwd: dir, Timeout: 120})
	if err != nil {
		return "", err
	}
	if !push.OK() {
		return "", fmt.Errorf("push failed: %s", truncate(push.Stdout+push.Stderr, 400))
	}
	pr, err := ex.Run(ctx, fmt.Sprintf("gh pr create --head %s --title %s --body %s", q(branch), q(title), q(body)),
		executor.RunOpts{Cwd: dir, Timeout: 120})
	if err != nil {
		return "", err
	}
	if !pr.OK() {
		return "", fmt.Errorf("gh pr create failed: %s", truncate(pr.Stdout+pr.Stderr, 400))
	}
	lines := strings.Split(strings.TrimSpace(pr.Stdout), "\n")
	return strings.TrimSpace(lines[len(lines)-1]), nil
}

// postGitHubComment replies on the issue/PR the triggered task came from —
// the follow-up-comment half of requirement 1, and what a rejected/rate-
// limited mention gets instead of silence when ReplyOnSkip is wired in later.
func postGitHubComment(ctx context.Context, ex executor.Executor, repo string, issueNumber int, body string) error {
	q := executor.ShellQuote
	res, err := ex.Run(ctx, fmt.Sprintf("gh issue comment %d --repo %s --body %s", issueNumber, q(repo), q(body)),
		executor.RunOpts{Timeout: 30})
	if err != nil {
		return err
	}
	if !res.OK() {
		return fmt.Errorf("gh issue comment: %s", truncate(res.Stdout+res.Stderr, 400))
	}
	return nil
}
