//go:build unix

package helpers

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitFixture makes a repository with one commit at dir; dates are fixed so
// two fixtures are byte-for-byte alike where it matters.
func gitFixture(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	requireGit(t)
	os.MkdirAll(dir, 0o755)
	for name, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-qm", "base", "--allow-empty"}} {
		gitIn(t, dir, args...)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=T",
		"GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGeminiWorkspaceParity(t *testing.T) {
	script := pythonConst(t, "agents/gemini_workspace.go", "geminiWorkspaceScript")
	servers := `{"lectern":{"httpUrl":"http://127.0.0.1:1/mcp","headers":{"A":"b"},"timeout":1.0},"zeta":{"command":"z","args":["é"]}}`
	install := func(root string, session string, owned bool, workdir string) []string {
		o := "false"
		if owned {
			o = "true"
		}
		return []string{`{"op":"install","owned":` + o + `,"servers":` + servers + `,"session":` + session + `,"workdir":"` + workdir + `"}`}
	}
	remove := func(root, session, workdir string) []string {
		return []string{`{"op":"remove","session":` + session + `,"workdir":"` + workdir + `"}`}
	}
	normalize := func(root, s string) string {
		for _, wd := range []string{"/wt", "/home/lectern-scratch/job"} {
			sum := sha256.Sum256([]byte(pyRealpath(root + wd)))
			h := hex.EncodeToString(sum[:])
			s = strings.ReplaceAll(strings.ReplaceAll(s, h[:24], "$LEDGER"), h[:12], "$MARK")
		}
		return s
	}
	env := func(root string) []string {
		return []string{"HOME=" + root + "/home", "XDG_STATE_HOME=" + root + "/state", "LECTERN_SCRATCH_ROOT=", "AGENTDECK_SCRATCH_ROOT=",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, root string)
		steps func(root string) [][]string
		env   func(root string) []string
	}{
		{name: "shared lifecycle", steps: func(r string) [][]string {
			wd := r + "/wt"
			return [][]string{install(r, "5", true, wd), install(r, "6", true, wd), remove(r, "5", wd), remove(r, "6", wd), remove(r, "6", wd)}
		}},
		{name: "existing settings keep user servers", setup: func(t *testing.T, r string) {
			os.MkdirAll(r+"/wt/.gemini", 0o755)
			os.WriteFile(r+"/wt/.gemini/settings.json", []byte("{\"theme\": \"dark\", \"n\": 1e3, \"mcpServers\": {\"zeta\": {\"command\": \"mine\"}}, \"u\": \"\\u00e9\"}"), 0o640)
		}, steps: func(r string) [][]string {
			wd := r + "/wt"
			return [][]string{install(r, "1", true, wd), remove(r, "1", wd)}
		}},
		{name: "state from home", env: func(root string) []string {
			return []string{"HOME=" + root + "/home", "XDG_STATE_HOME=", "LECTERN_SCRATCH_ROOT=", "GIT_CONFIG_GLOBAL=/dev/null"}
		}, steps: func(r string) [][]string {
			return [][]string{install(r, "1", true, r+"/wt"), remove(r, "1", r+"/wt")}
		}},
		{name: "not owned outside scratch", steps: func(r string) [][]string {
			return [][]string{install(r, "1", false, r+"/wt")}
		}},
		{name: "scratch workspace", setup: func(t *testing.T, r string) {
			gitFixture(t, r+"/home/lectern-scratch/job", nil)
		}, steps: func(r string) [][]string {
			return [][]string{install(r, "2", false, r+"/home/lectern-scratch/job"), remove(r, "2", r+"/home/lectern-scratch/job")}
		}},
		{name: "tracked settings", setup: func(t *testing.T, r string) {
			os.MkdirAll(r+"/wt/.gemini", 0o755)
			os.WriteFile(r+"/wt/.gemini/settings.json", []byte("{}"), 0o644)
			gitIn(t, r+"/wt", "add", ".gemini/settings.json")
			gitIn(t, r+"/wt", "commit", "-qm", "settings")
		}, steps: func(r string) [][]string { return [][]string{install(r, "3", true, r+"/wt")} }},
		{name: "symlinked gemini dir", setup: func(t *testing.T, r string) {
			os.MkdirAll(r+"/elsewhere", 0o755)
			os.Symlink(r+"/elsewhere", r+"/wt/.gemini")
		}, steps: func(r string) [][]string { return [][]string{install(r, "3", true, r+"/wt")} }},
		{name: "comments in settings", setup: func(t *testing.T, r string) {
			os.MkdirAll(r+"/wt/.gemini", 0o755)
			os.WriteFile(r+"/wt/.gemini/settings.json", []byte("// hi\n{}"), 0o644)
		}, steps: func(r string) [][]string { return [][]string{install(r, "3", true, r+"/wt")} }},
		{name: "settings not an object", setup: func(t *testing.T, r string) {
			os.MkdirAll(r+"/wt/.gemini", 0o755)
			os.WriteFile(r+"/wt/.gemini/settings.json", []byte("[1]"), 0o644)
		}, steps: func(r string) [][]string { return [][]string{install(r, "3", true, r+"/wt")} }},
		{name: "servers not an object", setup: func(t *testing.T, r string) {
			os.MkdirAll(r+"/wt/.gemini", 0o755)
			os.WriteFile(r+"/wt/.gemini/settings.json", []byte(`{"mcpServers": []}`), 0o644)
		}, steps: func(r string) [][]string { return [][]string{install(r, "3", true, r+"/wt")} }},
		{name: "missing workspace", steps: func(r string) [][]string {
			return [][]string{install(r, "3", true, r+"/nope"), remove(r, "3", r+"/nope")}
		}},
		{name: "missing keys", steps: func(r string) [][]string {
			return [][]string{{`{"op":"install"}`}, {`{"workdir":"` + r + `/wt"}`}, {`{"op":"install","workdir":"` + r + `/wt","owned":true}`}}
		}},
		{name: "unreadable input", steps: func(r string) [][]string { return [][]string{{`{`}} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			twinSteps(t, script, "gemini-workspace", normalize, func(t *testing.T, root string) (runSpec, [][]string) {
				gitFixture(t, root+"/wt", map[string]string{"README": "x\n"})
				os.MkdirAll(root+"/home", 0o755)
				if c.setup != nil {
					c.setup(t, root)
				}
				e := env
				if c.env != nil {
					e = c.env
				}
				return runSpec{dir: root, env: e(root)}, c.steps(root)
			})
		})
	}
}
