package triggers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/worktree"
)

// processPostbacks reports the outcome of every trigger-created task that has
// left queued/running since the last pass back to whichever source asked for
// it: a PR referencing the issue, a comment reply, or a Slack thread update.
func (m *Manager) processPostbacks(ctx context.Context) {
	events, err := m.DB.PendingPostbackEvents()
	if err != nil {
		m.Log.Error("triggers: listing pending postbacks failed", "err", err)
		return
	}
	for _, ev := range events {
		m.postback(ctx, ev)
	}
}

func (m *Manager) postback(ctx context.Context, ev *store.TriggerEvent) {
	src, err := m.DB.TriggerSource(ev.SourceID)
	if err != nil {
		m.Log.Warn("triggers: postback source is gone", "event", ev.ID, "source", ev.SourceID)
		m.DB.MarkPostback(ev.ID, "error", "source was deleted")
		return
	}
	task, err := m.DB.Task(*ev.TaskID)
	if err != nil {
		m.DB.MarkPostback(ev.ID, "error", "task was deleted")
		return
	}
	att, _ := m.DB.LatestAttempt(task.ID)
	var perr error
	switch Kind(src.Kind) {
	case KindGitHub:
		perr = m.postbackGitHub(ctx, src, ev, task, att)
	case KindLinear:
		perr = m.postbackLinear(ctx, src, ev, task, att)
	case KindSlack:
		perr = m.postbackSlack(ctx, src, ev, task, att)
	}
	if perr != nil {
		m.Log.Warn("triggers: postback failed", "event", ev.ID, "kind", src.Kind, "err", perr)
		m.DB.MarkPostback(ev.ID, "error", perr.Error())
		return
	}
	m.DB.MarkPostback(ev.ID, "sent", "")
}

// summarize is the one line of result text every postback carries: the
// task's outcome plus a diff-stat one-liner when there is a diff to describe.
func summarize(task *store.Task, att *store.Attempt) string {
	line := fmt.Sprintf("Task #%d finished as %q.", task.ID, task.Status)
	if att == nil {
		return line
	}
	var stats []worktree.FileStat
	_ = json.Unmarshal([]byte(att.DiffStatJSON), &stats)
	if len(stats) == 0 {
		return line
	}
	add, del := 0, 0
	for _, s := range stats {
		add += s.Additions
		del += s.Deletions
	}
	return fmt.Sprintf("%s %d file%s changed, +%d -%d.", line, len(stats), plural(len(stats)), add, del)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func rawField(rawJSON, key string) any {
	var raw map[string]any
	_ = json.Unmarshal([]byte(rawJSON), &raw)
	return raw[key]
}

func rawString(rawJSON, key string) string {
	s, _ := rawField(rawJSON, key).(string)
	return s
}

func rawInt(rawJSON, key string) int {
	n, _ := rawField(rawJSON, key).(float64)
	return int(n)
}

func (m *Manager) postbackGitHub(ctx context.Context, src *store.TriggerSource, ev *store.TriggerEvent, task *store.Task, att *store.Attempt) error {
	project, err := m.DB.Project(ev.ProjectID)
	if err != nil {
		return err
	}
	ex, err := m.projectExecutor(project)
	if err != nil {
		return err
	}
	repo := rawString(ev.RawJSON, "repo")
	issueNum := rawInt(ev.RawJSON, "issue_number")
	summary := summarize(task, att)

	if task.Status == "failed" || task.Status == "cancelled" {
		return postGitHubComment(ctx, ex, repo, issueNum,
			fmt.Sprintf("Lectern task #%d did not complete (%s).\n\n%s", task.ID, task.Status, summary))
	}
	if ev.Kind != "issue" {
		// A mention-triggered follow-up posts its result rather than a
		// second PR — see requirement 1's "reply to the comment" branch.
		return postGitHubComment(ctx, ex, repo, issueNum,
			fmt.Sprintf("Lectern followed up on this (task #%d, %s).\n\n%s", task.ID, task.Status, summary))
	}
	if att == nil || att.Branch == "" || att.WorktreePath == "" {
		return postGitHubComment(ctx, ex, repo, issueNum,
			fmt.Sprintf("Lectern task #%d finished with no changes to open a pull request from.\n\n%s", task.ID, summary))
	}
	title := task.Title
	body := fmt.Sprintf("Closes #%d.\n\n%s\n\nOpened automatically by a Lectern GitHub trigger.", issueNum, summary)
	url, prErr := commitPushPR(ctx, ex, att.WorktreePath, att.Branch, title, body)
	if prErr != nil {
		return postGitHubComment(ctx, ex, repo, issueNum,
			fmt.Sprintf("Lectern finished the work (task #%d) but could not open a pull request: %s\n\n%s", task.ID, prErr.Error(), summary))
	}
	return postGitHubComment(ctx, ex, repo, issueNum, fmt.Sprintf("Opened %s\n\n%s", url, summary))
}

func (m *Manager) postbackLinear(ctx context.Context, src *store.TriggerSource, ev *store.TriggerEvent, task *store.Task, att *store.Attempt) error {
	cfg, err := ParseLinearConfig(src.ConfigJSON)
	if err != nil {
		return err
	}
	secrets, err := ParseLinearSecrets(src.SecretsJSON)
	if err != nil {
		return err
	}
	issueID := rawString(ev.RawJSON, "issue_id")
	summary := summarize(task, att)
	if err := postLinearComment(ctx, m, secrets.APIKey, issueID, summary); err != nil {
		return err
	}
	if task.Status == "failed" || task.Status == "cancelled" {
		return nil // leave the issue where a human put it; the comment says why
	}
	return moveLinearIssueState(ctx, m, secrets.APIKey, cfg.TeamKey, cfg.DoneStateName, issueID)
}
