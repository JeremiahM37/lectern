package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/sessions"
)

const agentRetentionKey = "__lectern_retained"

var agentSecretKey = regexp.MustCompile(`(?i)(^|[_-])(key|token|secret|password|credential|private|authorization)($|[_-])`)
var agentOpaqueConfigKey = regexp.MustCompile(`(?i)(^|[_-])(config|credentials?)[_-]?(content|json|data)$`)

func isAgentSecretEnv(key string) bool {
	return agentSecretKey.MatchString(key) || agentOpaqueConfigKey.MatchString(key)
}

// field distinguishes WHICH env map a marker belongs to — "env" (agent-wide)
// or "acp.env" (internal/sessions.ACPSpec.Env) — so a marker minted for one
// can never be replayed to unlock a value that lives in the other; it is
// folded into the HMAC input, not just a map key, precisely so a client
// cannot forge that separation itself.
func (s *Server) agentMarker(name, field, key string) map[string]any {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if len(s.agentKey) == 0 {
		s.agentKey = make([]byte, 32)
		if _, err := rand.Read(s.agentKey); err != nil {
			panic("cannot seed agent secret retention key")
		}
	}
	h := hmac.New(sha256.New, s.agentKey)
	_, _ = h.Write([]byte("agent-retention\x00" + name + "\x00" + field + "\x00" + key))
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

func (s *Server) restoreAgentMarker(value any, prior map[string]string, name, field, key string) (any, error) {
	token, ok := agentMarkerValue(value)
	if !ok {
		return value, nil
	}
	expected := s.agentMarker(name, field, key)[agentRetentionKey].(string)
	old, exists := prior[key]
	if token != expected || !exists {
		return nil, fmt.Errorf("secret retention token for %s.%s.%s is invalid or expired; reload agent settings", name, field, key)
	}
	return old, nil
}

// maskEnvView masks credential-shaped values of one env map for an HTTP
// response, sharing isAgentSecretEnv's detection with agentConfigWithRetainedSecrets
// so a value that round-trips masked here is exactly the one accepted back
// as a retention marker there.
func (s *Server) maskEnvView(name, field string, raw map[string]string) map[string]any {
	out := make(map[string]any, len(raw))
	for key, value := range raw {
		if isAgentSecretEnv(key) {
			out[key] = s.agentMarker(name, field, key)
		} else {
			out[key] = value
		}
	}
	return out
}

// agentViews are deliberately built from the stored specs rather than by
// mutating them: launchers always receive the real environment, while HTTP
// clients receive a typed marker for credential-shaped values — both the
// agent-wide `env` and, when set, `acp.env` (internal/sessions.ACPSpec.Env is
// exactly as capable of holding a provider credential as the agent-wide one,
// e.g. an ANTHROPIC_API_KEY scoped to a hosted ACP proxy).
func (s *Server) agentViews(specs []sessions.Spec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		raw, _ := json.Marshal(spec)
		var view map[string]any
		if json.Unmarshal(raw, &view) != nil {
			continue
		}
		if len(spec.Env) != 0 {
			view["env"] = s.maskEnvView(spec.Name, "env", spec.Env)
		}
		if spec.ACP != nil && len(spec.ACP.Env) != 0 {
			if acpView, ok := view["acp"].(map[string]any); ok {
				acpView["env"] = s.maskEnvView(spec.Name, "acp.env", spec.ACP.Env)
			}
		}
		out = append(out, view)
	}
	return out
}

// restoreEnvEntry rewrites one already-decoded env map in place, resolving
// any retention marker back to its stored value and rejecting a literal
// placeholder string sent by a client that meant to keep a secret but
// dropped the typed marker (see isAgentSecretPlaceholder).
func (s *Server) restoreEnvEntry(env map[string]any, prior map[string]string, name, field string) error {
	for key, value := range env {
		restored, err := s.restoreAgentMarker(value, prior, name, field, key)
		if err != nil {
			return err
		}
		if isAgentSecretEnv(key) {
			if literal, ok := restored.(string); ok && isAgentSecretPlaceholder(literal) {
				return fmt.Errorf("agent %s.%s.%s uses a secret placeholder string; send the typed retention marker returned by GET /api/agents", name, field, key)
			}
		}
		env[key] = restored
	}
	return nil
}

func (s *Server) agentConfigWithRetainedSecrets(body string) (string, error) {
	var entries []map[string]any
	if err := json.Unmarshal([]byte(body), &entries); err != nil {
		return "", fmt.Errorf("agents must be a list of objects: %w", err)
	}
	old := s.agentSpecs()
	oldEnv := make(map[string]map[string]string, len(old))
	oldACPEnv := make(map[string]map[string]string, len(old))
	for _, spec := range old {
		if spec.Env != nil {
			oldEnv[spec.Name] = spec.Env
		}
		if spec.ACP != nil && spec.ACP.Env != nil {
			oldACPEnv[spec.Name] = spec.ACP.Env
		}
	}
	for _, entry := range entries {
		name, _ := entry["name"].(string)
		if rawEnv, exists := entry["env"]; exists {
			if env, ok := rawEnv.(map[string]any); ok {
				if err := s.restoreEnvEntry(env, oldEnv[name], name, "env"); err != nil {
					return "", err
				}
			} // else: ValidateSpecs supplies the actionable type error.
		}
		if rawACP, exists := entry["acp"]; exists {
			if acp, ok := rawACP.(map[string]any); ok {
				if rawEnv, exists := acp["env"]; exists {
					if env, ok := rawEnv.(map[string]any); ok {
						if err := s.restoreEnvEntry(env, oldACPEnv[name], name, "acp.env"); err != nil {
							return "", err
						}
					}
				}
			}
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
	if !ok || (!spec.Builtin && spec.Task == nil && spec.ACP == nil) {
		return sessions.Spec{}, false
	}
	return spec, true
}

// taskPermissionError rejects a permission mode a dispatch cannot honour
// before an attempt is ever queued.
//
// An ACP agent (spec.ACP != nil) is deliberately exempt from every branch
// below: internal/drivers.acpDriver routes session/request_permission
// through the same broker codex-appserver uses for EVERY Lectern permission
// mode — "default" gates every call, "bypassPermissions" auto-selects the
// protocol's own allow_always option, and "plan"/"acceptEdits" fall back to
// the same auto-allow as bypass (the protocol has no separate notion of
// those two — see docs/acp.md) — so unlike a generic Task-backed custom CLI,
// there is no missing capability to reject here.
func taskPermissionError(spec sessions.Spec, mode string) error {
	if spec.ACP != nil {
		return nil
	}
	switch mode {
	case "default":
		// claude: hook.py's PreToolUse gate (unchanged). codex: the
		// codex-appserver driver's approval routing — codex has no
		// PreToolUse-equivalent hook, so this was rejected outright before
		// that driver existed; see internal/drivers.Select.
		if !spec.Builtin || (spec.Name != "claude" && spec.Name != "codex") {
			return fmt.Errorf("agent %q does not support gated approvals; use acceptEdits/plan", spec.Name)
		}
	case "steerable":
		if !spec.Builtin || spec.Name != "claude" {
			return fmt.Errorf("agent %q does not support the steerable driver; use claude", spec.Name)
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

// ---- agent catalog & shown-agent menu (Settings → Agents "Add from
// catalog", and the "shown agents" list every picker reads before falling
// back to "More agents…") ----

// agentMenuSettingKey is the DB.Setting key for the ordered list of agent
// names pickers show first. There is no multi-user model in this codebase —
// Lectern is a single-operator control plane — so, exactly like the
// "agents" setting itself, this is one shared ordering rather than a
// per-account preference.
const agentMenuSettingKey = "agent_menu"

// agentCatalogView is one sessions.CatalogPreset plus whether its binary is
// on PATH for this host, and whether an agent by the same name is already
// registered — so Settings can grey out "Add" for one already added instead
// of letting an operator create a second definition under the same name.
func (s *Server) agentCatalogView(specs []sessions.Spec) []map[string]any {
	already := map[string]bool{}
	for _, spec := range specs {
		already[spec.Name] = true
	}
	catalog := sessions.Catalog()
	out := make([]map[string]any, 0, len(catalog))
	for _, preset := range catalog {
		raw, err := json.Marshal(preset)
		if err != nil {
			continue
		}
		var view map[string]any
		if json.Unmarshal(raw, &view) != nil {
			continue
		}
		view["installed"] = preset.Installed()
		view["added"] = already[preset.Name]
		out = append(out, view)
	}
	return out
}

func (s *Server) listAgentCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.agentCatalogView(s.agentSpecs()))
}

// agentCapabilities is GET /api/agents/capabilities: name -> {capabilities,
// installed, builtin}. Deliberately a separate endpoint from GET
// /api/agents rather than extra fields folded into that response: the
// frontend keeps the agents list it fetches from GET /api/agents around and
// re-sends it (minus "builtin") as the custom-agent array on PUT
// /api/agents, so any computed field added there would round-trip straight
// back — and an untouched builtin carrying it would stop comparing equal in
// NormalizeBuiltinEntries, silently turning it into a persisted custom
// override.
func (s *Server) agentCapabilities(w http.ResponseWriter, r *http.Request) {
	specs := s.agentSpecs()
	out := make(map[string]map[string]any, len(specs))
	for _, spec := range specs {
		out[spec.Name] = map[string]any{
			"capabilities": spec.Capabilities(),
			"installed":    spec.Installed(),
			"builtin":      spec.Builtin,
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

// defaultAgentMenu is "installed built-ins" — what a fresh install shows in
// every picker before Settings → Agents has ever been touched. If none of
// the built-ins are installed (a fresh box with none of claude/codex/gemini
// on PATH yet, or a test/CI environment) every built-in is shown anyway
// rather than leaving pickers empty.
// defaultAgentMenu matches by the built-in agents' fixed NAMES
// (sessions.Builtins()), not by whether the CURRENTLY registered spec under
// that name still carries Builtin:true. Overriding a built-in by name (a
// corrected command, a local stub for testing) is meant to be a transparent
// swap — see ParseSpecs's own doc comment — and that transparency has to
// extend to which agents default to shown, or saving any override of
// claude/codex/gemini would silently evict it from every picker.
func defaultAgentMenu(specs []sessions.Spec) []string {
	builtinNames := map[string]bool{}
	for _, b := range sessions.Builtins() {
		builtinNames[b.Name] = true
	}
	var installed, named []string
	for _, spec := range specs {
		if !builtinNames[spec.Name] {
			continue
		}
		named = append(named, spec.Name)
		if spec.Installed() {
			installed = append(installed, spec.Name)
		}
	}
	if len(installed) > 0 {
		return installed
	}
	return named
}

// agentMenu is the ordered list of agent names a picker shows before "More
// agents…". A name for an agent since removed from the registry is dropped
// rather than surfaced as an entry that would 422 the moment it is chosen.
func (s *Server) agentMenu() []string {
	specs := s.agentSpecs()
	known := map[string]bool{}
	for _, spec := range specs {
		known[spec.Name] = true
	}
	raw := strings.TrimSpace(s.DB.Setting(agentMenuSettingKey))
	if raw == "" {
		return defaultAgentMenu(specs)
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return defaultAgentMenu(specs)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if known[name] && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return defaultAgentMenu(specs)
	}
	return out
}

func (s *Server) getAgentMenu(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"agents": s.agentMenu()})
}

// putAgentMenu replaces the shown/ordered agent list — Settings → Agents'
// toggle-and-reorder control. An unknown name is rejected outright rather
// than silently dropped, so a typo fails loudly at save time exactly like an
// unknown agent name anywhere else in the registry.
func (s *Server) putAgentMenu(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Agents []string `json:"agents"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	specs := s.agentSpecs()
	known := map[string]bool{}
	for _, spec := range specs {
		known[spec.Name] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(body.Agents))
	for _, name := range body.Agents {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		if !known[name] {
			httpError(w, 422, "unknown agent %q — define it in /api/agents first", name)
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		respondErr(w, err)
		return
	}
	if err := s.DB.SetSetting(agentMenuSettingKey, string(encoded)); err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"agents": out})
}
