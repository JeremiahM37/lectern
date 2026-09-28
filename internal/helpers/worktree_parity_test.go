//go:build unix

package helpers

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// The worktree scripts as internal/worktree/interactive.go assembles them.
type wtScripts struct{ single, cancel, preflight, group string }

func worktreeScripts(t *testing.T) wtScripts {
	t.Helper()
	raw := pythonFile(t, "worktree/setup_control.py")
	source, _ := json.Marshal(raw)
	control := "setup_control_source = " + string(source) + "\n" + raw
	return wtScripts{
		single:    control + "\n" + pythonFile(t, "worktree/interactive.py"),
		cancel:    control + "\nimport sys\np=json.loads(sys.argv[2])\ntry:\n SetupControl(p).access(cancel=True)\n print(json.dumps({'workspace':p}))\nexcept (OSError,ValueError,KeyError) as e:\n print(json.dumps({'error':str(e)}));sys.exit(1)",
		preflight: pythonFile(t, "worktree/multi_preflight.py"),
		group:     control + "\n" + pythonFile(t, "worktree/multi_worker.py"),
	}
}

const (
	wtTokA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	wtTokB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	wtTokC = "cccccccccccccccccccccccccccccccc"
	wtTokD = "dddddddddddddddddddddddddddddddd"
	wtTokE = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

// wtRepo makes a repository whose commit hash does not depend on when the
// test runs, so both copies of a fixture agree.
func wtRepo(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "."},
		{"-c", "user.name=Fixture", "-c", "user.email=f@example.invalid", "commit", "-qm", "base"},
	} {
		if args[0] == "add" {
			os.WriteFile(filepath.Join(dir, "file"), []byte("base\n"), 0o644)
		}
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
}

func wtSinglePlan(root, extra string) string {
	return `{"repo":"` + root + `/repo-a","path":"` + root + `/work/session1","branch":"lec/one","base":"HEAD","commit":"","token":"` + wtTokA + `","state":"creating"` + extra + `}`
}

func wtChildPlan(root, name, token string) string {
	return `{"repo":"` + root + `/repo-` + name + `","path":"` + root + `/work/group/` + name + `","branch":"lec/group","base":"HEAD","commit":"","token":"` + token + `","state":"creating"}`
}

func wtGroupPlan(root, extra string, names ...string) string {
	tokens := map[string]string{"a": wtTokB, "b": wtTokC, "c": wtTokD}
	var repos []string
	for _, n := range names {
		repos = append(repos, `{"name":"`+n+`","worktree":`+wtChildPlan(root, n, tokens[n])+`}`)
	}
	return `{"repo":"` + root + `/repo-a","path":"` + root + `/work/group","branch":"lec/group","base":"HEAD","commit":"","token":"` + wtTokA + `","state":"creating","repositories":[` + strings.Join(repos, ",") + `]` + extra + `}`
}

var (
	wtLockIdentity = regexp.MustCompile(`"operation_lock": \[\d+, \d+\]`)
	wtReceipt      = regexp.MustCompile(`"pgid": \d+, "starttime": "\d+"`)
)

// wtNormal makes one fixture's output comparable with its twin's.
func wtNormal(s, root string) string {
	s = wtReceipt.ReplaceAllString(strings.ReplaceAll(s, root, "$ROOT"), `"pgid": PID, "starttime": "START"`)
	return wtLockIdentity.ReplaceAllString(s, `"operation_lock": [DEV, INO]`)
}

// wtStep is one helper call, in both forms.
type wtStep struct {
	label    string
	py       func(s wtScripts, root string) (string, []string)
	goName   string
	goArgs   func(root string) []string
	setup    func(t *testing.T, root string) // before this step, on each copy
	wantFail bool
}

// wtTwins runs the same steps in a Python copy and a Go copy of the
// fixture, comparing each step's output and exit status and, at the end,
// every Lectern record the steps left.
func wtTwins(t *testing.T, fixture func(t *testing.T, root string), steps ...wtStep) {
	t.Helper()
	requirePython(t)
	requireGit(t)
	s := worktreeScripts(t)
	pyRoot, goRoot := t.TempDir(), t.TempDir()
	pyRoot, _ = filepath.EvalSymlinks(pyRoot)
	goRoot, _ = filepath.EvalSymlinks(goRoot)
	fixture(t, pyRoot)
	fixture(t, goRoot)
	spec := runSpec{env: []string{"TMUX=", "TMUX_TMPDIR=" + t.TempDir()}}
	for _, step := range steps {
		if step.setup != nil {
			step.setup(t, pyRoot)
			step.setup(t, goRoot)
		}
		script, pyArgs := step.py(s, pyRoot)
		py := runPy(t, spec, script, pyArgs...)
		goRes := runGo(t, spec, step.goName, step.goArgs(goRoot)...)
		py.stdout, goRes.stdout = wtNormal(py.stdout, pyRoot), wtNormal(goRes.stdout, goRoot)
		if py.stdout != goRes.stdout || py.rc != goRes.rc {
			t.Fatalf("%s: output differs\npython (rc %d): %s\nstderr: %s\ngo     (rc %d): %s\nstderr: %s",
				step.label, py.rc, py.stdout, py.stderr, goRes.rc, goRes.stdout, goRes.stderr)
		}
		if (py.rc != 0) != step.wantFail {
			t.Fatalf("%s: rc %d: %s %s", step.label, py.rc, py.stdout, py.stderr)
		}
	}
	if a, b := wtRecords(t, pyRoot), wtRecords(t, goRoot); a != b {
		t.Fatalf("records differ\npython:\n%s\ngo:\n%s", a, b)
	}
}

// wtRecords lists the Lectern records under root, normalized.
func wtRecords(t *testing.T, root string) string {
	var b strings.Builder
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		name := info.Name()
		if info.IsDir() && name == "objects" {
			return filepath.SkipDir
		}
		if strings.HasPrefix(name, ".lectern-") || strings.HasPrefix(name, ".agentdeck-") || name == "lectern-owner" {
			rel, _ := filepath.Rel(root, path)
			data, _ := os.ReadFile(path)
			if strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, "-lock") {
				data = nil
			}
			b.WriteString(rel + " " + info.Mode().String() + " " + wtNormal(string(data), root) + "\n")
		}
		return nil
	})
	return b.String()
}

func single(label, op string, extra func(root string) string, wantFail bool) wtStep {
	plan := func(root string) string {
		if extra == nil {
			return wtSinglePlan(root, "")
		}
		return wtSinglePlan(root, extra(root))
	}
	return wtStep{
		label: label,
		py: func(s wtScripts, root string) (string, []string) {
			return s.single, []string{op, plan(root), "-", "60"}
		},
		goName:   "worktree",
		goArgs:   func(root string) []string { return []string{op, plan(root), "-", "60"} },
		wantFail: wantFail,
	}
}

func group(label, op string, plan func(root string) string, wantFail bool) wtStep {
	return wtStep{
		label: label,
		py: func(s wtScripts, root string) (string, []string) {
			return s.group, []string{op, plan(root), s.preflight, s.single, "60"}
		},
		goName:   "worktree-group",
		goArgs:   func(root string) []string { return []string{op, plan(root), "60"} },
		wantFail: wantFail,
	}
}

func preflight(label string, plan func(root string) string, wantFail bool) wtStep {
	return wtStep{
		label: label,
		py: func(s wtScripts, root string) (string, []string) {
			return s.preflight, []string{"check-create", plan(root)}
		},
		goName:   "worktree-preflight",
		goArgs:   func(root string) []string { return []string{"check-create", plan(root)} },
		wantFail: wantFail,
	}
}

func cancel(label string, plan func(root string) string, wantFail bool) wtStep {
	return wtStep{
		label:    label,
		py:       func(s wtScripts, root string) (string, []string) { return s.cancel, []string{"cancel", plan(root)} },
		goName:   "worktree-cancel",
		goArgs:   func(root string) []string { return []string{"cancel", plan(root)} },
		wantFail: wantFail,
	}
}

func twoRepos(t *testing.T, root string) {
	wtRepo(t, filepath.Join(root, "repo-a"))
	wtRepo(t, filepath.Join(root, "repo-b"))
	wtRepo(t, filepath.Join(root, "repo-c"))
}

func TestWorktreeSingleParity(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		wtTwins(t, twoRepos,
			single("create", "create", nil, false),
			single("again", "create", nil, true),
			single("status is not a single-checkout operation", "status", nil, true))
	})
	t.Run("refusals", func(t *testing.T) {
		wtTwins(t, twoRepos,
			single("dash base", "create", func(string) string { return `,"base":"-x"` }, true),
			single("missing base", "create", func(string) string { return `,"base":"nope"` }, true),
			single("bad branch", "create", func(string) string { return `,"branch":"bad..name"` }, true),
			single("fresh control token", "create", func(string) string { return `,"control_token":"` + wtTokE + `"` }, false),
			single("bad token", "create", func(string) string { return `,"control_token":"XYZ"` }, true))
	})
	t.Run("setup command", func(t *testing.T) {
		wtTwins(t, twoRepos,
			single("failing setup", "create", func(string) string {
				return `,"setup_command":"echo out; echo err >&2; printf '\\xff'; exit 3","setup_env":{"LEC_X":"1"}`
			}, true))
	})
	t.Run("setup succeeds", func(t *testing.T) {
		wtTwins(t, twoRepos,
			single("setup", "create", func(string) string {
				return `,"setup_command":"echo \"$LEC_X\" > made","setup_env":{"LEC_X":"value"}`
			}, false))
	})
	t.Run("cancel", func(t *testing.T) {
		wtTwins(t, twoRepos,
			cancel("cancel before create", func(root string) string { return wtSinglePlan(root, "") }, false),
			single("create after cancel", "create", nil, true),
			cancel("bad token", func(root string) string { return wtSinglePlan(root, `,"control_token":"nothex"`) }, true),
			cancel("missing path", func(string) string { return `{"token":"` + wtTokA + `","repo":""}` }, true))
	})
	t.Run("symlinked record", func(t *testing.T) {
		link := func(t *testing.T, root string) {
			os.MkdirAll(filepath.Join(root, "work"), 0o755)
			os.WriteFile(filepath.Join(root, "victim"), []byte("untouched"), 0o644)
			os.Symlink(filepath.Join(root, "victim"), filepath.Join(root, "work", ".lectern-setup-"+wtTokA+".json"))
		}
		wtTwins(t, func(t *testing.T, root string) { twoRepos(t, root); link(t, root) },
			cancel("cancel", func(root string) string { return wtSinglePlan(root, "") }, true),
			single("create", "create", nil, true))
	})
	t.Run("foreign record", func(t *testing.T) {
		wtTwins(t, func(t *testing.T, root string) {
			twoRepos(t, root)
			os.MkdirAll(filepath.Join(root, "work"), 0o755)
			os.WriteFile(filepath.Join(root, "work", ".lectern-setup-"+wtTokA+".json"),
				[]byte(`{"token": "`+wtTokA+`", "path": "/elsewhere", "repo": "", "cancelled": false}`), 0o600)
		}, single("create", "create", nil, true), cancel("cancel", func(root string) string { return wtSinglePlan(root, "") }, true))
	})
}

func TestWorktreeSingleRemovalParity(t *testing.T) {
	testutil.RequireIsolated(t)
	dirty := func(t *testing.T, root string) {
		os.WriteFile(filepath.Join(root, "work", "session1", "untracked"), []byte("x"), 0o644)
	}
	wtTwins(t, twoRepos,
		single("create", "create", nil, false),
		single("check-recover", "check-recover", nil, false),
		single("recover", "recover", nil, false),
		single("check-remove", "check-remove", nil, false),
		wtStep(func() wtStep { s := single("dirty", "remove", nil, true); s.setup = dirty; return s }()),
		wtStep(func() wtStep {
			s := single("clean", "remove", nil, false)
			s.setup = func(t *testing.T, root string) { os.Remove(filepath.Join(root, "work", "session1", "untracked")) }
			return s
		}()),
		single("removed", "remove", nil, false),
		single("ownership", "check-remove", func(string) string { return `,"token":"` + wtTokB + `"` }, false))
	t.Run("ownership mismatch", func(t *testing.T) {
		wtTwins(t, twoRepos,
			single("create", "create", nil, false),
			single("other owner", "check-remove", func(string) string { return `,"token":"` + wtTokB + `"` }, true),
			single("other branch", "check-remove", func(string) string { return `,"branch":"lec/two"` }, true))
	})
}

func TestWorktreePreflightParity(t *testing.T) {
	wtTwins(t, twoRepos,
		preflight("ok", func(root string) string { return wtGroupPlan(root, "", "a", "b") }, false),
		preflight("none", func(root string) string { return wtGroupPlan(root, "") }, true),
		preflight("same repo twice", func(root string) string {
			return strings.Replace(wtGroupPlan(root, "", "a", "b"), "/repo-b\"", "/repo-a\"", 1)
		}, true),
		preflight("duplicate token", func(root string) string {
			return strings.Replace(wtGroupPlan(root, "", "a", "b"), wtTokC, wtTokB, 1)
		}, true),
		preflight("not json", func(string) string { return "{" }, true),
		preflight("missing key", func(string) string { return `{"repositories":[{}]}` }, true))
}

func TestWorktreeGroupParity(t *testing.T) {
	planAB := func(root string) string { return wtGroupPlan(root, "", "a", "b") }
	wtTwins(t, twoRepos,
		group("create", "create", planAB, false),
		group("status", "status", planAB, false),
		group("create again", "create", planAB, true),
		group("unknown", "frobnicate", planAB, true),
		group("extend without identity", "extend", func(root string) string { return wtGroupPlan(root, "", "a", "b", "c") }, true),
		group("extend", "extend", func(root string) string {
			return wtGroupPlan(root, `,"control_token":"`+wtTokE+`"`, "a", "b", "c")
		}, false),
		group("status after extend", "status", planAB, false),
		group("mismatched status", "status", func(root string) string { return wtGroupPlan(root, "", "b") }, true))
}

func TestWorktreeGroupReceiptParity(t *testing.T) {
	planAB := func(root string) string { return wtGroupPlan(root, "", "a", "b") }
	live := func(t *testing.T, root string) {
		// This test's own process group is alive and not a zombie.
		os.WriteFile(filepath.Join(root, "work", "group", ".lectern-process.json"),
			[]byte(`{"pgid": `+strconv.Itoa(syscall.Getpgrp())+`, "starttime": null, "boot_id": null}`), 0o600)
	}
	replaced := func(t *testing.T, root string) {
		lock := filepath.Join(root, "work", "group", ".lectern-lock")
		os.Remove(lock)
		os.WriteFile(lock, nil, 0o600)
	}
	invalid := func(t *testing.T, root string) {
		os.WriteFile(filepath.Join(root, "work", "group", ".lectern-process.json"), []byte(`{"pgid": true}`), 0o600)
	}
	wtTwins(t, twoRepos,
		group("create", "create", planAB, false),
		wtStep(func() wtStep { s := group("busy status", "status", planAB, false); s.setup = live; return s }()),
		wtStep(func() wtStep { s := group("invalid receipt", "status", planAB, false); s.setup = invalid; return s }()),
		wtStep(func() wtStep { s := group("replaced lock", "status", planAB, true); s.setup = replaced; return s }()))
}

func TestWorktreeGroupRemovalParity(t *testing.T) {
	testutil.RequireIsolated(t)
	planAB := func(root string) string { return wtGroupPlan(root, "", "a", "b") }
	extraFile := func(t *testing.T, root string) {
		os.WriteFile(filepath.Join(root, "work", "group", "stray"), []byte("x"), 0o644)
	}
	wtTwins(t, twoRepos,
		group("create", "create", planAB, false),
		group("check-remove", "check-remove", planAB, false),
		group("recover", "recover", planAB, false),
		wtStep(func() wtStep { s := group("stray file", "remove", planAB, true); s.setup = extraFile; return s }()),
		wtStep(func() wtStep {
			s := group("remove", "remove", planAB, false)
			s.setup = func(t *testing.T, root string) { os.Remove(filepath.Join(root, "work", "group", "stray")) }
			return s
		}()),
		group("mismatch", "remove", func(root string) string { return wtGroupPlan(root, "", "a") }, true))
}
