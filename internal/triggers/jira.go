package triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/trackers"
)

// JiraConfig is trigger_sources.config_json for kind "jira". It works with
// Jira Cloud (email + API token) and Server/Data Center (personal access
// token); Flavor is guessed from the host when empty.
type JiraConfig struct {
	BaseURL    string `json:"base_url"`
	Flavor     string `json:"flavor"`
	Email      string `json:"email"`
	ProjectKey string `json:"project_key"`
	// Label is the issue label that creates a task. Default "lectern".
	Label string `json:"label"`
	// AllowedUsers is who may trigger work: the reporter's email, account
	// id (Cloud), username (Server) or display name. Empty means nobody.
	AllowedUsers []string `json:"allowed_users"`
	// DoneTransition is the transition (or destination status) a finished
	// task moves its issue through. Default "Done".
	DoneTransition string `json:"done_transition"`
	Agent          string `json:"agent"`
	Model          string `json:"model"`
	MaxPerHour     int    `json:"max_per_hour"`
}

func (c *JiraConfig) setDefaults() {
	if c.Label == "" {
		c.Label = "lectern"
	}
	if c.DoneTransition == "" {
		c.DoneTransition = "Done"
	}
	if c.MaxPerHour <= 0 {
		c.MaxPerHour = defaultMaxPerHour
	}
}

func (c JiraConfig) validate() error {
	if strings.TrimSpace(c.ProjectKey) == "" {
		return fmt.Errorf("jira source needs a project_key")
	}
	return nil
}

// ParseJiraConfig reads a source's config_json, filling in defaults.
func ParseJiraConfig(raw string) (JiraConfig, error) {
	var c JiraConfig
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return c, fmt.Errorf("bad jira config: %w", err)
		}
	}
	c.setDefaults()
	return c, nil
}

// JiraSecrets is trigger_sources.secrets_json for kind "jira": an API token
// (Cloud) or a personal access token (Server/Data Center).
type JiraSecrets struct {
	Token string `json:"token"`
}

func ParseJiraSecrets(raw string) (JiraSecrets, error) {
	var s JiraSecrets
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return s, fmt.Errorf("bad jira secrets: %w", err)
		}
	}
	return s, nil
}

func (c JiraConfig) client(httpc *http.Client, s JiraSecrets) *trackers.Jira {
	return &trackers.Jira{BaseURL: c.BaseURL, Flavor: c.Flavor, Email: c.Email, Token: s.Token, HTTP: httpc}
}

// jiraCursor is trigger_sources.cursor_json for kind "jira".
type jiraCursor struct {
	Since string `json:"since"` // RFC3339
}

// jiraSinceClause turns the cursor into JQL. JQL dates are read in the
// Jira user's own time zone, so an absolute UTC timestamp would be off by
// hours; a relative "-Nm" is not. One extra minute covers JQL's minute
// granularity — the event ledger drops anything seen twice.
func jiraSinceClause(since string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return ""
	}
	mins := int(math.Ceil(now.Sub(t).Minutes())) + 1
	if mins < 1 {
		mins = 1
	}
	return fmt.Sprintf(` AND updated >= "-%dm"`, mins)
}

// pollJira lists issues in the project carrying the label, updated since the
// last poll, and files a task for each one an allowlisted reporter raised.
func (m *Manager) pollJira(ctx context.Context, src *store.TriggerSource) error {
	project, err := m.DB.Project(src.ProjectID)
	if err != nil {
		return err
	}
	cfg, err := ParseJiraConfig(src.ConfigJSON)
	if err != nil {
		return err
	}
	secrets, err := ParseJiraSecrets(src.SecretsJSON)
	if err != nil {
		return err
	}
	client := cfg.client(m.HTTP, secrets)
	if err := client.Validate(); err != nil {
		return err
	}
	var cursor jiraCursor
	if src.CursorJSON != "" {
		_ = json.Unmarshal([]byte(src.CursorJSON), &cursor)
	}
	now := m.nowFn().UTC()
	jql := fmt.Sprintf("project = %s AND labels = %s", trackers.JQLString(cfg.ProjectKey), trackers.JQLString(cfg.Label))
	if cursor.Since != "" {
		jql += jiraSinceClause(cursor.Since, now)
	}
	jql += " ORDER BY updated ASC"
	issues, err := client.Search(ctx, jql, 50, "description")
	if err != nil {
		return err
	}
	for _, issue := range issues {
		m.intake(project, src, cfg.AllowedUsers, cfg.Agent, cfg.Model, "", cfg.MaxPerHour,
			jiraCandidate(client, issue, cfg.AllowedUsers))
	}
	return m.DB.RecordPoll(src.ID, store.J(jiraCursor{Since: now.Format(time.RFC3339)}), "ok", "")
}

// jiraCandidate names the reporter by whichever identity the allowlist
// uses: Cloud often hides email addresses, Server has usernames.
func jiraCandidate(client *trackers.Jira, issue trackers.JiraIssue, allow []string) candidate {
	ids := issue.Reporter().Identities()
	author := ""
	for _, id := range ids {
		if authorAllowed(id, allow) {
			author = id
			break
		}
	}
	if author == "" && len(ids) > 0 {
		author = ids[len(ids)-1]
	}
	url := client.BrowseURL(issue.Key)
	return candidate{
		ExternalID: "jira:" + firstNonEmpty(issue.ID, issue.Key),
		Kind:       "jira_issue",
		Author:     author,
		Summary:    issue.Fields.Summary,
		Title:      fmt.Sprintf("[%s] %s", issue.Key, issue.Fields.Summary),
		Prompt:     fmt.Sprintf("Jira issue %s: %s\n\n%s\n\n%s", issue.Key, issue.Fields.Summary, issue.Description(), url),
		Labels:     []string{"jira-issue"},
		Raw:        map[string]any{"issue_key": issue.Key, "url": url},
	}
}

func (m *Manager) postbackJira(ctx context.Context, src *store.TriggerSource, ev *store.TriggerEvent, task *store.Task, att *store.Attempt) error {
	cfg, err := ParseJiraConfig(src.ConfigJSON)
	if err != nil {
		return err
	}
	secrets, err := ParseJiraSecrets(src.SecretsJSON)
	if err != nil {
		return err
	}
	client := cfg.client(m.HTTP, secrets)
	key := rawString(ev.RawJSON, "issue_key")
	if err := client.Comment(ctx, key, summarize(task, att)); err != nil {
		return err
	}
	if task.Status == "failed" || task.Status == "cancelled" {
		return nil // left where a human put it; the comment says why
	}
	return client.TransitionByName(ctx, key, cfg.DoneTransition)
}

func testJira(ctx context.Context, m *Manager, src *store.TriggerSource) (string, error) {
	cfg, err := ParseJiraConfig(src.ConfigJSON)
	if err != nil {
		return "", err
	}
	if err := cfg.validate(); err != nil {
		return "", err
	}
	secrets, err := ParseJiraSecrets(src.SecretsJSON)
	if err != nil {
		return "", err
	}
	client := cfg.client(m.HTTP, secrets)
	who, err := client.Myself(ctx)
	if err != nil {
		return "", err
	}
	if _, err := client.Search(ctx, "project = "+trackers.JQLString(cfg.ProjectKey), 1); err != nil {
		return "", fmt.Errorf("signed in as %s, but cannot search project %s: %w", who, cfg.ProjectKey, err)
	}
	return fmt.Sprintf("Jira token works as %s and can search project %s", who, cfg.ProjectKey), nil
}
