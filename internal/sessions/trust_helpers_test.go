package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/helpers/helperstest"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

func TestHelperProcess(t *testing.T) { helperstest.Serve() }

func builtinSpec(t *testing.T, name string) Spec {
	t.Helper()
	for _, s := range Builtins() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no built-in %s", name)
	return Spec{}
}

func writeFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(root, 0o755)
	for name, content := range files {
		path := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// trustParity runs a built-in trust command's Python and its Go helper in the
// same fixture and compares exit status and every file left behind.
func trustParity(t *testing.T, agent, helper string, cases []trustCase) {
	testutil.RequireIsolated(t)
	helperstest.RequirePython(t)
	spec := builtinSpec(t, agent)
	home := filepath.Join(t.TempDir(), "home")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			proc := helperstest.Proc{Dir: t.TempDir(), Env: helperstest.Env(home, c.env...)}
			writeFixture(t, home, c.files)
			py := helperstest.Run(t, proc, "bash", "-c", spec.TrustProbe(c.dir))
			pyTree := helperstest.Tree(t, home)
			writeFixture(t, home, c.files)
			got := helperstest.Go(t, proc, helper, c.dir)
			helperstest.Same(t, c.name, py, got)
			helperstest.SameTree(t, c.name, pyTree, helperstest.Tree(t, home))
		})
	}
}

type trustCase struct {
	name  string
	dir   string
	files map[string]string
	env   []string
}

func TestClaudeTrustHelperParity(t *testing.T) {
	existing := `{"numStartups": 12, "tipsHistory": {"a": 1.50, "b": 1e22, "c": 1e-7},
  "big": 123456789012345678901234567890, "nan": NaN, "neg": -Infinity,
  "text": "caf\u00e9 \ud83d\ude00 <&> \u2028 \u007f", "lone": "\udc00",
  "projects": {"/other": {"allowedTools": [], "hasTrustDialogAccepted": false}},
  "empty": {}, "list": [], "dup": 1, "dup": 2}`
	trustParity(t, "claude", "claude-trust", []trustCase{
		{name: "no config", dir: "/work/a"},
		{name: "existing config", dir: "/work/a b's \u00e9", files: map[string]string{".claude.json": existing}},
		{name: "already trusted", dir: "/other", files: map[string]string{".claude.json": `{"projects": {"/other": {"hasTrustDialogAccepted": true}}}`}},
		{name: "truthy but not True", dir: "/other", files: map[string]string{".claude.json": `{"projects": {"/other": {"hasTrustDialogAccepted": 1}}}`}},
		{name: "unreadable config", dir: "/w", files: map[string]string{".claude.json": `{"projects": `}},
		{name: "config not an object", dir: "/w", files: map[string]string{".claude.json": `[1]`}},
		{name: "projects not an object", dir: "/w", files: map[string]string{".claude.json": `{"projects": [1]}`}},
		{name: "entry not an object", dir: "/w", files: map[string]string{".claude.json": `{"projects": {"/w": "x"}}`}},
		{name: "CLAUDE_CONFIG_DIR", dir: "/w", env: []string{"CLAUDE_CONFIG_DIR=~/alt/cfg"},
			files: map[string]string{"alt/cfg/.claude.json": `{"x": 1}`}},
	})
}

func TestCodexTrustHelperParity(t *testing.T) {
	const cfg = "# my settings\nmodel = \"o3\"   # keep this comment\n\n[mcp_servers.x]\ncommand = \"x\"\n" +
		"[projects.\"/known\"]\ntrust_level = \"trusted\"\n"
	trustParity(t, "codex", "codex-trust", []trustCase{
		{name: "no config", dir: "/work/a"},
		{name: "empty config", dir: "/work/a", files: map[string]string{".codex/config.toml": ""}},
		{name: "blank config", dir: "/work/a", files: map[string]string{".codex/config.toml": " \n\t\n"}},
		{name: "new project", dir: "/work/new \"q\" \\ \u00e9", files: map[string]string{".codex/config.toml": cfg}},
		{name: "known project", dir: "/known", files: map[string]string{".codex/config.toml": cfg}},
		{name: "known inline", dir: "/k", files: map[string]string{".codex/config.toml": "projects = { \"/k\" = { trust_level = \"trusted\" } }\n"}},
		{name: "known dotted", dir: "/k", files: map[string]string{".codex/config.toml": "projects.\"/k\".trust_level = \"trusted\"\n"}},
		{name: "project list", dir: "/k", files: map[string]string{".codex/config.toml": "projects = [\"/k\"]\n"}},
		{name: "project string", dir: "/k", files: map[string]string{".codex/config.toml": "projects = \"/k/sub\"\n"}},
		{name: "project number", dir: "/k", files: map[string]string{".codex/config.toml": "projects = 5\n"}},
		{name: "invalid TOML", dir: "/k", files: map[string]string{".codex/config.toml": "[projects\n"}},
		{name: "duplicate would be invalid", dir: "/k", files: map[string]string{".codex/config.toml": "[projects.'/k']\n"}},
		{name: "CRLF config", dir: "/k", files: map[string]string{".codex/config.toml": "a = 1\r\n[projects.\"/k\"]\r\n"}},
		{name: "lone CR", dir: "/k", files: map[string]string{".codex/config.toml": "a = 1\r[projects.\"/j\"]\r"}},
		{name: "invalid date", dir: "/k", files: map[string]string{".codex/config.toml": "d = 2021-02-30\n"}},
		{name: "not UTF-8", dir: "/k", files: map[string]string{".codex/config.toml": "a = \"\xff\"\n"}},
		{name: "CODEX_HOME", dir: "/k", env: []string{"CODEX_HOME=~/elsewhere/codex"}},
	})
}

// A target with a lectern binary runs the built-in trust commands as Go
// helpers; one without keeps the Python, and operator-defined commands are
// never replaced.
func TestTrustProbeOnPicksTheHelper(t *testing.T) {
	plain := executor.NewMock(0)
	withLectern := executor.NewMock(0)
	executor.SetTargetEnv(withLectern, executor.TargetEnv{Lectern: "/opt/lectern/lectern"})
	for agent, helper := range map[string]string{"claude": "claude-trust", "codex": "codex-trust"} {
		spec := builtinSpec(t, agent)
		if got := spec.TrustProbeOn(plain, "/w d"); got != spec.TrustProbe("/w d") {
			t.Errorf("%s without lectern: %q", agent, got)
		}
		if got, want := spec.TrustProbeOn(withLectern, "/w d"), "/opt/lectern/lectern helper "+helper+" '/w d'"; got != want {
			t.Errorf("%s with lectern: %q, want %q", agent, got, want)
		}
	}
	legacy := builtinSpec(t, "claude")
	legacy.TrustCommand = legacyClaudeTrust
	if got := legacy.TrustProbeOn(withLectern, "/w"); !strings.Contains(got, "helper claude-trust") {
		t.Errorf("a legacy snapshot should run the helper too: %q", got)
	}
	custom := Spec{Name: "x", TrustCommand: "touch {dir}/.trusted"}
	if got := custom.TrustProbeOn(withLectern, "/w"); got != "touch /w/.trusted" {
		t.Errorf("an operator's trust command must run as declared: %q", got)
	}
}

func TestCodexNotifyHelperArg(t *testing.T) {
	got := codexNotifyHelperArg(`/opt/"lec"\bin/lectern`)
	want := []string{"-c", `notify=["/opt/\"lec\"\\bin/lectern","helper","codex-notify"]`}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("got %q, want %q", got, want)
	}
}
