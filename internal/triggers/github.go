package triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// githubCursor is trigger_sources.cursor_json for kind "github".
//
// since-timestamps, not ETags: gh api's conditional-request (304) behaviour
// through --include was not something this could verify against live GitHub
// without a real token in this environment, and a since cursor is a fully
// documented, reliable substitute at homelab polling volumes — see
// docs/triggers.md. Rate-limit awareness instead comes from reading
// x-ratelimit-remaining off every response and backing off before it hits 0.
type githubCursor struct {
	IssuesSince   string `json:"issues_since"`
	CommentsSince string `json:"comments_since"`
}

func parseGitHubCursor(raw string) githubCursor {
	var c githubCursor
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	return c
}

type ghIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	User        ghUser    `json:"user"`
	Labels      []ghLabel `json:"labels"`
	PullRequest *struct{} `json:"pull_request"`
	UpdatedAt   string    `json:"updated_at"`
}
type ghUser struct {
	Login string `json:"login"`
}
type ghLabel struct {
	Name string `json:"name"`
}
type ghComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	HTMLURL   string `json:"html_url"`
	IssueURL  string `json:"issue_url"`
	User      ghUser `json:"user"`
	UpdatedAt string `json:"updated_at"`
}

func hasLabel(labels []ghLabel, name string) bool {
	for _, l := range labels {
		if strings.EqualFold(l.Name, name) {
			return true
		}
	}
	return false
}

// issueNumberFromURL pulls the trailing number off an issue_url like
// ".../repos/owner/repo/issues/42" — how a comment says which issue/PR it
// belongs to.
func issueNumberFromURL(url string) int {
	parts := strings.Split(strings.TrimRight(url, "/"), "/")
	if len(parts) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(parts[len(parts)-1])
	return n
}

func (m *Manager) projectExecutor(project *store.Project) (executor.Executor, error) {
	if m.Reg == nil {
		return nil, fmt.Errorf("no executor registry configured")
	}
	target, err := m.DB.Target(project.TargetID)
	if err != nil {
		return nil, err
	}
	return m.Reg.For(target)
}

// pollGitHub lists open issues carrying the configured label and recent
// comments mentioning the configured handle, on the project's own target
// (its `gh` CLI login is the only credential this ever touches — see the
// package doc).
func (m *Manager) pollGitHub(ctx context.Context, src *store.TriggerSource) error {
	project, err := m.DB.Project(src.ProjectID)
	if err != nil {
		return err
	}
	cfg, err := ParseGitHubConfig(src.ConfigJSON)
	if err != nil {
		return err
	}
	ex, err := m.projectExecutor(project)
	if err != nil {
		return err
	}
	cursor := parseGitHubCursor(src.CursorJSON)
	pollStart := m.nowFn().UTC().Format(time.RFC3339)

	issues, remaining, err := m.listLabelledIssues(ctx, ex, cfg, cursor.IssuesSince)
	if err != nil {
		return fmt.Errorf("listing issues: %w", err)
	}
	for _, issue := range issues {
		if issue.PullRequest != nil || !hasLabel(issue.Labels, cfg.Label) {
			continue
		}
		m.intake(project, src, cfg.AllowedAuthors, cfg.Agent, cfg.Model, cfg.BaseBranch, cfg.MaxPerHour, candidate{
			ExternalID: fmt.Sprintf("issue:%d", issue.Number),
			Kind:       "issue",
			Author:     issue.User.Login,
			Summary:    issue.Title,
			Title:      issue.Title,
			Prompt: fmt.Sprintf("GitHub issue #%d on %s: %s\n\n%s\n\n%s",
				issue.Number, cfg.Repo, issue.Title, issue.Body, issue.HTMLURL),
			Labels: []string{"github-issue"},
			Raw:    map[string]any{"repo": cfg.Repo, "issue_number": issue.Number, "html_url": issue.HTMLURL},
		})
		// Re-labelled regardless of whether intake created a task (also true
		// for a rejected/rate-limited author) so a stranger's comment cannot
		// wedge this issue into being re-evaluated forever — the operator
		// sees why in the event log and can re-add the label by hand.
		m.relabelIssue(ctx, ex, cfg, issue.Number)
	}

	if remaining >= 0 && remaining < 5 {
		m.Log.Warn("triggers: github rate limit low, skipping comment poll this tick",
			"source", src.ID, "remaining", remaining)
		cursor.IssuesSince = pollStart
		m.DB.RecordPoll(src.ID, store.J(cursor), "ok", "")
		return nil
	}

	comments, _, err := m.listRecentComments(ctx, ex, cfg, cursor.CommentsSince)
	if err != nil {
		return fmt.Errorf("listing comments: %w", err)
	}
	for _, c := range comments {
		if !strings.Contains(strings.ToLower(c.Body), strings.ToLower(cfg.MentionHandle)) {
			continue
		}
		issueNum := issueNumberFromURL(c.IssueURL)
		m.intake(project, src, cfg.AllowedAuthors, cfg.Agent, cfg.Model, cfg.BaseBranch, cfg.MaxPerHour, candidate{
			ExternalID: fmt.Sprintf("comment:%d", c.ID),
			Kind:       "comment",
			Author:     c.User.Login,
			Summary:    fmt.Sprintf("mention on #%d", issueNum),
			Title:      fmt.Sprintf("Follow-up on %s#%d", cfg.Repo, issueNum),
			Prompt: fmt.Sprintf("A comment on %s#%d mentioned %s:\n\n%s\n\n%s",
				cfg.Repo, issueNum, cfg.MentionHandle, c.Body, c.HTMLURL),
			Labels: []string{"github-comment"},
			Raw:    map[string]any{"repo": cfg.Repo, "issue_number": issueNum, "comment_id": c.ID, "html_url": c.HTMLURL},
		})
	}

	cursor.IssuesSince = pollStart
	cursor.CommentsSince = pollStart
	return m.DB.RecordPoll(src.ID, store.J(cursor), "ok", "")
}

func (m *Manager) listLabelledIssues(ctx context.Context, ex executor.Executor, cfg GitHubConfig, since string) ([]ghIssue, int, error) {
	path := fmt.Sprintf("repos/%s/issues?labels=%s&state=open&sort=updated&direction=asc&per_page=50",
		cfg.Repo, ghQueryEscape(cfg.Label))
	if since != "" {
		path += "&since=" + ghQueryEscape(since)
	}
	res, err := ghAPI(ctx, ex, path)
	if err != nil {
		return nil, -1, err
	}
	var issues []ghIssue
	if err := json.Unmarshal(res.Body, &issues); err != nil {
		return nil, res.RateRemaining, fmt.Errorf("parsing issues: %w (%s)", err, truncate(string(res.Body), 300))
	}
	return issues, res.RateRemaining, nil
}

func (m *Manager) listRecentComments(ctx context.Context, ex executor.Executor, cfg GitHubConfig, since string) ([]ghComment, int, error) {
	path := fmt.Sprintf("repos/%s/issues/comments?sort=updated&direction=asc&per_page=50", cfg.Repo)
	if since != "" {
		path += "&since=" + ghQueryEscape(since)
	}
	res, err := ghAPI(ctx, ex, path)
	if err != nil {
		return nil, -1, err
	}
	var comments []ghComment
	if err := json.Unmarshal(res.Body, &comments); err != nil {
		return nil, res.RateRemaining, fmt.Errorf("parsing comments: %w (%s)", err, truncate(string(res.Body), 300))
	}
	return comments, res.RateRemaining, nil
}

// relabelIssue swaps Label for QueuedLabel so the same issue is never
// re-evaluated on the next poll even if the event ledger were ever lost —
// the label itself is the durable "this was seen" marker on GitHub's side.
func (m *Manager) relabelIssue(ctx context.Context, ex executor.Executor, cfg GitHubConfig, number int) {
	q := executor.ShellQuote
	cmd := fmt.Sprintf("gh issue edit %d --repo %s --remove-label %s --add-label %s",
		number, q(cfg.Repo), q(cfg.Label), q(cfg.QueuedLabel))
	if res, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 30}); err != nil || !res.OK() {
		m.Log.Warn("triggers: relabelling github issue failed", "repo", cfg.Repo, "issue", number)
	}
}

func ghQueryEscape(s string) string {
	r := strings.NewReplacer(" ", "%20", "&", "%26", "#", "%23", "+", "%2B")
	return r.Replace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
