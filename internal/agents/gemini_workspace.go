package agents

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/helpers"
)

// Interactive Gemini CLI has no per-session MCP file: its settings-file
// overrides are ignored unless their directory is owned by root. The one
// place it reads servers from a workspace is <workspace>/.gemini/settings.json,
// so Lectern writes there, and only in a workspace Lectern itself created (an
// interactive worktree or a scratch directory). The helper below merges into
// an existing file, never replaces a server the file already names, keeps a
// file it created out of `git status` through the repository's info/exclude
// (shared by linked worktrees, so each workspace adds and removes its own
// marked line), and records what it added in a private ledger so cleanup
// removes exactly that.

// GeminiMCPServers translates the project declaration into Gemini CLI's
// mcpServers entries: stdio keeps command/args/env; http and sse use url plus
// type, the form Gemini documents in place of the deprecated httpUrl.
func GeminiMCPServers(mcp map[string]any) (map[string]any, error) {
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
		if kind == "stdio" {
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
		} else {
			headers, err := mcpStringMap(name, "headers", cfg["headers"])
			if err != nil {
				return nil, err
			}
			entry["url"] = str(cfg["url"])
			entry["type"] = kind
			if len(headers) > 0 {
				entry["headers"] = headers
			}
		}
		out[name] = entry
	}
	return out, nil
}

// GeminiWorkspaceMCPResult is the helper's reply.
type GeminiWorkspaceMCPResult struct {
	// Skipped names why nothing was written: "not-owned" (not a Lectern
	// workspace), "tracked" (the repository commits .gemini/settings.json).
	Skipped string   `json:"skipped,omitempty"`
	Path    string   `json:"path,omitempty"`
	Added   []string `json:"added,omitempty"`
	// Kept lists declared servers the file already defined differently; the
	// workspace's own definition wins.
	Kept    []string `json:"kept,omitempty"`
	Removed []string `json:"removed,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// GeminiWorkspaceMCPCommand builds the target-side install (servers != nil)
// or remove (servers == nil) command for one session. owned is true when
// Lectern knows it created workdir (an interactive worktree); otherwise the
// helper accepts workdir only inside the target's scratch root.
func GeminiWorkspaceMCPCommand(workdir string, sessionID int64, owned bool, servers map[string]any) string {
	in := map[string]any{"op": "remove", "workdir": workdir, "session": sessionID}
	if servers != nil {
		in = map[string]any{"op": "install", "workdir": workdir, "session": sessionID, "owned": owned, "servers": servers}
	}
	raw, _ := json.Marshal(in)
	return "python3 -c " + shellQuote(geminiWorkspaceScript) + " " + shellQuote(string(raw))
}

// GeminiWorkspaceMCPCommandFor is GeminiWorkspaceMCPCommand for the target ex
// drives: the lectern helper when that target has the binary.
func GeminiWorkspaceMCPCommandFor(ex executor.Executor, workdir string, sessionID int64, owned bool, servers map[string]any) string {
	fallback := GeminiWorkspaceMCPCommand(workdir, sessionID, owned, servers)
	in := map[string]any{"op": "remove", "workdir": workdir, "session": sessionID}
	if servers != nil {
		in = map[string]any{"op": "install", "workdir": workdir, "session": sessionID, "owned": owned, "servers": servers}
	}
	raw, _ := json.Marshal(in)
	return helpers.Command(ex, "gemini-workspace", []string{string(raw)}, fallback)
}

// ParseGeminiWorkspaceMCP decodes the helper's reply.
func ParseGeminiWorkspaceMCP(stdout string) (*GeminiWorkspaceMCPResult, error) {
	var out GeminiWorkspaceMCPResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &out); err != nil {
		return nil, fmt.Errorf("invalid Gemini workspace MCP reply: %w", err)
	}
	if out.Error != "" {
		return &out, fmt.Errorf("gemini workspace MCP: %s", out.Error)
	}
	return &out, nil
}

// geminiWorkspaceScript: `lectern-gemini-workspace-mcp`. The ledger lives in
// the target user's private state directory, keyed by the workspace's real
// path, and lists the sessions using it, so a shared workspace keeps the
// servers until its last Lectern session ends. A server is removed only while
// its value is still exactly what Lectern wrote.
const geminiWorkspaceScript = `# lectern-gemini-workspace-mcp
import hashlib, json, os, subprocess, sys, tempfile
a = json.loads(sys.argv[1])
MARKER = '# lectern-gemini-mcp'
PATTERN = '/.gemini/settings.json'
def reply(**kw):
    print(json.dumps(kw, separators=(',', ':'))); sys.exit(0)
def git(wd, *args):
    return subprocess.run(['git', '-c', 'safe.directory=' + wd, '-C', wd] + list(args), capture_output=True, text=True)
def scratch_root():
    root = os.environ.get('LECTERN_SCRATCH_ROOT') or os.environ.get('AGENTDECK_SCRATCH_ROOT') or ''
    if root:
        return root
    home = os.path.expanduser('~')
    if os.path.isdir(os.path.join(home, 'lectern-scratch')) or not os.path.isdir(os.path.join(home, 'agentdeck-scratch')):
        return os.path.join(home, 'lectern-scratch')
    return os.path.join(home, 'agentdeck-scratch')
def under(path, root):
    root = os.path.realpath(root)
    return path != root and os.path.commonpath([path, root]) == root
def state_dir():
    base = os.environ.get('XDG_STATE_HOME') or os.path.join(os.path.expanduser('~'), '.local', 'state')
    d = os.path.join(base, 'lectern', 'gemini-mcp')
    os.makedirs(d, 0o700, exist_ok=True)
    return d
def write_json(path, obj, mode):
    fd, tmp = tempfile.mkstemp(prefix='.lectern-', dir=os.path.dirname(path))
    with os.fdopen(fd, 'w') as f:
        json.dump(obj, f, indent=2); f.write('\n')
    os.chmod(tmp, mode)
    os.replace(tmp, path)
def exclude_path(wd):
    p = git(wd, 'rev-parse', '--git-path', 'info/exclude').stdout.strip()
    if not p:
        return ''
    return p if os.path.isabs(p) else os.path.join(wd, p)
def edit_exclude(wd, remove):
    p = exclude_path(wd)
    if not p:
        return False
    os.makedirs(os.path.dirname(p), exist_ok=True)
    try: old = open(p, encoding='utf8').read()
    except OSError: old = ''
    # Linked worktrees share the repository's info/exclude, so each workspace
    # gets its own marker and removes only its own pair.
    pair = MARKER + ':' + hashlib.sha256(wd.encode()).hexdigest()[:12] + '\n' + PATTERN + '\n'
    if remove:
        new = old.replace(pair, '')
    elif pair in old:
        return True
    else:
        new = old + ('' if not old or old.endswith('\n') else '\n') + pair
    if new != old:
        with open(p, 'w', encoding='utf8') as f: f.write(new)
    return True
try:
    wd = os.path.realpath(a['workdir'])
    if not os.path.isdir(wd):
        reply(error='workspace does not exist')
    ledger_path = os.path.join(state_dir(), hashlib.sha256(wd.encode()).hexdigest()[:24] + '.json')
    try: ledger = json.load(open(ledger_path))
    except (OSError, ValueError): ledger = None
    gdir = os.path.join(wd, '.gemini')
    path = os.path.join(gdir, 'settings.json')
    for p in (gdir, path):
        if os.path.islink(p):
            reply(error=p + ' is a symlink; not editing it')
    def file_mode():
        try: return os.stat(path).st_mode & 0o777
        except OSError: return 0o600
    def load_settings():
        if not os.path.exists(path):
            return None
        try:
            data = json.load(open(path, encoding='utf8'))
        except ValueError:
            reply(error='.gemini/settings.json is not plain JSON (comments?); not editing it')
        if not isinstance(data, dict):
            reply(error='.gemini/settings.json is not a JSON object; not editing it')
        return data
    def strip(settings, entries):
        removed = []
        servers = settings.get('mcpServers')
        if isinstance(servers, dict):
            for name, entry in entries.items():
                if name in servers and servers[name] == entry:
                    del servers[name]; removed.append(name)
            if not servers:
                del settings['mcpServers']
        return removed
    def finish_remove(led, settings):
        removed = strip(settings, led.get('entries', {})) if settings is not None else []
        if settings is not None:
            if led.get('created_file') and not settings:
                os.unlink(path)
                if led.get('created_dir'):
                    try: os.rmdir(gdir)
                    except OSError: pass
            else:
                write_json(path, settings, file_mode())
        if led.get('excluded'):
            edit_exclude(wd, True)
        os.unlink(ledger_path)
        return removed
    if a['op'] == 'remove':
        if ledger is None:
            reply(removed=[])
        sessions = [s for s in ledger.get('sessions', []) if s != a['session']]
        if sessions:
            ledger['sessions'] = sessions
            write_json(ledger_path, ledger, 0o600)
            reply(path=path, removed=[])
        reply(path=path, removed=finish_remove(ledger, load_settings()))
    # install
    if not a.get('owned') and not under(wd, scratch_root()):
        reply(skipped='not-owned')
    if git(wd, 'ls-files', '--error-unmatch', '--', '.gemini/settings.json').returncode == 0:
        reply(skipped='tracked')
    if ledger is not None and not ledger.get('sessions'):
        finish_remove(ledger, load_settings())
        ledger = None
    settings = load_settings()
    if ledger is None:
        ledger = {'workdir': wd, 'sessions': [], 'entries': {},
                  'created_file': settings is None, 'created_dir': not os.path.isdir(gdir), 'excluded': False}
    if settings is None:
        settings = {}
    servers = settings.setdefault('mcpServers', {})
    if not isinstance(servers, dict):
        reply(error='mcpServers in .gemini/settings.json is not an object; not editing it')
    added, kept = [], []
    for name, entry in sorted(a['servers'].items()):
        if name in servers and servers[name] != ledger['entries'].get(name):
            kept.append(name); continue
        servers[name] = entry; ledger['entries'][name] = entry; added.append(name)
    os.makedirs(gdir, 0o700, exist_ok=True)
    write_json(path, settings, file_mode())
    if ledger['created_file'] and not ledger['excluded']:
        ledger['excluded'] = edit_exclude(wd, False)
    if a['session'] not in ledger['sessions']:
        ledger['sessions'].append(a['session'])
    write_json(ledger_path, ledger, 0o600)
    reply(path=path, added=added, kept=kept)
except SystemExit:
    raise
except Exception as e:
    reply(error=str(e))
`
