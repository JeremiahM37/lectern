package console

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *dashboard) manageProfilesForm() tea.Cmd {
	operation, selectedID := "new", ""
	if len(m.profiles) > 0 {
		operation, selectedID = "edit", id(m.profiles[0])
	}
	return m.openForm("Launch profiles — reusable settings for new sessions", []field{
		optionField("operation", "Action", operation, []choice{{"Create profile", "new"}, {"Edit profile", "edit"}, {"Delete profile", "delete"}}, true),
		optionField("profile", "Saved profile (for edit/delete)", selectedID, options(m.profiles, "No saved profile"), false),
	}, func(body map[string]any) tea.Cmd {
		if body["operation"] == "new" {
			return m.profileSettingsForm(nil)
		}
		var selected row
		for _, p := range m.profiles {
			if id(p) == str(body["profile"]) {
				selected = p
				break
			}
		}
		if selected == nil {
			m.notice = "Choose a saved profile, or create a new one."
			return nil
		}
		if body["operation"] == "delete" {
			m.form = nil
			m.pending = &dashboardAction{Label: "Delete launch profile", Method: "DELETE", Path: "/launch-profiles/" + id(selected), Warning: "Delete " + oneLine(name(selected)) + "? Existing sessions retain their saved launch settings."}
			return nil
		}
		return m.profileSettingsForm(selected)
	})
}

func (m *dashboard) profileSettingsForm(selected row) tea.Cmd {
	agents := []choice{}
	for _, a := range m.agents {
		agents = append(agents, choice{str(a["name"]), str(a["name"])})
	}
	if len(agents) == 0 {
		m.notice = "Agent definitions are unavailable. Refresh and try again."
		return nil
	}
	env := str(selected["env_json"])
	if env == "" {
		env = "{}"
	}
	fields := []field{
		{Key: "name", Label: "Profile name", Value: str(selected["name"]), Required: true},
		optionField("agent", "Agent", str(selected["agent"]), agents, true),
		{Key: "command", Label: "Command override (blank uses agent settings)", Value: str(selected["command"])},
		{Key: "model", Label: "Default model (optional)", Value: str(selected["model"])},
		{Key: "env_json", Label: "Environment JSON (overrides project defaults)", Value: env, Multiline: true},
		{Key: "description", Label: "Description (what this profile is for; shown in lists)", Value: str(selected["description"])},
		{Key: "instructions", Label: "Instructions — launch briefing typed to the agent when a session starts (multiline)", Value: str(selected["instructions"]), Multiline: true},
	}
	return m.openForm("Launch profile — existing sessions keep their settings", fields, func(body map[string]any) tea.Cmd {
		method, path := "POST", "/launch-profiles"
		if selected != nil {
			method, path = "PUT", fmt.Sprintf("/launch-profiles/%s", id(selected))
		}
		// Description and instructions are sent even when blank so clearing a
		// field is an explicit empty value. formBody drops empty fields, so read
		// the live form values: a key that is absent means "leave the stored
		// value alone" for older clients, while "" here means "clear it".
		if m.form != nil {
			for _, f := range m.form.fields {
				if f.Key == "description" || f.Key == "instructions" {
					body[f.Key] = f.Value
				}
			}
		}
		return m.request("Save launch profile", method, path, body, false)
	})
}

func (m *dashboard) profileChoices(empty string) []choice {
	out := []choice{{empty, ""}}
	for _, p := range m.profiles {
		out = append(out, choice{name(p) + " · " + str(p["agent"]), id(p)})
	}
	return out
}
