package pluginpkg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func files(manifest string, extra ...File) []File {
	return append([]File{{Path: ManifestName, Mode: 0o644, Data: []byte(manifest)}}, extra...)
}

const good = `
id: acme.notes
name: Notes
version: 1.0.0
min_lectern: 2.0.0
homepage: https://example.com
capabilities:
  host_exec: true
  target_exec: true
  mcp_tools: true
  secrets: [NOTES_TOKEN]
  network: [api.example.com]
  notify: true
contributes:
  skills:
    - {id: review, path: skills/review, description: Review things}
  workflows:
    - {id: triage, name: Triage, path: workflows/triage, commands: ["acme-triage go"]}
  mcp_servers:
    notes: {command: python3, args: ["${LECTERN_PLUGIN_ROOT}/mcp.py"], env: {T: "${secret:NOTES_TOKEN}"}}
    docs: {type: http, url: "https://mcp.example.com"}
  hooks:
    - {event: task.finished, run: host, command: [python3, hooks/h.py]}
    - {event: session.start, run: target, command: [echo, hi], timeout: 5}
  quick_commands:
    - {id: go, label: Go, text: "go"}
  themes:
    - {id: forest, name: Forest, accent: "#2f855a", dark: {bg: "#0b1410"}}
  palette_commands:
    - {id: docs, title: Docs, href: "https://example.com/docs"}
    - {id: plugins, title: Plugins, href: "#settings/plugins"}
`

var goodFiles = []File{
	{Path: "skills/review/SKILL.md", Mode: 0o644, Data: []byte("---\nname: review\n---\n")},
	{Path: "workflows/triage/SKILL.md", Mode: 0o644, Data: []byte("x")},
	{Path: "hooks/h.py", Mode: 0o755, Data: []byte("print('{}')")},
}

func TestAValidManifestLoadsWithItsCapabilities(t *testing.T) {
	pkg, err := Load(files(good, goodFiles...), false)
	if err != nil {
		t.Fatal(err)
	}
	keys := strings.Join(CapabilityKeys(pkg.Manifest.CapabilityList()), "\n")
	for _, want := range []string{
		"host_exec: task.finished: python3 hooks/h.py",
		"target_exec: session.start: echo hi",
		`target_exec: MCP server notes: python3 ["${LECTERN_PLUGIN_ROOT}/mcp.py"]`,
		"mcp_tools: docs: https://mcp.example.com",
		"secrets: NOTES_TOKEN",
		"network: api.example.com",
		"notify",
	} {
		if !strings.Contains(keys, want) {
			t.Errorf("capability list lacks %q:\n%s", want, keys)
		}
	}
	if pkg.Hash == "" || pkg.Format != "lectern" {
		t.Fatalf("hash %q format %q", pkg.Hash, pkg.Format)
	}
	if e := pkg.Manifest.EntryFor("review", ""); e != "notes-review" {
		t.Fatalf("default entry %q", e)
	}
}

func TestValidationRefusesWhatItShould(t *testing.T) {
	cases := map[string]struct{ manifest, want string }{
		"unknown field":      {"id: a\nname: A\nversion: 1\ncapabilities: {hostexec: true}\ncontributes: {}", "field hostexec not found"},
		"reserved id":        {"id: lectern.x\nname: A\nversion: 1\ncapabilities: {}\ncontributes: {}", "reserved"},
		"bad id":             {"id: Bad_ID\nname: A\nversion: 1\ncapabilities: {}\ncontributes: {}", "id"},
		"host hook, no cap":  {"id: a\nname: A\nversion: 1\ncapabilities: {}\ncontributes: {hooks: [{event: task.finished, run: host, command: [true]}]}", "host_exec"},
		"mcp command no cap": {"id: a\nname: A\nversion: 1\ncapabilities: {mcp_tools: true}\ncontributes: {mcp_servers: {x: {command: node}}}", "target_exec"},
		"mcp no cap":         {"id: a\nname: A\nversion: 1\ncapabilities: {}\ncontributes: {mcp_servers: {x: {url: 'https://a.b'}}}", "mcp_tools"},
		"undeclared secret":  {"id: a\nname: A\nversion: 1\ncapabilities: {mcp_tools: true}\ncontributes: {mcp_servers: {x: {url: 'https://a.b', headers: {A: '${secret:NOPE}'}}}}", "NOPE"},
		"agents no cap":      {"id: a\nname: A\nversion: 1\ncapabilities: {}\ncontributes: {agents: [{name: x, command: x}]}", "capabilities.agents"},
		"unknown event":      {"id: a\nname: A\nversion: 1\ncapabilities: {host_exec: true}\ncontributes: {hooks: [{event: task.eaten, run: host, command: [true]}]}", "event"},
		"escaping path":      {"id: a\nname: A\nversion: 1\ncapabilities: {}\ncontributes: {skills: [{id: s, path: ../x}]}", "leaves the plugin"},
		"missing SKILL.md":   {"id: a\nname: A\nversion: 1\ncapabilities: {}\ncontributes: {skills: [{id: s, path: skills/s}]}", "SKILL.md is missing"},
		"bad theme token":    {"id: a\nname: A\nversion: 1\ncapabilities: {}\ncontributes: {themes: [{id: t, name: T, dark: {background: '#000000'}}]}", "not a theme token"},
		"bad theme colour":   {"id: a\nname: A\nversion: 1\ncapabilities: {}\ncontributes: {themes: [{id: t, name: T, dark: {bg: red}}]}", "#rrggbb"},
		"javascript href":    {"id: a\nname: A\nversion: 1\ncapabilities: {}\ncontributes: {palette_commands: [{id: p, title: P, href: 'javascript:alert(1)'}]}", "href"},
		"http href":          {"id: a\nname: A\nversion: 1\ncapabilities: {}\ncontributes: {palette_commands: [{id: p, title: P, href: 'http://x.y'}]}", "href"},
		"no version":         {"id: a\nname: A\ncapabilities: {}\ncontributes: {}", "version"},
		"hook file missing":  {"id: a\nname: A\nversion: 1\ncapabilities: {host_exec: true}\ncontributes: {hooks: [{event: task.finished, run: host, command: [hooks/x.sh]}]}", "not in the plugin"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(files(c.manifest), false)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error mentioning %q, got %v", c.want, err)
			}
		})
	}
}

func TestContentHashPinsEveryByteModeAndName(t *testing.T) {
	base := []File{{Path: "a", Mode: 0o644, Data: []byte("x")}, {Path: "b", Mode: 0o755, Data: []byte("y")}}
	h := ContentHash(base)
	reordered := []File{base[1], base[0]}
	if ContentHash(reordered) != h {
		t.Fatal("hash depends on file order")
	}
	for name, change := range map[string][]File{
		"bytes":  {{Path: "a", Mode: 0o644, Data: []byte("z")}, base[1]},
		"mode":   {{Path: "a", Mode: 0o755, Data: []byte("x")}, base[1]},
		"rename": {{Path: "c", Mode: 0o644, Data: []byte("x")}, base[1]},
		"added":  append(append([]File{}, base...), File{Path: "d", Mode: 0o644}),
	} {
		if ContentHash(change) == h {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
}

func TestReadDirRefusesSymlinksAndSkipsGit(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "run.sh"), []byte("x"), 0o755)
	got, err := ReadDir(dir)
	if err != nil || len(got) != 1 || got[0].Path != "run.sh" || got[0].Mode != 0o755 {
		t.Fatalf("got %+v %v", got, err)
	}
	os.Symlink("/etc/passwd", filepath.Join(dir, "leak"))
	if _, err := ReadDir(dir); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("a symlink was followed: %v", err)
	}
}

func TestWriteDirIsReadOnlyAndRoundTrips(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store", "abc")
	in := []File{{Path: "x/y.sh", Mode: 0o755, Data: []byte("hi")}, {Path: "z", Mode: 0o644, Data: []byte("z")}}
	if err := WriteDir(root, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadDir(root)
	if err != nil || ContentHash(out) != ContentHash(in) {
		t.Fatalf("round trip changed the content: %v", err)
	}
	if info, _ := os.Stat(filepath.Join(root, "z")); info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("installed file is writable: %v", info.Mode())
	}
}

func TestEveryBundledPluginIsValid(t *testing.T) {
	pkgs, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range pkgs {
		ids = append(ids, p.Manifest.ID)
	}
	if strings.Join(ids, ",") != "lectern.agent-catalog,lectern.delegate,lectern.maestro,lectern.spec-kit" {
		t.Fatalf("bundled plugins: %v", ids)
	}
}

func TestClaudeCodePluginImportsSkillsAndMCP(t *testing.T) {
	in := []File{
		{Path: ClaudeManifest, Mode: 0o644, Data: []byte(`{"name":"PR Helper","version":"2.1.0","description":"d","author":{"name":"Ann"}}`)},
		{Path: "skills/pr-review/SKILL.md", Mode: 0o644, Data: []byte("---\nname: pr-review\ndescription: Reviews PRs\n---\n")},
		{Path: ".mcp.json", Mode: 0o644, Data: []byte(`{"mcpServers":{"gh":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/gh-mcp"}}}`)},
		{Path: "commands/ship.md", Mode: 0o644, Data: []byte("x")},
		{Path: "hooks/hooks.json", Mode: 0o644, Data: []byte("{}")},
	}
	pkg, err := Load(in, false)
	if err != nil {
		t.Fatal(err)
	}
	m := pkg.Manifest
	if pkg.Format != "claude" || m.ID != "pr-helper" || m.Author != "Ann" || m.Version != "2.1.0" {
		t.Fatalf("identity: %+v", m)
	}
	if len(m.Contributes.Skills) != 1 || m.Contributes.Skills[0].Description != "Reviews PRs" {
		t.Fatalf("skills: %+v", m.Contributes.Skills)
	}
	if m.Contributes.MCPServers["gh"]["command"] != "${LECTERN_PLUGIN_ROOT}/bin/gh-mcp" {
		t.Fatalf("mcp: %+v", m.Contributes.MCPServers)
	}
	if !m.Capabilities.MCPTools || !m.Capabilities.TargetExec {
		t.Fatalf("derived capabilities: %+v", m.Capabilities)
	}
	if strings.Join(pkg.Skipped, ",") != "commands/,hooks/" {
		t.Fatalf("skipped: %v", pkg.Skipped)
	}
}

func TestScaffoldValidates(t *testing.T) {
	out, err := Scaffold("acme.hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(out, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Scaffold("lectern.mine"); err == nil {
		t.Fatal("scaffolded a reserved id")
	}
}

func TestMinLecternVersion(t *testing.T) {
	m := &Manifest{ID: "a", MinLectern: "2.5.0"}
	if m.CheckVersion("2.4.1") == nil {
		t.Fatal("2.4.1 accepted for >=2.5.0")
	}
	for _, v := range []string{"2.5.0", "2.10.0", "3.0", "dev"} {
		if err := m.CheckVersion(v); err != nil {
			t.Fatalf("%s: %v", v, err)
		}
	}
}

func TestCapabilityGrowth(t *testing.T) {
	a := []Capability{{Key: "host_exec", Detail: []string{"task.finished: a"}}}
	b := []Capability{{Key: "host_exec", Detail: []string{"task.finished: a", "ci.failed: b"}}, {Key: "notify"}}
	grown := Grown(a, b)
	if strings.Join(grown, "|") != "host_exec: ci.failed: b|notify" {
		t.Fatalf("grown %v", grown)
	}
	if len(Grown(b, a)) != 0 {
		t.Fatal("shrinking reported growth")
	}
}
