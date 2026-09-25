package agents

import (
	"fmt"
	"sort"
	"strings"
)

// Names are the agents lectern knows how to launch.
var Names = []string{"claude", "codex", "gemini"}

// GatedCapable are the agents that support the hook-gated 'default' permission
// mode. codex and gemini have no PreToolUse equivalent, so a gated task on them
// is rejected before dispatch rather than silently running ungated.
var GatedCapable = map[string]bool{"claude": true}

// codexSandbox maps lectern's permission modes onto codex sandbox policies
// (codex >= 0.140; the older --full-auto was removed upstream).
var codexSandbox = map[string][]string{
	"plan":              {"--sandbox", "read-only"},
	"acceptEdits":       {"--sandbox", "workspace-write"},
	"bypassPermissions": {"--dangerously-bypass-approvals-and-sandbox"},
}

// Launcher builds the shell command that starts an agent in a tmux session.
type Launcher struct {
	ClaudeBin string
	CodexBin  string
	GeminiBin string
}

// TaskDefinition describes the non-interactive invocation for a configured
// agent. Built-in agents keep their dedicated adapters; custom definitions use
// this deliberately small contract so a CLI can be backed by any provider or
// local model through its environment.
type TaskDefinition struct {
	Name           string   `json:"name"`
	Command        string   `json:"command"`
	Args           []string `json:"args,omitempty"`
	ModelFlag      string   `json:"model_flag,omitempty"`
	PromptArg      bool     `json:"prompt_arg,omitempty"`
	PromptTemplate string   `json:"prompt_template,omitempty"`
	OutputMode     string   `json:"output_mode,omitempty"`
	// PermissionArgs explicitly maps Lectern modes to this CLI's flags.
	// acceptEdits may be omitted when the CLI's native default is acceptable;
	// plan and bypassPermissions require an explicit mapping.
	PermissionArgs map[string][]string `json:"permission_args,omitempty"`
	ResumeArgs     []string            `json:"resume_args,omitempty"`
	Env            map[string]string   `json:"env,omitempty"`
	Builtin        bool                `json:"builtin,omitempty"`
	// ACP, when set, means this agent's non-interactive invocation is an
	// Agent Client Protocol (agentclientprotocol.com) process rather than a
	// plain-text/JSONL CLI: internal/drivers.KindACP drives it instead of
	// the generic task path, and PromptTemplate/OutputMode/PermissionArgs
	// above are ignored (the protocol carries the prompt and permission
	// decisions itself). See internal/drivers/acp.go.
	ACP *ACPDefinition `json:"acp,omitempty"`
}

// ACPDefinition is a custom agent's ACP invocation: the command to spawn (an
// ACP agent speaks JSON-RPC over its own stdio, same as codex app-server),
// e.g. Command:"npx" Args:["-y","@zed-industries/claude-code-acp"].
type ACPDefinition struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// TaskLaunchConfig is the immutable, private snapshot attached to a queued
// attempt. It keeps registry and project environment edits from changing work
// that was already accepted by the operator.
type TaskLaunchConfig struct {
	Version    int               `json:"version"`
	Agent      string            `json:"agent"`
	Definition TaskDefinition    `json:"definition"`
	Env        map[string]string `json:"env"`
}

// LaunchSpec is one attempt's launch parameters.
type LaunchSpec struct {
	Agent          string
	Worktree       string
	TmuxSession    string
	PermissionMode string
	Model          string
	ResumeSession  string
	Sandbox        bool
	Env            map[string]string
	SettingsPath   string
	MCPConfig      string
	StrictMCP      bool
	// Definition is set for a configured custom agent. Leaving it nil preserves
	// the existing built-in adapter behavior and its provider-specific flags.
	Definition *TaskDefinition
	// ExtraArgs are provider-specific flags, already validated by the adapter.
	ExtraArgs []string
}

// EnvPrefix renders the shell prefix of KEY=VAL pairs injected before the agent
// binary.
//
// This is the any-model door: point a project at any Anthropic-compatible
// endpoint (Ollama >= 0.20 natively, LiteLLM, llama.cpp, vLLM gateways) via
// ANTHROPIC_BASE_URL / ANTHROPIC_AUTH_TOKEN, or set OPENAI_*/GEMINI_* for the
// other agents. Values are shell-quoted; keys are validated.
func EnvPrefix(env map[string]string, sandbox bool) (string, error) {
	pairs := map[string]string{}
	for k, v := range env {
		pairs[k] = v
	}
	if sandbox {
		// claude refuses bypassPermissions as root; inside a disposable container
		// that refusal is the wrong default
		if _, ok := pairs["IS_SANDBOX"]; !ok {
			pairs["IS_SANDBOX"] = "1"
		}
	}
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if !validEnvName(k) {
			return "", fmt.Errorf("invalid env var name %q", k)
		}
		parts = append(parts, k+"="+shellQuote(pairs[k]))
	}
	if len(parts) == 0 {
		return "", nil
	}
	return strings.Join(parts, " ") + " ", nil
}

func validEnvName(k string) bool {
	if k == "" || (k[0] >= '0' && k[0] <= '9') {
		return false
	}
	for _, r := range k {
		if r != '_' && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// Command renders the full `tmux new-session` invocation for one attempt.
func (l Launcher) Command(s LaunchSpec) (string, error) {
	prefix, err := EnvPrefix(s.Env, s.Sandbox)
	if err != nil {
		return "", err
	}
	if s.Definition != nil && !s.Definition.Builtin {
		return genericTaskCommand(s, prefix, *s.Definition)
	}
	if s.Agent == "" || s.Agent == "claude" {
		return l.claudeCommand(s, prefix), nil
	}

	// Codex receives validated additive -c overrides; Gemini has no equivalent
	// of --settings/--mcp-config. The staged context bundle still reaches both
	// through the prompt prefix.
	rt := RuntimeDir(s.Worktree)
	var parts []string
	switch s.Agent {
	case "codex":
		parts = []string{l.bin(l.CodexBin, "codex")}
		for _, arg := range s.ExtraArgs {
			parts = append(parts, shellQuote(arg))
		}
		parts = append(parts, "exec", "--json")
		if s.Model != "" {
			parts = append(parts, "-m", s.Model)
		}
		flags, ok := codexSandbox[s.PermissionMode]
		if !ok {
			return "", fmt.Errorf("codex has no sandbox for permission mode %q", s.PermissionMode)
		}
		parts = append(parts, flags...)
		if s.ResumeSession != "" {
			parts = append(parts, "resume", shellQuote(s.ResumeSession))
		}
		parts = append(parts, `"$(cat .lectern/prompt.md)"`)
	case "gemini":
		parts = []string{l.bin(l.GeminiBin, "gemini"), "-p", `"$(cat .lectern/prompt.md)"`}
		if s.Model != "" {
			parts = append(parts, "-m", s.Model)
		}
		if s.PermissionMode == "acceptEdits" || s.PermissionMode == "bypassPermissions" {
			parts = append(parts, "--yolo")
		}
	default:
		return "", fmt.Errorf("unknown agent %q", s.Agent)
	}
	// Both read stdin even with the prompt passed as an argument, and a tmux
	// pane's stdin never EOFs — without this redirect the agent waits forever on
	// "Reading additional input from stdin" and the attempt merely looks hung.
	inner := fmt.Sprintf("cd %s && %s%s < /dev/null > %s/events.jsonl 2> %s/stderr.log; echo $? > %s/exit_code",
		s.Worktree, prefix, strings.Join(parts, " "), rt, rt, rt)
	return "tmux new-session -d -s " + s.TmuxSession + " " + shellQuote(inner), nil
}

// genericTaskCommand launches a configured CLI as a bounded background task.
// The prompt is either an argument or stdin, as declared by the definition;
// no provider-specific flags are invented. A tmux pane's stdin never reaches
// EOF, so stdin delivery is implemented by an explicit file pipeline.
func genericTaskCommand(s LaunchSpec, prefix string, d TaskDefinition) (string, error) {
	if strings.TrimSpace(d.Command) == "" {
		return "", fmt.Errorf("agent %q has no command", d.Name)
	}
	mode := d.OutputMode
	if mode == "" {
		mode = "plain"
	}
	if mode != "plain" && mode != "jsonl" && mode != "codex" && mode != "claude" {
		return "", fmt.Errorf("agent %q has unsupported task output mode %q", d.Name, mode)
	}
	if s.PermissionMode == "plan" || s.PermissionMode == "bypassPermissions" {
		args, ok := d.PermissionArgs[s.PermissionMode]
		if !ok || !permissionArgsConfigured(args) {
			return "", fmt.Errorf("agent %q does not support permission mode %q; configure permission_args or use acceptEdits",
				d.Name, s.PermissionMode)
		}
	}
	parts := []string{d.Command}
	for _, arg := range d.Args {
		parts = append(parts, shellQuote(arg))
	}
	if s.Model != "" && d.ModelFlag != "" {
		parts = append(parts, shellQuote(d.ModelFlag), shellQuote(s.Model))
	}
	if args := d.PermissionArgs[s.PermissionMode]; len(args) > 0 {
		for _, arg := range args {
			parts = append(parts, shellQuote(arg))
		}
	}
	if s.ResumeSession != "" && len(d.ResumeArgs) > 0 {
		for _, arg := range d.ResumeArgs {
			parts = append(parts, shellQuote(strings.ReplaceAll(arg, "{id}", s.ResumeSession)))
		}
	}
	invocation := strings.Join(parts, " ")
	promptMode := d.PromptTemplate
	if promptMode == "" && d.PromptArg {
		promptMode = "{prompt}"
	}
	if promptMode == "stdin" {
		invocation = `cat .lectern/prompt.md | ` + prefix + invocation
	} else {
		if promptMode == "" {
			return "", fmt.Errorf("agent %q task prompt_template is required", d.Name)
		}
		if !strings.Contains(promptMode, "{prompt}") && !strings.Contains(promptMode, "{prompt_file}") {
			return "", fmt.Errorf("agent %q task prompt_template must contain {prompt} or {prompt_file}", d.Name)
		}
		args, err := renderPromptTemplate(promptMode)
		if err != nil {
			return "", fmt.Errorf("agent %q: %w", d.Name, err)
		}
		invocation = prefix + invocation + " " + strings.Join(args, " ")
	}
	rt := RuntimeDir(s.Worktree)
	quotedRT := shellQuote(rt)
	inner := fmt.Sprintf("cd %s && %s > %s/events.jsonl 2> %s/stderr.log; echo $? > %s/exit_code",
		shellQuote(s.Worktree), invocation, quotedRT, quotedRT, quotedRT)
	if promptMode != "stdin" {
		inner = fmt.Sprintf("cd %s && %s < /dev/null > %s/events.jsonl 2> %s/stderr.log; echo $? > %s/exit_code",
			shellQuote(s.Worktree), invocation, quotedRT, quotedRT, quotedRT)
	}
	return "tmux new-session -d -s " + shellQuote(s.TmuxSession) + " " + shellQuote(inner), nil
}

func permissionArgsConfigured(args []string) bool {
	if len(args) == 0 {
		return false
	}
	for _, arg := range args {
		if strings.TrimSpace(arg) == "" {
			return false
		}
	}
	return true
}

// renderPromptTemplate treats the template as a list of argument tokens. This
// keeps multiline prompts and literal provider flags safe while still allowing
// forms such as "--message {prompt}" and "--prompt-file {prompt_file}".
func renderPromptTemplate(template string) ([]string, error) {
	var out []string
	tokens, err := promptTemplateTokens(template)
	if err != nil {
		return nil, err
	}
	for _, token := range tokens {
		switch token {
		case "{prompt}":
			out = append(out, `"$(cat .lectern/prompt.md)"`)
		case "{prompt_file}":
			out = append(out, shellQuote(".lectern/prompt.md"))
		default:
			if strings.Contains(token, "{prompt}") || strings.Contains(token, "{prompt_file}") {
				return nil, fmt.Errorf("prompt placeholders must be whole argument tokens")
			}
			out = append(out, shellQuote(strings.Trim(token, `"'`)))
		}
	}
	return out, nil
}

// promptTemplateTokens is a small argument tokenizer, rather than a shell
// parser. It accepts whitespace-separated arguments with single/double quotes
// and backslash escapes, then shell-quotes every literal token when rendering.
// This supports a flag such as --name "two words" without accepting arbitrary
// command substitutions from the configured template.
func promptTemplateTokens(template string) ([]string, error) {
	var tokens []string
	var token strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if token.Len() > 0 {
			tokens = append(tokens, token.String())
			token.Reset()
		}
	}
	for _, r := range template {
		if escaped {
			token.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				token.WriteRune(r)
			}
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			token.WriteRune(r)
		}
	}
	if escaped {
		token.WriteByte('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("prompt_template has an unterminated quote")
	}
	flush()
	return tokens, nil
}

func (l Launcher) claudeCommand(s LaunchSpec, prefix string) string {
	rt := RuntimeDir(s.Worktree)
	settings := s.SettingsPath
	if settings == "" {
		settings = SettingsRel
	}
	parts := []string{l.bin(l.ClaudeBin, "claude"), "-p", `"$(cat .lectern/prompt.md)"`,
		"--output-format", "stream-json", "--verbose",
		"--permission-mode", s.PermissionMode}
	parts = append(parts, "--settings", shellQuote(settings))
	if s.MCPConfig != "" {
		parts = append(parts, "--mcp-config", shellQuote(s.MCPConfig))
		if s.StrictMCP {
			parts = append(parts, "--strict-mcp-config")
		}
	}
	if s.Model != "" {
		parts = append(parts, "--model", s.Model)
	}
	if s.ResumeSession != "" {
		parts = append(parts, "--resume", s.ResumeSession)
	}
	inner := fmt.Sprintf("cd %s && %s%s > %s/events.jsonl 2> %s/stderr.log; echo $? > %s/exit_code",
		s.Worktree, prefix, strings.Join(parts, " "), rt, rt, rt)
	return "tmux new-session -d -s " + s.TmuxSession + " " + shellQuote(inner)
}

func (l Launcher) bin(configured, def string) string {
	if configured != "" {
		return configured
	}
	return def
}
