package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/internal/sessions"
)

const agentRetentionKey = "__lectern_retained"

var agentSecretKey = regexp.MustCompile(`(?i)(^|[_-])(key|token|secret|password|credential|private|authorization)($|[_-])`)
var agentOpaqueConfigKey = regexp.MustCompile(`(?i)(^|[_-])(config|credentials?)[_-]?(content|json|data)$`)

func isAgentSecretEnv(key string) bool {
	return agentSecretKey.MatchString(key) || agentOpaqueConfigKey.MatchString(key)
}

func (s *Server) agentMarker(name, key string) map[string]any {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if len(s.agentKey) == 0 {
		s.agentKey = make([]byte, 32)
		if _, err := rand.Read(s.agentKey); err != nil {
			panic("cannot seed agent secret retention key")
		}
	}
	h := hmac.New(sha256.New, s.agentKey)
	_, _ = h.Write([]byte("agent-retention\x00" + name + "\x00" + key))
	return map[string]any{agentRetentionKey: hex.EncodeToString(h.Sum(nil))}
}

func agentMarkerValue(value any) (string, bool) {
	m, ok := value.(map[string]any)
	if !ok || len(m) != 1 {
		return "", false
	}
	token, ok := m[agentRetentionKey].(string)
	return token, ok && token != ""
}

func (s *Server) restoreAgentMarker(value any, prior map[string]string, name, key string) (any, error) {
	token, ok := agentMarkerValue(value)
	if !ok {
		return value, nil
	}
	expected := s.agentMarker(name, key)[agentRetentionKey].(string)
	old, exists := prior[key]
	if token != expected || !exists {
		return nil, fmt.Errorf("secret retention token for %s.%s is invalid or expired; reload agent settings", name, key)
	}
	return old, nil
}

// agentViews are deliberately built from the stored specs rather than by
// mutating them: launchers always receive the real environment, while HTTP
// clients receive a typed marker for credential-shaped values.
func (s *Server) agentViews(specs []sessions.Spec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		raw, _ := json.Marshal(spec)
		var view map[string]any
		if json.Unmarshal(raw, &view) != nil {
			continue
		}
		if len(spec.Env) != 0 {
			env := make(map[string]any, len(spec.Env))
			for key, value := range spec.Env {
				if isAgentSecretEnv(key) {
					env[key] = s.agentMarker(spec.Name, key)
				} else {
					env[key] = value
				}
			}
			view["env"] = env
		}
		out = append(out, view)
	}
	return out
}

func (s *Server) agentConfigWithRetainedSecrets(body string) (string, error) {
	var entries []map[string]any
	if err := json.Unmarshal([]byte(body), &entries); err != nil {
		return "", fmt.Errorf("agents must be a list of objects: %w", err)
	}
	old := s.agentSpecs()
	oldEnv := make(map[string]map[string]string, len(old))
	for _, spec := range old {
		if spec.Env != nil {
			oldEnv[spec.Name] = spec.Env
		}
	}
	for _, entry := range entries {
		name, _ := entry["name"].(string)
		rawEnv, exists := entry["env"]
		if !exists {
			continue
		}
		env, ok := rawEnv.(map[string]any)
		if !ok {
			continue // ValidateSpecs supplies the actionable type error.
		}
		prior := oldEnv[name]
		for key, value := range env {
			restored, err := s.restoreAgentMarker(value, prior, name, key)
			if err != nil {
				return "", err
			}
			if isAgentSecretEnv(key) {
				if literal, ok := restored.(string); ok && isAgentSecretPlaceholder(literal) {
					return "", fmt.Errorf("agent %s.%s uses a secret placeholder string; send the typed retention marker returned by GET /api/agents", name, key)
				}
			}
			env[key] = restored
		}
	}
	normalized, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("encode agents: %w", err)
	}
	return string(normalized), nil
}

func isAgentSecretPlaceholder(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "__keep__", "[secret retained]", "[redacted]", "<redacted>", "***", "••••", "••••••":
		return true
	default:
		return false
	}
}

func (s *Server) knownAgent(name string) bool {
	_, ok := sessions.Find(s.agentSpecs(), name)
	return ok
}

func (s *Server) knownAgentNames() []string {
	specs := s.agentSpecs()
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

func (s *Server) taskAgent(name string) (sessions.Spec, bool) {
	spec, ok := sessions.Find(s.agentSpecs(), name)
	if !ok || (!spec.Builtin && spec.Task == nil) {
		return sessions.Spec{}, false
	}
	return spec, true
}

func taskPermissionError(spec sessions.Spec, mode string) error {
	switch mode {
	case "default":
		if !spec.Builtin || spec.Name != "claude" {
			return fmt.Errorf("agent %q does not support gated approvals; use acceptEdits/plan", spec.Name)
		}
	case "plan", "bypassPermissions":
		if !spec.Builtin {
			if spec.Task == nil {
				return fmt.Errorf("agent %q has no non-interactive task definition", spec.Name)
			}
			args, ok := spec.Task.PermissionArgs[mode]
			if !ok || !sessions.TaskPermissionArgsConfigured(args) {
				return fmt.Errorf("agent %q does not support permission mode %q; configure task.permission_args or use acceptEdits", spec.Name, mode)
			}
		}
	}
	return nil
}
