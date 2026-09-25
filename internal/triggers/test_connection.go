package triggers

import (
	"context"
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// TestConnection is the settings UI's "test connection" button: it makes one
// cheap, read-only call against the source's own configuration and returns a
// human-readable result, without touching the poll cursor or event ledger.
func (m *Manager) TestConnection(ctx context.Context, src *store.TriggerSource) (string, error) {
	project, err := m.DB.Project(src.ProjectID)
	if err != nil {
		return "", fmt.Errorf("project is gone: %w", err)
	}
	switch Kind(src.Kind) {
	case KindGitHub:
		return m.testGitHub(ctx, project, src)
	case KindSlack:
		return testSlack(ctx, m, src)
	case KindLinear:
		return testLinear(ctx, m, src)
	default:
		return "", fmt.Errorf("unknown trigger kind %q", src.Kind)
	}
}

func (m *Manager) testGitHub(ctx context.Context, project *store.Project, src *store.TriggerSource) (string, error) {
	cfg, err := ParseGitHubConfig(src.ConfigJSON)
	if err != nil {
		return "", err
	}
	if err := cfg.validate(); err != nil {
		return "", err
	}
	ex, err := m.projectExecutor(project)
	if err != nil {
		return "", err
	}
	res, err := ex.Run(ctx, "gh api "+executor.ShellQuote("repos/"+cfg.Repo), executor.RunOpts{Timeout: 20})
	if err != nil {
		return "", err
	}
	if !res.OK() {
		return "", fmt.Errorf("gh could not read %s on %s: %s", cfg.Repo, project.TargetName, truncate(res.Stdout+res.Stderr, 300))
	}
	return fmt.Sprintf("gh on %s can read %s", project.TargetName, cfg.Repo), nil
}

func testSlack(ctx context.Context, m *Manager, src *store.TriggerSource) (string, error) {
	secrets, err := ParseSlackSecrets(src.SecretsJSON)
	if err != nil {
		return "", err
	}
	if err := secrets.validate(); err != nil {
		return "", err
	}
	wsURL, err := slackOpenConnection(ctx, m, secrets.AppToken)
	if err != nil {
		return "", fmt.Errorf("app token: %w", err)
	}
	if wsURL == "" {
		return "", fmt.Errorf("slack did not return a socket URL")
	}
	return "Slack app token can open a Socket Mode connection", nil
}

func testLinear(ctx context.Context, m *Manager, src *store.TriggerSource) (string, error) {
	cfg, err := ParseLinearConfig(src.ConfigJSON)
	if err != nil {
		return "", err
	}
	if err := cfg.validate(); err != nil {
		return "", err
	}
	secrets, err := ParseLinearSecrets(src.SecretsJSON)
	if err != nil {
		return "", err
	}
	if err := secrets.validate(); err != nil {
		return "", err
	}
	type teamResult struct {
		Teams struct {
			Nodes []struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	const q = `query($key: String!) { teams(filter: {key: {eq: $key}}, first: 1) { nodes { key name } } }`
	res, err := linearRequest[teamResult](ctx, m, secrets.APIKey, q, map[string]any{"key": cfg.TeamKey})
	if err != nil {
		return "", err
	}
	if len(res.Teams.Nodes) == 0 {
		return "", fmt.Errorf("no Linear team with key %q is visible to this API key", cfg.TeamKey)
	}
	return fmt.Sprintf("Linear API key can read team %q (%s)", res.Teams.Nodes[0].Key, res.Teams.Nodes[0].Name), nil
}
