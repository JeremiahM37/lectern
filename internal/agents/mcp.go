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
