package console

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// manageAgentsForm is the terminal-only path for adding a runner. The web
// editor has friendly controls, while the TUI keeps the same fields available
// over SSH and for screen readers. Provider configuration is optional; the
// command remains the required executable boundary.
func (m *dashboard) manageAgentsForm() tea.Cmd {
	choices := []choice{{"New custom runner", ""}}
	for _, agent := range m.agents {
		choices = append(choices, choice{str(agent["name"]), str(agent["name"])})
	}
	return m.openForm("Agent runners — command plus optional provider settings", []field{
		optionField("agent", "Edit existing runner (blank creates a new one)", "", choices, false),
		optionField("operation", "Action", "edit", []choice{{"Edit", "edit"}, {"Remove", "remove"}}, true),
	}, func(body map[string]any) tea.Cmd {
		name := str(body["agent"])
		if name == "" {
			return m.agentDefinitionForm(nil)
		}
		for _, agent := range m.agents {
			if str(agent["name"]) == name {
				if str(body["operation"]) == "remove" {
					if agent["builtin"] == true {
						m.notice = "Built-in agents cannot be removed; edit one to create an override."
						return nil
					}
					var next []map[string]any
					for _, current := range m.agents {
						if current["builtin"] != true && str(current["name"]) != name {
							next = append(next, map[string]any(current))
						}
					}
					return m.request("Remove agent runner", "PUT", "/agents", next, false)
				}
				return m.agentDefinitionForm(agent)
			}
		}
		m.notice = "That runner disappeared; refresh and try again."
		return nil
	})
}

func agentJSON(value any) string {
	if value == nil {
		return "[]"
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func maskedAgentEnv(source row) string {
	if source == nil || source["env"] == nil {
		return "{}"
	}
	env, ok := source["env"].(map[string]any)
	if !ok {
		return "{}"
	}
	masked := make(map[string]string, len(env))
	for key := range env {
		masked[key] = "__KEEP__"
	}
	return agentJSON(masked)
}

func sourceEnvMap(source row) map[string]any {
	if source == nil {
		return nil
	}
	env, _ := source["env"].(map[string]any)
	return env
}

func cloneAgent(source row) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	delete(out, "id")
	delete(out, "builtin")
	return out
}

var providerEndpointNames = []string{"OPENAI_API_BASE", "OPENAI_BASE_URL", "ANTHROPIC_BASE_URL", "OLLAMA_HOST", "BASE_URL"}

func agentProviderEnv(source row) string {
	if source != nil {
		if key := str(source["provider_env"]); key != "" {
			return key
		}
		if env, ok := source["env"].(map[string]any); ok {
			for _, key := range providerEndpointNames {
				if str(env[key]) != "" {
					return key
				}
			}
		}
	}
	return "OPENAI_API_BASE"
}

func agentProviderURL(source row) string {
	if source == nil {
		return ""
	}
	if value := str(source["provider_url"]); value != "" {
		return value
	}
	if env, ok := source["env"].(map[string]any); ok {
		key := agentProviderEnv(source)
		value := str(env[key])
		if value != "••••" && value != "***" && value != "__KEEP__" {
			return value
		}
	}
	return ""
}

func (m *dashboard) agentDefinitionForm(source row) tea.Cmd {
	name, command, provider := "", "", ""
	var task map[string]any
	if source != nil {
		name, command, provider = str(source["name"]), str(source["command"]), agentProviderURL(source)
		task, _ = source["task"].(map[string]any)
	}
	env := maskedAgentEnv(source)
	preset := "custom"
	if command == "opencode" {
		preset = "opencode"
	}
	if command == "aider" {
		preset = "aider"
	}
	taskLabel := "Enable background tasks (separate one-shot command)"
	if source != nil && source["builtin"] == true {
		taskLabel += "; off makes this override interactive-only"
	}
	fields := []field{
		optionField("preset", "Starter template (advanced fields below)", preset, []choice{{"Custom runner", "custom"}, {"OpenCode", "opencode"}, {"Aider", "aider"}}, false),
		{Key: "name", Label: "Runner name", Value: name},
		{Key: "command", Label: "Interactive command (required)", Value: command},
		{Key: "args_json", Label: "Fixed interactive arguments (JSON array)", Value: agentJSON(sourceValue(source, "args")), Multiline: true},
		{Key: "model_flag", Label: "Model flag (blank if unsupported)", Value: str(sourceValue(source, "model_flag"))},
		{Key: "provider_url", Label: "Provider endpoint URL (optional)", Value: provider},
		{Key: "provider_env", Label: "Provider URL environment variable", Value: agentProviderEnv(source)},
		boolField("prompt_arg", "Opening prompt is a positional argument", source != nil && source["prompt_arg"] == true),
		{Key: "resume_args_json", Label: "Resume arguments (JSON array)", Value: agentJSON(sourceValue(source, "resume_args")), Multiline: true},
		{Key: "resume_id_args_json", Label: "Resume ID arguments (JSON array; {id}/{dir})", Value: agentJSON(sourceValue(source, "resume_id_args")), Multiline: true},
		{Key: "fork_args_json", Label: "Fork arguments (JSON array; {id}/{dir})", Value: agentJSON(sourceValue(source, "fork_args")), Multiline: true},
		{Key: "trust_command", Label: "Trust command (optional; {dir})", Value: str(sourceValue(source, "trust_command"))},
		{Key: "models_command", Label: "Model catalog command (optional; {bin})", Value: str(sourceValue(source, "models_command"))},
		{Key: "yolo_args_json", Label: "Yolo arguments (JSON array)", Value: agentJSON(sourceValue(source, "yolo_args")), Multiline: true},
		{Key: "env", Label: "Environment JSON (saved credentials are masked in this form)", Value: env, Multiline: true},
		boolField("task_enabled", taskLabel, task != nil),
		{Key: "task_command", Label: "Task command (blank uses interactive command)", Value: str(taskValue(task, "command"))},
		{Key: "task_args_json", Label: "Task arguments (JSON array)", Value: agentJSON(taskValue(task, "args")), Multiline: true},
		{Key: "task_prompt_template", Label: "Task prompt template ({prompt}, {prompt_file}, or stdin)", Value: str(taskValue(task, "prompt_template"))},
		optionField("task_output", "Task output", str(taskValue(task, "output_mode")), []choice{{"Plain text", "plain"}, {"JSONL events", "jsonl"}}, false),
		{Key: "task_permissions_json", Label: "Task permission arguments (JSON object)", Value: objectJSON(taskValue(task, "permission_args")), Multiline: true},
	}
	return m.openForm("Agent runner — save to all session/task selectors", fields, func(body map[string]any) tea.Cmd {
		parseArray := func(key string) ([]string, error) {
			var values []string
			if raw := strings.TrimSpace(str(body[key])); raw != "" {
				if err := json.Unmarshal([]byte(raw), &values); err != nil {
					return nil, fmt.Errorf("%s must be a JSON array of strings", key)
				}
			}
			return values, nil
		}
		args, err := parseArray("args_json")
		if err != nil {
			m.notice = err.Error()
			return nil
		}
		resume, err := parseArray("resume_args_json")
		if err != nil {
			m.notice = err.Error()
			return nil
		}
		yolo, err := parseArray("yolo_args_json")
		if err != nil {
			m.notice = err.Error()
			return nil
		}
		resumeID, err := parseArray("resume_id_args_json")
		if err != nil {
			m.notice = err.Error()
			return nil
		}
		fork, err := parseArray("fork_args_json")
		if err != nil {
			m.notice = err.Error()
			return nil
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(str(body["env"])), &env); err != nil || env == nil || !validAgentEnv(env) {
			m.notice = "Environment must be a JSON object of string values (or retained secret markers)."
			return nil
		}
		preset := str(body["preset"])
		if preset == "opencode" {
			if str(body["name"]) == "" {
				body["name"] = "opencode"
			}
			if str(body["command"]) == "" {
				body["command"] = "opencode"
			}
			if str(body["model_flag"]) == "" {
				body["model_flag"] = "--model"
			}
		}
		if preset == "aider" {
			if str(body["name"]) == "" {
				body["name"] = "aider"
			}
			if str(body["command"]) == "" {
				body["command"] = "aider"
			}
			if str(body["model_flag"]) == "" {
				body["model_flag"] = "--model"
			}
			if str(body["provider_env"]) == "OPENAI_BASE_URL" {
				body["provider_env"] = "OPENAI_API_BASE"
			}
		}
		if preset == "opencode" && strings.TrimSpace(str(body["provider_url"])) != "" {
			m.notice = "OpenCode uses its configured provider settings; add OPENCODE_CONFIG_CONTENT in Environment."
			return nil
		}
		if strings.TrimSpace(str(body["name"])) == "" || strings.TrimSpace(str(body["command"])) == "" {
			m.notice = "Runner name and interactive command are required."
			return nil
		}
		endpointEnv := strings.TrimSpace(str(body["provider_env"]))
		if endpointEnv == "" {
			endpointEnv = "OPENAI_BASE_URL"
		}
		if !validEnvName(endpointEnv) {
			m.notice = "Provider environment name is invalid."
			return nil
		}
		if endpoint := strings.TrimSpace(str(body["provider_url"])); endpoint != "" {
			env[endpointEnv] = endpoint
		}
		if source != nil {
			for key, value := range sourceEnvMap(source) {
				if env[key] == "__KEEP__" {
					// Agent GET redacts secret values as a typed retention marker.
					// Preserve that object through PUT; converting it with str()
					// would store the marker text as the credential.
					env[key] = value
				}
			}
		}
		spec := map[string]any{"name": str(body["name"]), "command": str(body["command"]), "args": args,
			"model_flag": str(body["model_flag"]), "prompt_arg": body["prompt_arg"] == true,
			"resume_args": resume, "resume_id_args": resumeID, "fork_args": fork,
			"trust_command": str(body["trust_command"]), "models_command": str(body["models_command"]),
			"yolo_args": yolo, "env": env}
		if body["task_enabled"] == true {
			taskArgs, parseErr := parseArray("task_args_json")
			if parseErr != nil {
				m.notice = parseErr.Error()
				return nil
			}
			var permissions map[string][]string
			if err := json.Unmarshal([]byte(str(body["task_permissions_json"])), &permissions); err != nil || permissions == nil {
				m.notice = "Task permission arguments must be a JSON object of string arrays."
				return nil
			}
			spec["task"] = map[string]any{"command": str(body["task_command"]), "args": taskArgs,
				"prompt_template": str(body["task_prompt_template"]), "output_mode": str(body["task_output"]), "permission_args": permissions}
		}
		var next []map[string]any
		for _, agent := range m.agents {
			if agent["builtin"] == true {
				if source != nil && str(agent["name"]) == str(source["name"]) {
					// Explicitly editing a built-in creates a custom override.
					preserved := cloneAgent(source)
					for key, value := range spec {
						preserved[key] = value
					}
					next = append(next, preserved)
				}
				continue
			}
			if source != nil && str(agent["name"]) == str(source["name"]) {
				preserved := cloneAgent(source)
				for key, value := range spec {
					preserved[key] = value
				}
				next = append(next, preserved)
			} else {
				next = append(next, map[string]any(agent))
			}
		}
		if source == nil {
			next = append(next, spec)
		}
		return m.request("Save agent runner", "PUT", "/agents", next, false)
	})
}

func validEnvName(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func validAgentEnv(env map[string]any) bool {
	for _, value := range env {
		if _, ok := value.(string); ok {
			continue
		}
		marker, ok := value.(map[string]any)
		if !ok || len(marker) != 1 {
			return false
		}
		if token, ok := marker["__lectern_retained"].(string); !ok || token == "" {
			return false
		}
	}
	return true
}

func sourceValue(source row, key string) any {
	if source == nil {
		return nil
	}
	return source[key]
}

func taskValue(task map[string]any, key string) any {
	if task == nil {
		return nil
	}
	return task[key]
}

func objectJSON(value any) string {
	if value == nil {
		return "{}"
	}
	return agentJSON(value)
}
