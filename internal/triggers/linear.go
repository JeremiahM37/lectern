package triggers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

const linearAPIURL = "https://api.linear.app/graphql"

// linearCursor is trigger_sources.cursor_json for kind "linear".
type linearCursor struct {
	Since string `json:"since"` // RFC3339; only issues updated after this are polled
}

func parseLinearCursor(raw string) linearCursor {
	var c linearCursor
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	return c
}

type linearUser struct {
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

type linearIssue struct {
	ID          string      `json:"id"`
	Identifier  string      `json:"identifier"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	URL         string      `json:"url"`
	UpdatedAt   string      `json:"updatedAt"`
	Creator     *linearUser `json:"creator"`
}

// gqlRequest is the client every Linear call goes through. It is a package
// var (not a Manager method) so tests can point it at an httptest fake
// without a real API key or network access.
var linearHTTPClient = func(m *Manager) *http.Client {
	if m.HTTP != nil {
		return m.HTTP
	}
	return http.DefaultClient
}

func linearRequest[T any](ctx context.Context, m *Manager, apiKey, query string, variables map[string]any) (T, error) {
	var zero T
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return zero, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearAPIURL, bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiKey)
	resp, err := linearHTTPClient(m).Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return zero, err
	}
	if resp.StatusCode >= 300 {
		return zero, fmt.Errorf("linear api %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var envelope struct {
		Data   T `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return zero, fmt.Errorf("parsing linear response: %w (%s)", err, truncate(string(raw), 300))
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, len(envelope.Errors))
		for i, e := range envelope.Errors {
			msgs[i] = e.Message
		}
		return zero, fmt.Errorf("linear: %s", strings.Join(msgs, "; "))
	}
	return envelope.Data, nil
}

const linearIssuesQuery = `query($teamKey: String!, $label: String!, $since: DateTimeOrDuration) {
  issues(filter: {team: {key: {eq: $teamKey}}, labels: {name: {eq: $label}}, updatedAt: {gt: $since}}, first: 50, orderBy: updatedAt) {
    nodes { id identifier title description url updatedAt creator { email displayName } }
  }
}`

type issuesPayload struct {
	Issues struct {
		Nodes []linearIssue `json:"nodes"`
	} `json:"issues"`
}

// pollLinear lists issues in the configured team carrying the configured
// label, updated since the last poll, and files a task for each one an
// allowlisted author created.
func (m *Manager) pollLinear(ctx context.Context, src *store.TriggerSource) error {
	project, err := m.DB.Project(src.ProjectID)
	if err != nil {
		return err
	}
	cfg, err := ParseLinearConfig(src.ConfigJSON)
	if err != nil {
		return err
	}
	secrets, err := ParseLinearSecrets(src.SecretsJSON)
	if err != nil {
		return err
	}
	if err := secrets.validate(); err != nil {
		return err
	}
	cursor := parseLinearCursor(src.CursorJSON)
	pollStart := m.nowFn().UTC().Format(time.RFC3339)

	vars := map[string]any{"teamKey": cfg.TeamKey, "label": cfg.Label}
	if cursor.Since != "" {
		vars["since"] = cursor.Since
	} else {
		vars["since"] = nil
	}
	data, err := linearRequest[issuesPayload](ctx, m, secrets.APIKey, linearIssuesQuery, vars)
	if err != nil {
		return err
	}
	for _, issue := range data.Issues.Nodes {
		author := ""
		if issue.Creator != nil {
			author = firstNonEmpty(issue.Creator.Email, issue.Creator.DisplayName)
		}
		m.intake(project, src, cfg.AllowedUsers, cfg.Agent, cfg.Model, "", cfg.MaxPerHour, candidate{
			ExternalID: "linear:" + issue.ID,
			Kind:       "linear_issue",
			Author:     author,
			Summary:    issue.Title,
			Title:      fmt.Sprintf("[%s] %s", issue.Identifier, issue.Title),
			Prompt: fmt.Sprintf("Linear issue %s: %s\n\n%s\n\n%s",
				issue.Identifier, issue.Title, issue.Description, issue.URL),
			Labels: []string{"linear-issue"},
			Raw:    map[string]any{"issue_id": issue.ID, "identifier": issue.Identifier, "url": issue.URL},
		})
	}
	cursor.Since = pollStart
	return m.DB.RecordPoll(src.ID, store.J(cursor), "ok", "")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

const linearCommentMutation = `mutation($issueId: String!, $body: String!) {
  commentCreate(input: {issueId: $issueId, body: $body}) { success }
}`

func postLinearComment(ctx context.Context, m *Manager, apiKey, issueID, body string) error {
	type result struct {
		CommentCreate struct{ Success bool } `json:"commentCreate"`
	}
	res, err := linearRequest[result](ctx, m, apiKey, linearCommentMutation, map[string]any{"issueId": issueID, "body": body})
	if err != nil {
		return err
	}
	if !res.CommentCreate.Success {
		return fmt.Errorf("linear commentCreate did not report success")
	}
	return nil
}

const linearWorkflowStateQuery = `query($teamKey: String!, $name: String!) {
  workflowStates(filter: {team: {key: {eq: $teamKey}}, name: {eq: $name}}, first: 1) {
    nodes { id }
  }
}`

const linearIssueUpdateMutation = `mutation($id: String!, $stateId: String!) {
  issueUpdate(id: $id, input: {stateId: $stateId}) { success }
}`

// moveLinearIssueState resolves DoneStateName to a workflow state id in this
// team and moves the issue there. A team with no state of that name is
// reported rather than guessed at — silently landing on the wrong column is
// worse than leaving the issue where a human put it.
func moveLinearIssueState(ctx context.Context, m *Manager, apiKey, teamKey, stateName, issueID string) error {
	type statesResult struct {
		WorkflowStates struct {
			Nodes []struct{ ID string } `json:"nodes"`
		} `json:"workflowStates"`
	}
	states, err := linearRequest[statesResult](ctx, m, apiKey, linearWorkflowStateQuery,
		map[string]any{"teamKey": teamKey, "name": stateName})
	if err != nil {
		return err
	}
	if len(states.WorkflowStates.Nodes) == 0 {
		return fmt.Errorf("no workflow state named %q on team %q", stateName, teamKey)
	}
	type updateResult struct {
		IssueUpdate struct{ Success bool } `json:"issueUpdate"`
	}
	res, err := linearRequest[updateResult](ctx, m, apiKey, linearIssueUpdateMutation,
		map[string]any{"id": issueID, "stateId": states.WorkflowStates.Nodes[0].ID})
	if err != nil {
		return err
	}
	if !res.IssueUpdate.Success {
		return fmt.Errorf("linear issueUpdate did not report success")
	}
	return nil
}
