package agents

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// MCPPayload normalizes the project form into the standard Claude document.
func MCPPayload(mcp map[string]any) ([]byte, error) {
	if inner, ok := mcp["mcpServers"]; ok {
		return json.Marshal(map[string]any{"mcpServers": inner})
	}
	return json.Marshal(map[string]any{"mcpServers": mcp})
}

// CodexMCPArgs returns additive -c overrides. It deliberately does not alter
// CODEX_HOME: that directory owns auth, history, instructions and plugins.
func CodexMCPArgs(mcp map[string]any) ([]string, error) {
	if inner, ok := mcp["mcpServers"].(map[string]any); ok {
		mcp = inner
	}
	names := make([]string, 0, len(mcp))
	for name := range mcp {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []string
	for _, name := range names {
		if name == "" || strings.IndexFunc(name, func(r rune) bool {
			return !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') &&
				!(r >= '0' && r <= '9') && r != '_' && r != '-'
		}) >= 0 {
			return nil, fmt.Errorf("MCP server %q cannot be represented by Codex -c; use only letters, digits, '_' or '-'", name)
		}
		cfg, ok := mcp[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("MCP server %q must be an object", name)
		}
		keys := make([]string, 0, len(cfg))
		for key := range cfg {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if key == "type" || key == "transport" {
				continue
			}
			value, err := tomlValue(cfg[key])
			if err != nil {
				return nil, fmt.Errorf("MCP server %q field %q: %w", name, key, err)
			}
			field := key
			if key == "headers" {
				field = "http_headers"
			}
			path := `mcp_servers.` + tomlKey(name) + `.` + tomlKey(field)
			out = append(out, "-c", path+"="+value)
		}
	}
	return out, nil
}

// interactiveMCPInstallScript creates and publishes one private runtime file on
// the target. It deliberately uses directory descriptors for every operation
// below the target user's state directory: the worktree can be modified by
// setup hooks or another session, and generated credentials must never become
// Git files. A hard link is the publication primitive because it is atomic and
// refuses an existing destination, including a symlink, without replacing
// foreign content.
const interactiveMCPInstallScript = `
import base64, os, stat, sys

rel, encoded = sys.argv[2:]
parts = rel.split('/')
if len(parts) != 4 or parts[0] != 'lectern' or parts[1] != 'mcp' or parts[3] != 'mcp.json' or not parts[2] or any(p in ('', '.', '..') for p in parts):
    raise RuntimeError('invalid interactive MCP path')

state = os.environ.get('XDG_STATE_HOME', '')
if not state:
    home = os.environ.get('HOME', '')
    if not home:
        raise RuntimeError('target HOME is unavailable')
    state = home + '/.local/state'
if not state.startswith('/'):
    raise RuntimeError('target XDG_STATE_HOME must be absolute')

O_DIR = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
def open_or_make_path(path):
    fd = os.open('/', O_DIR)
    try:
        for part in path.split('/')[1:]:
            if not part or part in ('.', '..'):
                raise RuntimeError('invalid state path')
            try:
                os.mkdir(part, 0o700, dir_fd=fd)
            except FileExistsError:
                pass
            nxt = os.open(part, O_DIR, dir_fd=fd)
            os.close(fd)
            fd = nxt
        return fd
    except:
        os.close(fd)
        raise

def open_or_make_dir(parent, name, mode):
    try:
        os.mkdir(name, mode, dir_fd=parent)
    except FileExistsError:
        pass
    return os.open(name, O_DIR, dir_fd=parent)

root = open_or_make_path(state)
lec = None
interactive = None
leaf = None
tmp = None
try:
    lec = open_or_make_dir(root, 'lectern', 0o700)
    interactive = open_or_make_dir(lec, 'mcp', 0o700)
    try:
        os.mkdir(parts[2], 0o700, dir_fd=interactive)
    except FileExistsError:
        raise RuntimeError('interactive MCP runtime already exists')
    # Open the exclusive leaf immediately and compare its inode with the one
    # just created. If a concurrent writer replaced it before this point, fail;
    # after this descriptor is held, later pathname changes cannot redirect us.
    expected = os.stat(parts[2], dir_fd=interactive, follow_symlinks=False)
    leaf = os.open(parts[2], O_DIR, dir_fd=interactive)
    actual = os.fstat(leaf)
    if (expected.st_dev, expected.st_ino) != (actual.st_dev, actual.st_ino) or not stat.S_ISDIR(actual.st_mode):
        raise RuntimeError('interactive MCP runtime was replaced')
    tmp = os.open('.mcp.tmp', os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=leaf)
    payload = base64.b64decode(encoded, validate=True)
    view = memoryview(payload)
    while view:
        n = os.write(tmp, view)
        view = view[n:]
    os.fsync(tmp)
    os.close(tmp)
    tmp = None
    # Do not follow a source symlink, and do not replace an existing destination.
    os.link('.mcp.tmp', 'mcp.json', src_dir_fd=leaf, dst_dir_fd=leaf, follow_symlinks=False)
    os.unlink('.mcp.tmp', dir_fd=leaf)
    print(state + '/' + '/'.join(parts), flush=True)
finally:
    for fd in (tmp, leaf, interactive, lec, root):
        if fd is not None:
            try: os.close(fd)
            except OSError: pass
`

// MCPInstallCommand performs the complete private runtime install in one
// target-side operation. The payload is base64-encoded so neither JSON bytes
// nor path values become shell syntax. It prints the resulting absolute path;
// the target's normal HOME/XDG state directory supplies the root.
func MCPInstallCommand(rel string, payload []byte) string {
	encoded := base64.StdEncoding.EncodeToString(payload)
	return "python3 -c " + shellQuote(interactiveMCPInstallScript) + " -- " + shellQuote(rel) + " " + shellQuote(encoded)
}

// PrivateMCPPath validates the helper's absolute-path result before it becomes
// part of a launch command.
func PrivateMCPPath(output string) (string, error) {
	path := strings.TrimSpace(output)
	if path == "" || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\r\n") {
		return "", fmt.Errorf("target returned an invalid private MCP path")
	}
	return path, nil
}

// MCPStateEnvPrefix forwards only the state-directory selectors. Agent API
// credentials and model endpoints do not belong in the helper's environment.
func MCPStateEnvPrefix(env map[string]string) (string, error) {
	stateEnv := map[string]string{}
	for _, key := range []string{"HOME", "XDG_STATE_HOME"} {
		if value, ok := env[key]; ok {
			stateEnv[key] = value
		}
	}
	return EnvPrefix(stateEnv, false)
}

func tomlKey(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' && r != '-'
	}) < 0 {
		return s
	}
	return tomlString(s)
}

func tomlValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return tomlString(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			p, err := tomlValue(item)
			if err != nil {
				return "", err
			}
			parts[i] = p
		}
		return "[" + strings.Join(parts, ",") + "]", nil
	case []string:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = tomlString(item)
		}
		return "[" + strings.Join(parts, ",") + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for key := range x {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, key := range keys {
			p, err := tomlValue(x[key])
			if err != nil {
				return "", err
			}
			parts[i] = tomlKey(key) + " = " + p
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	case map[string]string:
		keys := make([]string, 0, len(x))
		for key := range x {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, key := range keys {
			parts[i] = tomlKey(key) + " = " + tomlString(x[key])
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	default:
		return "", fmt.Errorf("unsupported value type %T", v)
	}
}

// tomlString emits a TOML basic string. strconv.Quote is a Go literal encoder
// and emits escapes such as \a, \v, and \xNN that TOML does not accept.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// mcpServerMap returns the project declaration's server map, accepting both
// a bare mapping and a full {"mcpServers": {...}} document.
func mcpServerMap(mcp map[string]any) (map[string]map[string]any, []string, error) {
	if inner, ok := mcp["mcpServers"].(map[string]any); ok {
		mcp = inner
	}
	servers := map[string]map[string]any{}
	names := make([]string, 0, len(mcp))
	for name, raw := range mcp {
		cfg, ok := raw.(map[string]any)
		if !ok || name == "" {
			return nil, nil, fmt.Errorf("MCP server %q must be a named object", name)
		}
		servers[name] = cfg
		names = append(names, name)
	}
	sort.Strings(names)
	return servers, names, nil
}

// mcpTransport classifies one Claude-format server: "stdio" (command),
// "http" or "sse" (url). Fields other than the ones every translation below
// can carry are rejected rather than silently dropped.
func mcpTransport(name string, cfg map[string]any) (string, error) {
	for key := range cfg {
		switch key {
		case "type", "transport", "command", "args", "env", "url", "headers":
		default:
			return "", fmt.Errorf("MCP server %q field %q has no translation for this agent", name, key)
		}
	}
	kind := str(cfg["type"])
	if kind == "" {
		kind = str(cfg["transport"])
	}
	switch {
	case str(cfg["command"]) != "" && (kind == "" || kind == "stdio"):
		return "stdio", nil
	case str(cfg["url"]) != "" && (kind == "http" || kind == "streamable-http" || kind == ""):
		return "http", nil
	case str(cfg["url"]) != "" && kind == "sse":
		return "sse", nil
	}
	return "", fmt.Errorf("MCP server %q needs a command (stdio) or a url with type http or sse", name)
}

func mcpStrings(name, field string, v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("MCP server %q field %q must be a list of strings", name, field)
	}
	out := make([]string, len(items))
	for i, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("MCP server %q field %q must be a list of strings", name, field)
		}
		out[i] = s
	}
	return out, nil
}

func mcpStringMap(name, field string, v any) (map[string]string, error) {
	if v == nil {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("MCP server %q field %q must be an object of strings", name, field)
	}
	out := make(map[string]string, len(m))
	for k, item := range m {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("MCP server %q field %q must be an object of strings", name, field)
		}
		out[k] = s
	}
	return out, nil
}

// MCPAdapter says how a project's MCP declaration reaches an interactive
// agent other than Claude and Codex. Each writes a private runtime file (see
// MCPInstallCommand) that the CLI reads in addition to the user's own config,
// so strict_mcp cannot be expressed and is rejected.
type MCPAdapter struct {
	// Payload translates the Claude-format declaration into the file the
	// CLI reads.
	Payload func(mcp map[string]any) ([]byte, error)
	// EnvVar, when set, is the environment variable that names the file.
	EnvVar string
	// Args, when set, are launch arguments naming the file ({path} replaced).
	Args []string
}

// MCPAdapterFor returns the translation for agent, by Lectern agent name.
// Each was checked against the CLI itself (docs/context-parity.md):
//   - opencode: OPENCODE_CONFIG names an extra config file merged between
//     the global and project configs; servers live under "mcp".
//   - qwen: QWEN_CODE_SYSTEM_DEFAULTS_PATH names the lowest-precedence
//     settings file; mcpServers is shallow-merged with the user's own.
//   - copilot: --additional-mcp-config @file augments ~/.copilot/mcp-config.json
//     for the session and reads the Claude document as is.
//
// Gemini CLI has no equivalent: its system settings files are ignored unless
// their directory is owned by root, so a per-session file is not possible.
func MCPAdapterFor(agent string) (MCPAdapter, bool) {
	switch agent {
	case "opencode":
		return MCPAdapter{Payload: OpenCodeMCPPayload, EnvVar: "OPENCODE_CONFIG"}, true
	case "qwen":
		return MCPAdapter{Payload: QwenMCPPayload, EnvVar: "QWEN_CODE_SYSTEM_DEFAULTS_PATH"}, true
	case "copilot":
		return MCPAdapter{Payload: CopilotMCPPayload, Args: []string{"--additional-mcp-config", "@{path}"}}, true
	}
	return MCPAdapter{}, false
}

// OpenCodeMCPPayload writes an OpenCode config containing only "mcp".
func OpenCodeMCPPayload(mcp map[string]any) ([]byte, error) {
	servers, names, err := mcpServerMap(mcp)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, name := range names {
		cfg := servers[name]
		kind, err := mcpTransport(name, cfg)
		if err != nil {
			return nil, err
		}
		entry := map[string]any{"enabled": true}
		if kind == "stdio" {
			args, err := mcpStrings(name, "args", cfg["args"])
			if err != nil {
				return nil, err
			}
			env, err := mcpStringMap(name, "env", cfg["env"])
			if err != nil {
				return nil, err
			}
			entry["type"] = "local"
			entry["command"] = append([]string{str(cfg["command"])}, args...)
			if len(env) > 0 {
				entry["environment"] = env
			}
		} else {
			headers, err := mcpStringMap(name, "headers", cfg["headers"])
			if err != nil {
				return nil, err
			}
			entry["type"] = "remote"
			entry["url"] = str(cfg["url"])
			if len(headers) > 0 {
				entry["headers"] = headers
			}
		}
		out[name] = entry
	}
	return json.Marshal(map[string]any{"$schema": "https://opencode.ai/config.json", "mcp": out})
}

// QwenMCPPayload writes a Qwen Code settings file containing only
// mcpServers. Qwen reads streamable HTTP from httpUrl and SSE from url.
func QwenMCPPayload(mcp map[string]any) ([]byte, error) {
	servers, names, err := mcpServerMap(mcp)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, name := range names {
		cfg := servers[name]
		kind, err := mcpTransport(name, cfg)
		if err != nil {
			return nil, err
		}
		entry := map[string]any{}
		switch kind {
		case "stdio":
			args, err := mcpStrings(name, "args", cfg["args"])
			if err != nil {
				return nil, err
			}
			env, err := mcpStringMap(name, "env", cfg["env"])
			if err != nil {
				return nil, err
			}
			entry["command"] = str(cfg["command"])
			if len(args) > 0 {
				entry["args"] = args
			}
			if len(env) > 0 {
				entry["env"] = env
			}
		case "http":
			entry["httpUrl"] = str(cfg["url"])
		case "sse":
			entry["url"] = str(cfg["url"])
		}
		if kind != "stdio" {
			headers, err := mcpStringMap(name, "headers", cfg["headers"])
			if err != nil {
				return nil, err
			}
			if len(headers) > 0 {
				entry["headers"] = headers
			}
		}
		out[name] = entry
	}
	return json.Marshal(map[string]any{"mcpServers": out})
}

// CopilotMCPPayload validates the declaration and writes the Claude-format
// document, which Copilot CLI's --additional-mcp-config reads directly.
func CopilotMCPPayload(mcp map[string]any) ([]byte, error) {
	servers, names, err := mcpServerMap(mcp)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if _, err := mcpTransport(name, servers[name]); err != nil {
			return nil, err
		}
	}
	return MCPPayload(mcp)
}

// ACPMCPServers translates the declaration into ACP session/new mcpServers
// entries (agentclientprotocol.com, "Session Setup"): stdio servers carry no
// type and list env as name/value pairs; http and sse servers carry type and
// name/value headers. needHTTP/needSSE tell the caller which optional
// transports the agent must advertise in mcpCapabilities.
func ACPMCPServers(mcp map[string]any) (out []any, needHTTP, needSSE bool, err error) {
	servers, names, err := mcpServerMap(mcp)
	if err != nil {
		return nil, false, false, err
	}
	pairs := func(m map[string]string) []any {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		list := make([]any, 0, len(keys))
		for _, k := range keys {
			list = append(list, map[string]any{"name": k, "value": m[k]})
		}
		return list
	}
	out = []any{}
	for _, name := range names {
		cfg := servers[name]
		kind, err := mcpTransport(name, cfg)
		if err != nil {
			return nil, false, false, err
		}
		if kind == "stdio" {
			args, err := mcpStrings(name, "args", cfg["args"])
			if err != nil {
				return nil, false, false, err
			}
			env, err := mcpStringMap(name, "env", cfg["env"])
			if err != nil {
				return nil, false, false, err
			}
			if args == nil {
				args = []string{}
			}
			out = append(out, map[string]any{"name": name, "command": str(cfg["command"]), "args": args, "env": pairs(env)})
			continue
		}
		headers, err := mcpStringMap(name, "headers", cfg["headers"])
		if err != nil {
			return nil, false, false, err
		}
		needHTTP = needHTTP || kind == "http"
		needSSE = needSSE || kind == "sse"
		out = append(out, map[string]any{"type": kind, "name": name, "url": str(cfg["url"]), "headers": pairs(headers)})
	}
	return out, needHTTP, needSSE, nil
}
