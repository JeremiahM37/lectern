package sessions

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// LaunchConfiguration records declared settings, not the target's ambient
// environment or the contents of its credential/configuration files. It is
// private because explicitly configured environment values may contain secrets.
type LaunchConfiguration struct {
	Version     int    `json:"version"`
	Spec        Spec   `json:"spec"`
	Yolo        bool   `json:"yolo"`
	ProfileID   int64  `json:"profile_id,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
	// PermissionMode records whether this launch registered the
	// PermissionRequest hook ("ask") or not ("bypass"/empty), so a
	// continuation of this exact session keeps behaving the same way
	// without the caller having to know or re-specify it.
	PermissionMode string `json:"permission_mode,omitempty"`
	// ProfileInstructions is the selected profile's launch briefing, captured
	// with the rest of the settings so a continuation reuses the briefing the
	// session started with rather than a later edit of the reusable profile.
	// It is text delivered to the agent, never configuration: it cannot change
	// the spec, environment, model or approvals.
	ProfileInstructions string `json:"profile_instructions,omitempty"`
}

// profileBriefing renders a captured profile's instructions as a labelled,
// user-level launch briefing. It returns "" when there is nothing to deliver.
// The caller's own prime and the project/Grimoire context are left intact; this
// text is only ever prepended to them.
func profileBriefing(cfg *LaunchConfiguration) string {
	if cfg == nil {
		return ""
	}
	instructions := strings.TrimSpace(cfg.ProfileInstructions)
	if instructions == "" {
		return ""
	}
	label := strings.TrimSpace(cfg.ProfileName)
	if label == "" {
		label = "launch profile"
	}
	return "Launch profile briefing (\"" + label + "\") — the operator set this as this session's launch briefing:\n" + instructions
}

// ApplyLaunchProfile resolves a named profile before creating a session record.
// Once launched, continuations use the captured configuration, even if the
// reusable profile is edited or deleted later.
func (m *Manager) ApplyLaunchProfile(o LaunchOpts) (LaunchOpts, error) {
	if o.ProfileID == 0 {
		return o, nil
	}
	if o.Configuration != nil {
		return o, fmt.Errorf("a saved continuation cannot replace its launch profile")
	}
	p, err := m.DB.LaunchProfile(o.ProfileID)
	if err != nil {
		return o, fmt.Errorf("launch profile is unavailable")
	}
	if o.Agent != "" && o.Agent != p.Agent {
		return o, fmt.Errorf("launch profile belongs to agent %q", p.Agent)
	}
	cfg, err := m.launchConfiguration(p.Agent, o.ProjectID, nil)
	if err != nil {
		return o, err
	}
	var env map[string]string
	if err := json.Unmarshal([]byte(p.EnvJSON), &env); err != nil {
		return o, fmt.Errorf("launch profile environment is unreadable")
	}
	if _, err := EnvPrefix(env); err != nil {
		return o, err
	}
	for k, v := range env {
		cfg.Spec.Env[k] = v
	}
	if p.Command != "" {
		cfg.Spec.Command = p.Command
	}
	cfg.ProfileID, cfg.ProfileName, cfg.Yolo = p.ID, p.Name, o.Yolo
	cfg.ProfileInstructions = p.Instructions
	o.Configuration, o.Agent = cfg, p.Agent
	if o.Model == "" {
		o.Model = p.Model
	}
	o.ProfileID = 0
	return o, nil
}

// SessionLaunchConfiguration falls back to current settings only for records
// predating snapshots or adopted terminals whose original settings are unknown.
func (m *Manager) SessionLaunchConfiguration(row *store.Session) (*LaunchConfiguration, error) {
	var saved *LaunchConfiguration
	if row.LaunchConfigJSON != "" {
		if err := json.Unmarshal([]byte(row.LaunchConfigJSON), &saved); err != nil || saved == nil {
			return nil, fmt.Errorf("session launch configuration is unreadable")
		}
	}
	return m.launchConfiguration(row.Agent, row.ProjectID, saved)
}

func (m *Manager) launchConfiguration(agent string, projectID *int64, saved *LaunchConfiguration) (*LaunchConfiguration, error) {
	if saved != nil {
		if saved.Version != 1 || saved.Spec.Name != agent || saved.Spec.Command == "" {
			return nil, fmt.Errorf("session launch configuration is unsupported or incomplete")
		}
		if _, err := EnvPrefix(saved.Spec.Env); err != nil {
			return nil, err
		}
		copy := *saved
		return &copy, nil
	}
	spec, ok := Find(m.specs(), agent)
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", agent)
	}
	spec = m.Launcher.resolve(spec)
	env := map[string]string{}
	for k, v := range spec.Env {
		env[k] = v
	}
	for k, v := range m.ProjectEnv(projectID) {
		env[k] = v
	}
	spec.Env = env
	return &LaunchConfiguration{Version: 1, Spec: spec}, nil
}
