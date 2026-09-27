package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/pluginpkg"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newManager(t *testing.T) *Manager {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "lectern.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db, filepath.Join(t.TempDir(), "lectern-plugins"), "2.4.1", quietLog())
}

const notesManifest = `id: acme.notes
name: Notes
version: %s
capabilities:
  host_exec: true
  notify: true
  mcp_tools: true
  target_exec: true
  secrets: [NOTES_TOKEN]
contributes:
  skills:
    - {id: review, path: skills/review}
  mcp_servers:
    notes: {command: python3, args: ["${LECTERN_PLUGIN_ROOT}/mcp.py"], env: {TOKEN: "${secret:NOTES_TOKEN}"}}
    docs: {url: "https://mcp.example.com"}
  hooks:
    - {event: task.finished, run: host, command: [python3, hooks/h.py]}
  quick_commands:
    - {id: go, label: Go, text: go}
  themes:
    - {id: forest, name: Forest, accent: "#2f855a"}
  palette_commands:
    - {id: docs, title: Docs, href: "#settings/plugins"}
`

const hookScript = `import json, os, sys
ev = json.load(sys.stdin)
print(json.dumps({"ok": True, "message": "saw %s for task %s token=%s" % (ev["event"], ev["data"]["id"], os.environ.get("NOTES_TOKEN", "")), "notify": "done"}))
`

func writePlugin(t *testing.T, dir, version string, extra map[string]string) {
	t.Helper()
	files := map[string]string{
		"lectern-plugin.yaml":    fmt.Sprintf(notesManifest, version),
		"skills/review/SKILL.md": "---\nname: review\n---\n",
		"hooks/h.py":             hookScript,
		"mcp.py":                 "print('mcp')\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	for p, body := range files {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func installPath(t *testing.T, m *Manager, dir string) *Plugin {
	t.Helper()
	pv, err := m.Preview(context.Background(), Source{Kind: "path", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Consent(pv.Hash, pv.Accept, "owner@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInstallFromPathNeedsTheExactCapabilitiesThePreviewShowed(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()
	writePlugin(t, dir, "1.0.0", nil)
	pv, err := m.Preview(context.Background(), Source{Kind: "path", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("acme.notes"); ok {
		t.Fatal("a preview installed the plugin")
	}
	// Consenting to less than the preview showed is refused.
	var ce *ConsentError
	if _, err := m.Consent(pv.Hash, pv.Accept[1:], "owner", nil); !errors.As(err, &ce) {
		t.Fatalf("partial consent accepted: %v", err)
	}
	if _, err := m.Consent("0000", pv.Accept, "owner", nil); !errors.Is(err, ErrUnknownPreview) {
		t.Fatalf("unknown preview: %v", err)
	}
	p, err := m.Consent(pv.Hash, pv.Accept, "owner", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Active() || p.Row.ConsentedHash != pv.Hash || p.Row.ConsentedBy != "owner" {
		t.Fatalf("installed plugin: %+v", p.Row)
	}
	// Editing the source directory changes nothing installed: it was copied.
	os.WriteFile(filepath.Join(dir, "hooks/h.py"), []byte("print('evil')"), 0o644)
	m.Reload()
	if p, _ := m.Get("acme.notes"); !p.Active() {
		t.Fatalf("editing the source affected the installed copy: %s", p.Status)
	}
}

func TestAModifiedInstallStopsUntilTrustedAgain(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()
	writePlugin(t, dir, "1.0.0", nil)
	p := installPath(t, m, dir)
	stored := m.storePath(p.Row.ContentHash)
	target := filepath.Join(stored, "hooks", "h.py")
	os.Chmod(filepath.Dir(target), 0o755)
	os.Chmod(target, 0o644)
	if err := os.WriteFile(target, []byte("print('changed')"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Reload()
	p, _ = m.Get("acme.notes")
	if p.Status != StatusModified || p.Active() {
		t.Fatalf("status %s after tampering", p.Status)
	}
	if len(m.ActiveFor(0)) != 4 { // only the bundled plugins
		t.Fatal("a modified plugin still contributes")
	}
	pv, err := m.TrustPreview("acme.notes")
	if err != nil {
		t.Fatal(err)
	}
	if pv.Hash == p.Row.ContentHash || pv.Unchanged {
		t.Fatal("trust preview did not see the change")
	}
	if _, err := m.Consent(pv.Hash, pv.Accept, "owner", nil); err != nil {
		t.Fatal(err)
	}
	if p, _ = m.Get("acme.notes"); !p.Active() {
		t.Fatalf("status %s after re-trust", p.Status)
	}
}

func TestUpdateReportsGrownCapabilities(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()
	writePlugin(t, dir, "1.0.0", nil)
	installPath(t, m, dir)
	// Same capabilities, new version: an update with nothing grown.
	writePlugin(t, dir, "1.1.0", nil)
	pv, err := m.UpdatePreview(context.Background(), "acme.notes", "")
	if err != nil {
		t.Fatal(err)
	}
	if pv.FromVersion != "1.0.0" || len(pv.Grown) != 0 {
		t.Fatalf("update from %s grown %v", pv.FromVersion, pv.Grown)
	}
	// A new host hook grows host_exec's detail.
	hookLine := "    - {event: task.finished, run: host, command: [python3, hooks/h.py]}\n"
	manifest := strings.Replace(fmt.Sprintf(notesManifest, "2.0.0"), hookLine,
		hookLine+"    - {event: ci.failed, run: host, command: [python3, hooks/h.py]}\n", 1)
	os.WriteFile(filepath.Join(dir, "lectern-plugin.yaml"), []byte(manifest), 0o644)
	pv, err = m.UpdatePreview(context.Background(), "acme.notes", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Grown) != 1 || pv.Grown[0] != "host_exec: ci.failed: python3 hooks/h.py" {
		t.Fatalf("grown %v", pv.Grown)
	}
	if _, err := m.Consent(pv.Hash, pv.Accept, "owner", nil); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.Get("acme.notes"); p.Manifest.Version != "2.0.0" || !p.Active() {
		t.Fatalf("after update: %s %s", p.Manifest.Version, p.Status)
	}
}

func TestBundledPluginsCanBeDisabledButNotRemovedOrShadowed(t *testing.T) {
	m := newManager(t)
	off := false
	if _, err := m.SetEnabled("lectern.spec-kit", &off, nil); err != nil {
		t.Fatal(err)
	}
	for _, w := range m.Workflows(0) {
		if w.ID == "spec-kit" {
			t.Fatal("a disabled bundled plugin still offers its workflow")
		}
	}
	if err := m.Remove("lectern.spec-kit"); err == nil {
		t.Fatal("removed a bundled plugin")
	}
	// A plugin cannot claim a bundled id.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "lectern-plugin.yaml"), []byte("id: lectern.spec-kit\nname: x\nversion: 1\ncapabilities: {}\ncontributes: {}\n"), 0o644)
	if _, err := m.Preview(context.Background(), Source{Kind: "path", Path: dir}); err == nil {
		t.Fatal("previewed a plugin claiming a bundled id")
	}
}

func TestContributionsFlowToTheirPlacesAndRespectScope(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()
	writePlugin(t, dir, "1.0.0", nil)
	installPath(t, m, dir)
	if err := m.SetSecrets("acme.notes", map[string]string{"NOTES_TOKEN": "s3cret"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetSecrets("acme.notes", map[string]string{"OTHER": "x"}); err == nil {
		t.Fatal("stored an undeclared secret")
	}
	ui := m.UI()
	if len(ui.QuickCommands) != 1 || ui.QuickCommands[0].ID != "acme.notes/go" || !ui.QuickCommands[0].Enter ||
		len(ui.Themes) != 1 || len(ui.PaletteCommands) != 1 {
		t.Fatalf("ui %+v", ui)
	}
	var skill bool
	for _, w := range m.Workflows(5) {
		if w.ID == "acme.notes:review" && w.Kind == "skill" && w.Entry == "notes-review" {
			skill = true
		}
	}
	if !skill {
		t.Fatal("the plugin skill is not offered to projects")
	}
	// URL servers need no staging; the secret is substituted.
	servers, err := m.MCPServers(context.Background(), nil, 1, 5, map[string]bool{"notes": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := servers["notes"]; ok {
		t.Fatal("a plugin server replaced the project's own server of the same name")
	}
	if servers["docs"] == nil {
		t.Fatalf("servers %v", servers)
	}
	// Scope to project 7: project 5 no longer sees it.
	if _, err := m.SetEnabled("acme.notes", nil, []int64{7}); err != nil {
		t.Fatal(err)
	}
	for _, w := range m.Workflows(5) {
		if strings.HasPrefix(w.ID, "acme.notes:") {
			t.Fatal("scoped plugin offered to another project")
		}
	}
	if got, _ := m.MCPServers(context.Background(), nil, 1, 5, nil); len(got) != 0 {
		t.Fatalf("scoped plugin servers leaked: %v", got)
	}
	off := false
	m.SetEnabled("acme.notes", &off, nil)
	if len(m.UI().QuickCommands) != 0 {
		t.Fatal("disabled plugin still contributes quick commands")
	}
}

// ---- git and index sources, against a local git HTTP server ----

func gitServer(t *testing.T) (string, string) {
	t.Helper()
	AllowLoopbackHTTP = true
	t.Cleanup(func() { AllowLoopbackHTTP = false })
	backend := filepath.Join(strings.TrimSpace(run(t, "", "git", "--exec-path")), "git-http-backend")
	root := t.TempDir()
	srv := httptest.NewServer(&cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}})
	t.Cleanup(srv.Close)
	return srv.URL, root
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// publish commits dir's contents to <root>/<name>.git and returns the commit.
func publish(t *testing.T, root, name, work string) string {
	t.Helper()
	bare := filepath.Join(root, name+".git")
	if _, err := os.Stat(bare); err != nil {
		run(t, "", "git", "init", "-q", "--bare", "-b", "main", bare)
		run(t, bare, "git", "config", "http.receivepack", "true")
	}
	if _, err := os.Stat(filepath.Join(work, ".git")); err != nil {
		run(t, work, "git", "init", "-q", "-b", "main")
		run(t, work, "git", "remote", "add", "origin", bare)
	}
	run(t, work, "git", "add", "-A")
	run(t, work, "git", "commit", "-q", "-m", "publish", "--allow-empty")
	run(t, work, "git", "push", "-q", "origin", "main")
	return strings.TrimSpace(run(t, work, "git", "rev-parse", "HEAD"))
}

func TestInstallFromGitPinsTheCommitAndTree(t *testing.T) {
	m := newManager(t)
	base, root := gitServer(t)
	work := t.TempDir()
	writePlugin(t, work, "1.0.0", nil)
	sha1 := publish(t, root, "notes", work)
	pv, err := m.Preview(context.Background(), Source{Kind: "git", URL: base + "/notes.git", Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Commit != sha1 || len(pv.Tree) != 40 {
		t.Fatalf("commit %s tree %s, want commit %s", pv.Commit, pv.Tree, sha1)
	}
	p, err := m.Consent(pv.Hash, pv.Accept, "owner", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Row.CommitSHA != sha1 || p.Row.SourceKind != "git" || p.Row.Ref != "main" {
		t.Fatalf("row %+v", p.Row)
	}
	// The branch moves; the installed plugin does not, until an update.
	writePlugin(t, work, "1.1.0", nil)
	sha2 := publish(t, root, "notes", work)
	m.Reload()
	if p, _ := m.Get("acme.notes"); p.Row.CommitSHA != sha1 || p.Manifest.Version != "1.0.0" {
		t.Fatal("the installed plugin followed the branch")
	}
	pv, err = m.UpdatePreview(context.Background(), "acme.notes", "")
	if err != nil {
		t.Fatal(err)
	}
	if pv.Commit != sha2 || pv.Manifest.Version != "1.1.0" {
		t.Fatalf("update preview at %s version %s", pv.Commit, pv.Manifest.Version)
	}
	// An exact older commit can be installed by its sha.
	pv, err = m.Preview(context.Background(), Source{Kind: "git", URL: base + "/notes.git", Commit: sha1})
	if err != nil || pv.Commit != sha1 || pv.Manifest.Version != "1.0.0" {
		t.Fatalf("pinned preview: %v %+v", err, pv)
	}
}

func TestGitURLsThatRunProgramsAreRefused(t *testing.T) {
	for _, u := range []string{"ext::sh -c touch% /tmp/pwned", "fd::17", "http://example.com/x.git", "git://example.com/x"} {
		if err := CheckGitURL(u); err == nil {
			t.Errorf("%q was allowed", u)
		}
	}
	for _, u := range []string{"https://github.com/a/b.git", "git@github.com:a/b.git", "ssh://git@host/x", "file:///tmp/x"} {
		if err := CheckGitURL(u); err != nil {
			t.Errorf("%q: %v", u, err)
		}
	}
	// Plain http to this machine is for tests only, and off by default.
	if err := CheckGitURL("http://127.0.0.1:8080/x.git"); err == nil {
		t.Error("loopback http allowed outside tests")
	}
	AllowLoopbackHTTP = true
	defer func() { AllowLoopbackHTTP = false }()
	if err := CheckGitURL("http://127.0.0.1:8080/x.git"); err != nil {
		t.Error(err)
	}
	if err := CheckGitURL("http://example.com/x.git"); err == nil {
		t.Error("remote http allowed")
	}
}

func TestInstallFromAMarketplaceIndexUsesTheListedCommit(t *testing.T) {
	m := newManager(t)
	base, root := gitServer(t)
	work := t.TempDir()
	writePlugin(t, work, "1.0.0", nil)
	sha1 := publish(t, root, "notes", work)
	writePlugin(t, work, "9.9.9", nil) // the branch moves past what the index lists
	publish(t, root, "notes", work)

	market := t.TempDir()
	os.WriteFile(filepath.Join(market, IndexFile), []byte(fmt.Sprintf(`name: acme
plugins:
  - id: acme.notes
    description: Notes for tasks
    source: %s/notes.git
    commit: %s
`, base, sha1)), 0o644)
	publish(t, root, "market", market)
	if _, err := m.AddSource(context.Background(), "acme", base+"/market.git", ""); err != nil {
		t.Fatal(err)
	}
	found, problems, err := m.Search(context.Background(), "notes", false)
	if err != nil || len(problems) > 0 || len(found) != 1 || found[0].ID != "acme.notes" {
		t.Fatalf("search: %v %v %+v", err, problems, found)
	}
	pv, err := m.Preview(context.Background(), Source{Kind: "index", ID: "acme.notes"})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Commit != sha1 || pv.Manifest.Version != "1.0.0" || pv.Source.Index != "acme" {
		t.Fatalf("index install at %s version %s", pv.Commit, pv.Manifest.Version)
	}
	// An index entry pinned to a branch name is refused.
	os.WriteFile(filepath.Join(market, IndexFile), []byte(fmt.Sprintf("name: acme\nplugins:\n  - {id: acme.notes, source: %s/notes.git, commit: main}\n", base)), 0o644)
	publish(t, root, "market", market)
	if _, err := m.Preview(context.Background(), Source{Kind: "index", ID: "acme.notes"}); err == nil || !strings.Contains(err.Error(), "full commit sha") {
		t.Fatalf("branch-pinned index entry: %v", err)
	}
}

func TestClaudeMarketplaceWithRelativePlugins(t *testing.T) {
	m := newManager(t)
	base, root := gitServer(t)
	market := t.TempDir()
	os.MkdirAll(filepath.Join(market, ".claude-plugin"), 0o755)
	os.MkdirAll(filepath.Join(market, "plugins/pr/.claude-plugin"), 0o755)
	os.MkdirAll(filepath.Join(market, "plugins/pr/skills/pr-review"), 0o755)
	os.WriteFile(filepath.Join(market, ".claude-plugin/marketplace.json"), []byte(`{"name":"cc","plugins":[{"name":"pr","source":"./plugins/pr"},{"name":"remote","source":{"source":"github","repo":"a/b"}}]}`), 0o644)
	os.WriteFile(filepath.Join(market, "plugins/pr/.claude-plugin/plugin.json"), []byte(`{"name":"pr","version":"1.0.0"}`), 0o644)
	os.WriteFile(filepath.Join(market, "plugins/pr/skills/pr-review/SKILL.md"), []byte("---\ndescription: review\n---\n"), 0o644)
	publish(t, root, "cc", market)
	if _, err := m.AddSource(context.Background(), "cc", base+"/cc.git", ""); err != nil {
		t.Fatal(err)
	}
	found, _, _ := m.Search(context.Background(), "", false)
	if len(found) != 1 || found[0].ID != "pr" {
		t.Fatalf("found %+v (a branch-only entry must be skipped)", found)
	}
	pv, err := m.Preview(context.Background(), Source{Kind: "index", ID: "pr"})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Format != "claude" || len(pv.Manifest.Contributes.Skills) != 1 {
		t.Fatalf("claude plugin preview %+v", pv)
	}
}

// ---- hooks ----

func TestHooksRunOnlyForConsentedEnabledUnmodifiedPlugins(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()
	writePlugin(t, dir, "1.0.0", nil)
	p := installPath(t, m, dir)
	m.SetSecrets("acme.notes", map[string]string{"NOTES_TOKEN": "tok"})

	var mu sync.Mutex
	var calls []string
	var notes []string
	h := &Hooks{M: m, Notify: func(title, body, url string) { mu.Lock(); notes = append(notes, body); mu.Unlock() }}
	h.RunHost = func(ctx context.Context, dir string, argv, env []string, stdin []byte) ([]byte, error) {
		mu.Lock()
		calls = append(calls, dir+": "+strings.Join(argv, " "))
		mu.Unlock()
		return runHostCommand(ctx, dir, argv, env, stdin)
	}
	h.Start(context.Background())
	finish := func(id int64) {
		h.Observe("task", &store.Task{ID: id, ProjectID: 1, Status: "queued"})
		h.Observe("task", &store.Task{ID: id, ProjectID: 1, Status: "running"})
		h.Observe("task", &store.Task{ID: id, ProjectID: 1, Status: "review"})
		h.Wait()
	}
	finish(1)
	runs, _ := m.DB.PluginHookRuns("acme.notes", 10)
	if len(calls) != 1 || len(runs) != 1 || !runs[0].OK || runs[0].Message != "saw task.finished for task 1 token=tok" {
		t.Fatalf("calls %v runs %+v", calls, runs)
	}
	if len(notes) != 1 || notes[0] != "done" {
		t.Fatalf("notify %v", notes)
	}
	if calls[0] != m.storePath(p.Row.ContentHash)+": python3 hooks/h.py" {
		t.Fatalf("hook ran %q, not the consented copy", calls[0])
	}

	// Disabled: nothing runs.
	off, on := false, true
	m.SetEnabled("acme.notes", &off, nil)
	finish(2)
	// Modified on disk: nothing runs, and the plugin is marked.
	m.SetEnabled("acme.notes", &on, nil)
	stored := filepath.Join(m.storePath(p.Row.ContentHash), "hooks")
	os.Chmod(stored, 0o755)
	os.Chmod(filepath.Join(stored, "h.py"), 0o644)
	os.WriteFile(filepath.Join(stored, "h.py"), []byte("print('{\"ok\": true, \"message\": \"tampered\"}')"), 0o644)
	finish(3)
	if len(calls) != 1 {
		t.Fatalf("hooks ran for a disabled or modified plugin: %v", calls)
	}
	if p, _ := m.Get("acme.notes"); p.Status != StatusModified {
		t.Fatalf("status %s", p.Status)
	}
}

func TestHookEventsFromTheBusShapes(t *testing.T) {
	m := newManager(t)
	h := &Hooks{M: m}
	h.Start(context.Background())
	var fired []string
	var mu sync.Mutex
	// Record by installing a stand-in that captures events for every name.
	dir := t.TempDir()
	var hooks []string
	for _, e := range pluginpkg.Events {
		hooks = append(hooks, fmt.Sprintf("    - {event: %s, run: host, command: [record]}", e))
	}
	os.WriteFile(filepath.Join(dir, "lectern-plugin.yaml"), []byte("id: acme.rec\nname: Rec\nversion: 1\ncapabilities: {host_exec: true}\ncontributes:\n  hooks:\n"+strings.Join(hooks, "\n")+"\n"), 0o644)
	installPath(t, m, dir)
	h.RunHost = func(ctx context.Context, dir string, argv, env []string, stdin []byte) ([]byte, error) {
		var ev Event
		json.Unmarshal(stdin, &ev)
		mu.Lock()
		fired = append(fired, ev.Event)
		mu.Unlock()
		return []byte(`{"ok":true}`), nil
	}
	now := store.Now() + 1
	h.Observe("session", &store.Session{ID: 9, Status: "starting", CreatedAt: now})
	h.Observe("session", &store.Session{ID: 9, Status: "running", CreatedAt: now})
	h.Observe("session", &store.Session{ID: 9, Status: "dead", CreatedAt: now})
	h.Observe("session", &store.Session{ID: 10, Status: "running", CreatedAt: 1}) // existed before Lectern started
	h.Observe("approval", &store.Approval{ID: 4, Status: "pending"})
	h.Observe("approval", &store.Approval{ID: 4, Status: "approved"})
	h.Wait()
	want := "session.start,session.end,approval.requested,approval.decided"
	if strings.Join(fired, ",") != want {
		t.Fatalf("fired %v, want %s", fired, want)
	}
}

// A server that runs a program from the plugin gets the plugin copied onto
// the machine first, into an immutable content-addressed directory, and a
// declared secret substituted.
func TestMCPServersStageThePluginOnTheTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	m := newManager(t)
	dir := t.TempDir()
	writePlugin(t, dir, "1.0.0", nil)
	p := installPath(t, m, dir)
	m.SetSecrets("acme.notes", map[string]string{"NOTES_TOKEN": `tok"en`})
	servers, err := m.MCPServers(context.Background(), executor.NewLocal(), 1, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	notes, _ := servers["notes"].(map[string]any)
	root := filepath.Join(home, ".lectern", "plugins", "acme.notes", p.Pkg.Hash[:16])
	args, _ := notes["args"].([]any)
	if len(args) != 1 || args[0] != root+"/mcp.py" {
		t.Fatalf("args %v, want %s/mcp.py", args, root)
	}
	if env, _ := notes["env"].(map[string]any); env["TOKEN"] != `tok"en` {
		t.Fatalf("secret not substituted: %v", notes["env"])
	}
	if b, err := os.ReadFile(filepath.Join(root, "mcp.py")); err != nil || string(b) != "print('mcp')\n" {
		t.Fatalf("staged file: %q %v", b, err)
	}
	// Without its secret, a server that needs one is left out, not launched broken.
	m.SetSecrets("acme.notes", map[string]string{"NOTES_TOKEN": ""})
	servers, _ = m.MCPServers(context.Background(), executor.NewLocal(), 1, 3, nil)
	if _, ok := servers["notes"]; ok {
		t.Fatal("a server was configured without its secret")
	}
}
