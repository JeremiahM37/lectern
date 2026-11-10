package backup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// remoteLimit caps one target's transcripts; they travel through the
// executor's stdout, which is held in memory.
const remoteLimit = 192 << 20

// remoteScript runs on an SSH target: it tars the Claude and Codex
// transcripts of the workspaces given as JSON in argv[1] and prints the
// tar.gz as base64. Paths are stored absolute, without the leading slash.
const remoteScript = `
import base64, glob, io, json, os, re, sys, tarfile
ws = json.loads(sys.argv[1]); limit = int(sys.argv[2])
home = os.path.expanduser('~')
files = set(); codex = set()
for agent, d in ws:
    for p in {d, os.path.realpath(d)}:
        if agent == 'claude':
            for root, _, names in os.walk(os.path.join(home, '.claude', 'projects', re.sub(r'[^a-zA-Z0-9]', '-', p))):
                files.update(os.path.join(root, n) for n in names)
        elif agent == 'codex':
            codex.add(p)
if codex:
    for f in glob.iglob(os.path.join(home, '.codex', 'sessions', '**', '*.jsonl'), recursive=True):
        try:
            with open(f, 'rb') as fh: head = fh.readline(65536)
            m = re.search(rb'"cwd":"((?:[^"\\]|\\.)*)"', head)
            if m and json.loads(b'"' + m.group(1) + b'"') in codex: files.add(f)
        except (OSError, ValueError): pass
total = 0; buf = io.BytesIO()
with tarfile.open(fileobj=buf, mode='w:gz') as tar:
    for f in sorted(files):
        try:
            if not os.path.isfile(f): continue
            total += os.path.getsize(f)
            if total > limit: sys.stderr.write('transcripts exceed %d bytes; the rest were left out\n' % limit); break
            tar.add(f, arcname=f.lstrip('/'), recursive=False)
        except OSError: pass
sys.stdout.write(base64.b64encode(buf.getvalue()).decode())
`

// FromLectern describes a Lectern server's data: its database, media, home
// and the workspaces of every session it has run, local and remote.
func FromLectern(db *store.DB, dbPath, mediaDir string, reg *executor.Registry) (Source, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Source{}, err
	}
	host, _ := os.Hostname()
	src := Source{DBPath: dbPath, MediaDir: mediaDir, Home: home, Host: host, RemoteWorkspaces: map[string][]Workspace{}}
	rows, err := db.Query(`SELECT DISTINCT s.agent, s.workdir, t.name, t.kind FROM sessions s JOIN targets t ON t.id = s.target_id`)
	if err != nil {
		return Source{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var w Workspace
		var kind string
		if err := rows.Scan(&w.Agent, &w.Dir, &w.Target, &kind); err != nil {
			return Source{}, err
		}
		switch kind {
		case "local":
			src.Workspaces = append(src.Workspaces, w)
		case "ssh", "pct":
			if w.Agent == "claude" || w.Agent == "codex" {
				src.RemoteWorkspaces[w.Target] = append(src.RemoteWorkspaces[w.Target], w)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Source{}, err
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "LECTERN_") || strings.HasPrefix(kv, "GRIMOIRE_") {
			src.Env = append(src.Env, kv)
		}
	}
	sort.Strings(src.Env)
	if reg != nil && len(src.RemoteWorkspaces) > 0 {
		src.Remote = func(ctx context.Context, target string, ws []Workspace) ([]byte, string, error) {
			return remoteTranscripts(ctx, db, reg, target, ws)
		}
	}
	return src, nil
}

func remoteTranscripts(ctx context.Context, db *store.DB, reg *executor.Registry, target string, ws []Workspace) ([]byte, string, error) {
	targets, err := db.Targets()
	if err != nil {
		return nil, "", err
	}
	var t *store.Target
	for _, c := range targets {
		if c.Name == target {
			t = c
		}
	}
	if t == nil {
		return nil, "", fmt.Errorf("target is gone")
	}
	ex, err := reg.For(t)
	if err != nil {
		return nil, "", err
	}
	pairs := make([][2]string, 0, len(ws))
	for _, w := range ws {
		pairs = append(pairs, [2]string{w.Agent, w.Dir})
	}
	arg, _ := json.Marshal(pairs)
	cmd := "python3 -c " + shellq.Quote(remoteScript) + " " + shellq.Quote(string(arg)) + " " + fmt.Sprint(remoteLimit)
	res, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 600})
	if err != nil {
		return nil, "", err
	}
	if !res.OK() {
		return nil, "", fmt.Errorf("exit %d: %s", res.RC, strings.TrimSpace(res.Stderr))
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(res.Stdout))
	return data, strings.TrimSpace(res.Stderr), err
}
