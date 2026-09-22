package sessions

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/internal/shellq"
)

// Spec describes how to start one interactive coding CLI.
//
// Three agents ship built in, but the set is deliberately open: the terminal
// does not care which binary is in it, and neither should the board. A spec is
// the smallest description that lets lectern launch, resume and prime an
// arbitrary CLI — anything beyond that is the CLI's business.
type Spec struct {
	Name string `json:"name"`
	// Command is the binary (or a full shell command) that starts it.
	Command string `json:"command"`
	// Args are fixed arguments appended on every launch.
	Args []string `json:"args,omitempty"`
	// ModelFlag passes a model, e.g. "--model" or "-m". Empty means this CLI has
	// no model switch and any model set on the session is ignored rather than
	// guessed at.
	ModelFlag string `json:"model_flag,omitempty"`
	// ResumeArgs reopen the CLI's own previous conversation, e.g. ["--continue"].
	// Empty means resume is not offered for this agent.
	ResumeArgs []string `json:"resume_args,omitempty"`
	// ResumeIDArgs select one exact conversation. {id} and {dir} are substituted
	// before shell quoting; {dir} follows the recorded working directory.
	ResumeIDArgs []string `json:"resume_id_args,omitempty"`
	ForkArgs     []string `json:"fork_args,omitempty"`
	// PromptArg says the opening message can be a positional argument. When it
	// cannot, lectern falls back to typing the message once the pane settles.
	PromptArg bool `json:"prompt_arg,omitempty"`
	// Env is agent-wide environment, layered under the project's own. This is
	// the local-model door for a CLI that wants its endpoint in the environment.
	Env map[string]string `json:"env,omitempty"`
	// TrustCommand marks a directory as one the operator already trusts, in
	// whatever file the CLI keeps that answer in. `{dir}` is replaced with the
	// shell-quoted working directory.
	//
	// Every coding CLI asks "do you trust this folder?" the first time it opens
	// one, and answers it in its own config. Launching an agent there IS the
	// answer — lectern was told to start it, in that directory, on purpose —
	// so being asked again in a terminal you then have to go and find is pure
	// friction. It bites hardest on scratch sessions, where the directory is new
	// every single time and the prompt is therefore guaranteed.
	TrustCommand string `json:"trust_command,omitempty"`
	// YoloArgs drop the CLI's approval prompts so the agent just works. Every
	// coding CLI spells this differently and some cannot do it at all; empty
	// means this agent has no such mode and the toggle is not offered for it.
	YoloArgs []string `json:"yolo_args,omitempty"`
	// ModelsCommand asks the CLI what models it has. `{bin}` is replaced with the
	// resolved binary. Model line-ups change faster than lectern ships, and a
	// list of names written down here is wrong the moment a vendor renames one —
	// so the tool is asked rather than remembered. Empty means no catalog, and
	// the UI falls back to whatever you have already run.
	ModelsCommand string `json:"models_command,omitempty"`
	// Task is optional because a configured CLI may be interactive-only. Its
	// command/args are independent from the interactive invocation above.
	Task *TaskSpec `json:"task,omitempty"`
	// Builtin marks the three that ship with lectern, so the UI can show which
	// are yours.
	Builtin bool `json:"builtin,omitempty"`
}

// TaskSpec describes a configured CLI's non-interactive one-shot command.
// PromptTemplate is appended to the command and must contain {prompt} or
// {prompt_file}; stdin is also supported when the value is exactly "stdin".
type TaskSpec struct {
	Command        string              `json:"command,omitempty"`
	Args           []string            `json:"args,omitempty"`
	PromptTemplate string              `json:"prompt_template"`
	OutputMode     string              `json:"output_mode,omitempty"`
	PermissionArgs map[string][]string `json:"permission_args,omitempty"`
	// ResumeArgs continue the CLI's own previous run for a follow-up, with
	// {id} replaced by the session id captured from its output, e.g.
	// ["resume", "{id}"] for a codex wrapper. Empty means a follow-up starts a
	// fresh process in the same worktree.
	ResumeArgs []string `json:"resume_args,omitempty"`
}

// Builtins are the agents lectern knows without being told.
func Builtins() []Spec {
	return []Spec{
		{Name: "claude", Command: "claude", ModelFlag: "--model",
			ForkArgs: []string{"--resume", "{id}", "--fork-session"}, ResumeIDArgs: []string{"--resume", "{id}"}, ResumeArgs: []string{"--continue"}, PromptArg: true, Builtin: true,
			YoloArgs:     []string{"--permission-mode", "bypassPermissions"},
			TrustCommand: claudeTrust},
		{Name: "codex", Command: "codex", ModelFlag: "-m",
			ForkArgs: []string{"fork", "{id}"}, ResumeIDArgs: []string{"resume", "{id}"}, ResumeArgs: []string{"resume", "--last"}, PromptArg: true, Builtin: true,
			ModelsCommand: "{bin} debug models",
			YoloArgs:      []string{"--dangerously-bypass-approvals-and-sandbox"},
			TrustCommand:  codexTrust},
		// gemini's interactive mode takes no opening message on the command
		// line, so its prime is typed in once the pane settles
		{Name: "gemini", Command: "gemini", ModelFlag: "-m", Builtin: true,
			YoloArgs: []string{"--yolo"}},
	}
}

// ParseSpecs decodes the operator's custom agents and merges them over the
// built-ins. Same name overrides, so a built-in whose CLI has drifted can be
// corrected without a new release.
func ParseSpecs(raw string) []Spec {
	out := Builtins()
	var custom []Spec
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &custom); err != nil {
			return out
		}
	}
	for _, c := range custom {
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" || c.Command == "" {
			continue
		}
		// The API returns built-ins with builtin:true. A GET/PUT round trip
		// must leave an untouched built-in implicit; otherwise this entry would
		// become a session-only custom override and lose built-in task support.
		// The exact field comparison prevents the marker itself from granting
		// capabilities to a changed definition.
		if c.Builtin && isExactBuiltin(c) {
			continue
		}
		c.Builtin = false
		replaced := false
		for i := range out {
			if out[i].Name == c.Name {
				out[i], replaced = c, true
				break
			}
		}
		if !replaced {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func isExactBuiltin(candidate Spec) bool {
	for _, builtin := range Builtins() {
		if candidate.Name != builtin.Name {
			continue
		}
		candidate.Builtin = builtin.Builtin
		return reflect.DeepEqual(candidate, builtin)
	}
	return false
}

// NormalizeBuiltinEntries removes exact built-ins returned by GET /api/agents
// before settings are persisted. Changed entries are retained as explicit
// custom overrides; their builtin marker is discarded and never grants
// built-in capabilities.
func NormalizeBuiltinEntries(raw string) (string, error) {
	var entries []map[string]any
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return "", fmt.Errorf("agents must be a list of objects: %w", err)
	}
	canonical := make(map[string]map[string]any)
	for _, builtin := range Builtins() {
		encoded, _ := json.Marshal(builtin)
		var value map[string]any
		_ = json.Unmarshal(encoded, &value)
		delete(value, "builtin")
		canonical[builtin.Name] = value
	}
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			out = append(out, entry)
			continue
		}
		name, _ := entry["name"].(string)
		marked, _ := entry["builtin"].(bool)
		candidate := make(map[string]any, len(entry))
		for key, value := range entry {
			if key != "builtin" {
				candidate[key] = value
			}
		}
		if marked {
			if expected, ok := canonical[name]; ok && reflect.DeepEqual(candidate, expected) {
				continue
			}
		}
		// builtin is UI metadata, and a changed marker must become an explicit
		// custom definition for the normal validation/merge path.
		delete(candidate, "builtin")
		out = append(out, candidate)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode agents: %w", err)
	}
	return string(encoded), nil
}

// ValidateSpecs rejects definitions that could not launch, so a bad one fails at
// config time rather than as a session that dies on start.
func ValidateSpecs(raw string) error {
	var custom []Spec
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), &custom); err != nil {
		return fmt.Errorf("agents must be a list of objects: %w", err)
	}
	seen := map[string]bool{}
	for i, c := range custom {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			return fmt.Errorf("agent %d has no name", i)
		}
		if seen[name] {
			return fmt.Errorf("duplicate agent %q", name)
		}
		seen[name] = true
		if strings.TrimSpace(c.Command) == "" {
			return fmt.Errorf("agent %q has no command", name)
		}
		if c.Task != nil {
			if !TaskOutputModeKnown(c.Task.OutputMode) {
				return fmt.Errorf("agent %q: task.output_mode must be plain, jsonl, codex or claude", name)
			}
			if err := validTaskPromptTemplate(c.Task.PromptTemplate); err != nil {
				return fmt.Errorf("agent %q: %w", name, err)
			}
		}
		if c.Task != nil {
			for mode := range c.Task.PermissionArgs {
				if mode != "acceptEdits" && mode != "plan" && mode != "bypassPermissions" {
					return fmt.Errorf("agent %q: permission_args has unsupported mode %q", name, mode)
				}
			}
			for _, mode := range []string{"plan", "bypassPermissions"} {
				if args, ok := c.Task.PermissionArgs[mode]; ok && !TaskPermissionArgsConfigured(args) {
					return fmt.Errorf("agent %q: permission_args.%s must contain a non-empty flag", name, mode)
				}
			}
		}
		for k := range c.Env {
			if !validEnvName(k) {
				return fmt.Errorf("agent %q: invalid env var name %q", name, k)
			}
		}
	}
	return nil
}

// TaskPermissionArgsConfigured distinguishes a real capability mapping from
// an empty JSON array that would silently claim a mode while emitting no flag.
func TaskPermissionArgsConfigured(args []string) bool {
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

func validTaskPromptTemplate(template string) error {
	if template == "stdin" {
		return nil
	}
	if strings.TrimSpace(template) == "" {
		return fmt.Errorf("task.prompt_template is required")
	}
	if err := validTaskTemplateQuotes(template); err != nil {
		return err
	}
	found := false
	for _, token := range strings.Fields(template) {
		token = strings.Trim(token, `"'`)
		if token == "{prompt}" || token == "{prompt_file}" {
			found = true
			continue
		}
		if strings.Contains(token, "{prompt}") || strings.Contains(token, "{prompt_file}") {
			return fmt.Errorf("task.prompt_template placeholders must be whole argument tokens")
		}
	}
	if !found {
		return fmt.Errorf("task.prompt_template must contain {prompt} or {prompt_file}")
	}
	return nil
}

func validTaskTemplateQuotes(template string) error {
	var quote rune
	escaped := false
	for _, r := range template {
		if escaped {
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
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
		}
	}
	if quote != 0 {
		return fmt.Errorf("task.prompt_template has an unterminated quote")
	}
	return nil
}

// Find returns the spec for a name.
func Find(specs []Spec, name string) (Spec, bool) {
	if name == "" {
		name = "claude"
	}
	for _, s := range specs {
		if s.Name == name {
			return s, true
		}
	}
	return Spec{}, false
}

// LaunchCommand renders the tmux invocation that starts this agent.
// Start is everything that varies between one launch of an agent and the next.
type Start struct {
	SetupToken string
	Workdir    string
	TmuxName   string
	Model      string
	Resume     bool
	ResumeID   string
	ForkID     string
	Prompt     string
	EnvPrefix  string
	// Yolo runs the agent without its approval prompts. On by default for
	// interactive sessions: you are sitting in the terminal watching it, which
	// is the supervision, and being asked to confirm every edit in a session you
	// opened on purpose is just friction. An agent with no YoloArgs ignores it.
	Yolo bool
	// ToolArgs are provider-specific configuration flags supplied by the
	// project. They are quoted here because they may contain MCP secrets.
	ToolArgs []string
}

// LaunchCommand builds the tmux command that starts one interactive session.
func (s Spec) LaunchCommand(o Start) string {
	parts := []string{s.Command}
	parts = append(parts, s.Args...)
	for _, arg := range o.ToolArgs {
		parts = append(parts, shellq.Quote(arg))
	}
	if o.ForkID != "" {
		for _, arg := range s.ForkArgs {
			parts = append(parts, shellq.Quote(strings.NewReplacer("{id}", o.ForkID, "{dir}", o.Workdir).Replace(arg)))
		}
	} else if o.ResumeID != "" {
		for _, arg := range s.ResumeIDArgs {
			parts = append(parts, shellq.Quote(strings.NewReplacer("{id}", o.ResumeID, "{dir}", o.Workdir).Replace(arg)))
		}
	} else if o.Resume && len(s.ResumeArgs) > 0 {
		parts = append(parts, s.ResumeArgs...)
	}
	if o.Yolo && len(s.YoloArgs) > 0 {
		parts = append(parts, s.YoloArgs...)
	}
	if o.Model != "" && s.ModelFlag != "" {
		parts = append(parts, s.ModelFlag, o.Model)
	}
	if o.Prompt != "" && s.PromptArg {
		parts = append(parts, shellq.Quote(o.Prompt))
	}
	inner := fmt.Sprintf("cd %s && %s%s; exec bash",
		shellq.Quote(o.Workdir), o.EnvPrefix, strings.Join(parts, " "))
	setupEnv := ""
	if o.SetupToken != "" {
		setupEnv = " -e " + shellq.Quote("LECTERN_SETUP_TOKEN="+o.SetupToken)
	}
	// Spell out the shell invocation so tmux cannot reinterpret the generated
	// command string differently across versions or target configurations.
	return fmt.Sprintf("tmux new-session -d%s -s %s -- bash -c %s", setupEnv,
		shellq.Quote(o.TmuxName), shellq.Quote(inner))
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

// ModelsProbe is the command that asks this agent for its model catalog, with
// the binary resolved. Empty when the agent cannot be asked.
func (s Spec) ModelsProbe() string {
	if s.ModelsCommand == "" {
		return ""
	}
	return strings.ReplaceAll(s.ModelsCommand, "{bin}", s.Command)
}

// ParseModelCatalog pulls model identifiers out of whatever JSON an agent's
// catalog command prints.
//
// The shape is the agent's, not ours, so this reads defensively: a top-level
// array, or the first array of objects found under a key, and from each entry a
// slug/id/name. Entries a CLI marks hidden are skipped — those are internal
// models its own picker does not offer either.
func ParseModelCatalog(raw string) []string {
	var doc any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &doc); err != nil {
		return nil
	}
	list := findList(doc, 0)
	out := []string{}
	seen := map[string]bool{}
	for _, item := range list {
		var name string
		switch v := item.(type) {
		case string:
			name = v
		case map[string]any:
			if vis, ok := v["visibility"].(string); ok && vis == "hide" {
				continue
			}
			for _, key := range []string{"slug", "id", "name"} {
				if got, ok := v[key].(string); ok && got != "" {
					name = got
					break
				}
			}
		}
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// findList returns the first plausible list of models in a decoded document.
func findList(doc any, depth int) []any {
	if depth > 3 {
		return nil
	}
	switch v := doc.(type) {
	case []any:
		return v
	case map[string]any:
		// a key actually called "models" beats any other array in the document
		if inner, ok := v["models"].([]any); ok {
			return inner
		}
		for _, val := range v {
			if got := findList(val, depth+1); got != nil {
				return got
			}
		}
	}
	return nil
}

// claudeTrust records an accepted workspace-trust dialog in ~/.claude.json.
//
// Written through a temp file and rename so the config is never observed
// half-written: Claude Code itself writes this file continuously (costs,
// durations), and a torn write would take out far more than a trust flag. It
// only ever adds the one key, and only when it is missing.
const claudeTrust = `python3 - {dir} <<'ADKTRUST'
import json, os, sys, tempfile
config = os.environ.get("CLAUDE_CONFIG_DIR")
path = os.path.join(os.path.expanduser(config), ".claude.json") if config else os.path.expanduser("~/.claude.json")
os.makedirs(os.path.dirname(path), mode=0o700, exist_ok=True)
try:
    with open(path) as f:
        doc = json.load(f)
except Exception:
    doc = {}
entry = doc.setdefault("projects", {}).setdefault(sys.argv[1], {})
if entry.get("hasTrustDialogAccepted") is not True:
    entry["hasTrustDialogAccepted"] = True
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path) or ".")
    with os.fdopen(fd, "w") as f:
        json.dump(doc, f, indent=2)
    os.replace(tmp, path)
ADKTRUST`

// codexTrust records a trusted project in ~/.codex/config.toml.
//
// Appended rather than rewritten: the file is the operator's, holding their MCP
// servers and model settings, and a TOML round-trip through a parser lectern
// does not own is a good way to lose a comment or reorder someone's config.
const codexTrust = `python3 - {dir} <<'ADKTRUST'
import fcntl, json, os, sys
from pathlib import Path
root = Path(os.path.expanduser(os.environ.get("CODEX_HOME") or "~/.codex"))
root.mkdir(mode=0o700, parents=True, exist_ok=True)
fd = os.open(root / "config.toml", os.O_RDWR | os.O_CREAT, 0o600)
with os.fdopen(fd, "r+", encoding="utf-8") as config:
    fcntl.flock(config, fcntl.LOCK_EX)
    text = config.read()
    if text.strip():
        # Reading only: existing settings and comments remain byte-identical.
        # Without a TOML parser, leave an existing config to the CLI itself.
        try:
            import tomllib
        except ImportError:
            raise SystemExit(1)
        doc = tomllib.loads(text)
        if sys.argv[1] in doc.get("projects", {}):
            raise SystemExit(0)
    config.seek(0, 2)
    config.write("\n[projects." + json.dumps(sys.argv[1], ensure_ascii=False) + "]\ntrust_level = \"trusted\"\n")
ADKTRUST`

// TrustProbe is the command that marks a directory trusted for this agent, or
// empty when the agent has no such notion.
func (s Spec) TrustProbe(dir string) string {
	if s.TrustCommand == "" || dir == "" {
		return ""
	}
	cmd := strings.ReplaceAll(currentTrustCommand(s.TrustCommand), "{dir}", shellq.Quote(dir))
	return strings.ReplaceAll(cmd, "{dir_raw}", dir)
}

// TaskOutputModeKnown says whether a custom agent's task output can be
// parsed. "codex" and "claude" are for a custom agent that wraps one of those
// CLIs (a different provider, a pinned model, extra flags) and so emits its
// exact stream; the timeline then reads as it does for the built-in agent
// instead of as generic JSON lines.
func TaskOutputModeKnown(mode string) bool {
	switch mode {
	case "", "plain", "jsonl", "codex", "claude":
		return true
	}
	return false
}
