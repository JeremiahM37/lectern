package effects

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// git runs a git command in dir with no user or system config, so the tests
// never read or write the developer's settings.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.test", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes a work clone on main with a bare origin that already has the
// first commit pushed and tracked.
func newRepo(t *testing.T) (work, remote string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	work = filepath.Join(root, "work")
	git(t, root, "init", "-q", "--bare", "-b", "main", remote)
	git(t, root, "init", "-q", "-b", "main", work)
	writeFile(t, filepath.Join(work, "a.txt"), "one\n")
	git(t, work, "add", "a.txt")
	git(t, work, "commit", "-q", "-m", "first")
	git(t, work, "remote", "add", "origin", remote)
	git(t, work, "push", "-q", "-u", "origin", "main")
	return work, remote
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitFile(t *testing.T, work, name, msg string) {
	t.Helper()
	writeFile(t, filepath.Join(work, name), msg+"\n")
	git(t, work, "add", name)
	git(t, work, "commit", "-q", "-m", msg)
}

var bg = context.Background()

func TestProbePushed(t *testing.T) {
	ex := executor.NewLocal()
	pushed := Claim{Kind: KindPushed}

	t.Run("head equals remote", func(t *testing.T) {
		work, _ := newRepo(t)
		v := Probe(bg, ex, work, pushed, Facts{})
		if v.Status != StatusVerified {
			t.Fatalf("got %+v, want verified", v)
		}
	})

	t.Run("unpushed commit is contradicted", func(t *testing.T) {
		work, _ := newRepo(t)
		commitFile(t, work, "b.txt", "second")
		head := git(t, work, "rev-parse", "--short=7", "HEAD")
		remoteSHA := git(t, work, "rev-parse", "--short=7", "origin/main")
		v := Probe(bg, ex, work, pushed, Facts{})
		if v.Status != StatusContradicted {
			t.Fatalf("got %+v, want contradicted", v)
		}
		want := "HEAD " + head + " is not on origin/main (remote at " + remoteSHA + ")"
		if v.Evidence != want {
			t.Fatalf("evidence %q, want %q", v.Evidence, want)
		}
	})

	t.Run("ancestor of remote is verified once the commit is fetched", func(t *testing.T) {
		work, remote := newRepo(t)
		other := filepath.Join(filepath.Dir(work), "other")
		git(t, filepath.Dir(work), "clone", "-q", remote, other)
		commitFile(t, other, "c.txt", "from elsewhere")
		git(t, other, "push", "-q", "origin", "main")

		// The remote commit is not in the local object store yet, so the probe
		// cannot tell "behind" from "diverged" and must say unknown.
		if v := Probe(bg, ex, work, pushed, Facts{}); v.Status != StatusUnknown {
			t.Fatalf("before fetch got %+v, want unknown", v)
		}
		git(t, work, "fetch", "-q", "origin")
		if v := Probe(bg, ex, work, pushed, Facts{}); v.Status != StatusVerified {
			t.Fatalf("after fetch got %+v, want verified", v)
		}
	})

	t.Run("no upstream is unknown", func(t *testing.T) {
		work, _ := newRepo(t)
		git(t, work, "checkout", "-q", "-b", "feature")
		if v := Probe(bg, ex, work, pushed, Facts{}); v.Status != StatusUnknown {
			t.Fatalf("got %+v, want unknown", v)
		}
	})

	t.Run("unreachable remote is unknown", func(t *testing.T) {
		work, _ := newRepo(t)
		git(t, work, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
		if v := Probe(bg, ex, work, pushed, Facts{}); v.Status != StatusUnknown {
			t.Fatalf("got %+v, want unknown", v)
		}
	})

	t.Run("remote branch deleted is contradicted", func(t *testing.T) {
		work, remote := newRepo(t)
		git(t, remote, "update-ref", "-d", "refs/heads/main")
		if v := Probe(bg, ex, work, pushed, Facts{}); v.Status != StatusContradicted {
			t.Fatalf("got %+v, want contradicted", v)
		}
	})
}

func TestProbeCommitted(t *testing.T) {
	ex := executor.NewLocal()
	c := Claim{Kind: KindCommitted}

	work, _ := newRepo(t)
	if v := Probe(bg, ex, work, c, Facts{}); v.Status != StatusVerified {
		t.Fatalf("clean tree got %+v, want verified", v)
	}
	writeFile(t, filepath.Join(work, "untracked.txt"), "new\n")
	if v := Probe(bg, ex, work, c, Facts{}); v.Status != StatusVerified {
		t.Fatalf("untracked file only got %+v, want verified", v)
	}
	writeFile(t, filepath.Join(work, "a.txt"), "changed\n")
	v := Probe(bg, ex, work, c, Facts{})
	if v.Status != StatusContradicted || v.Evidence != "1 tracked file has uncommitted changes" {
		t.Fatalf("one dirty file got %+v", v)
	}
	writeFile(t, filepath.Join(work, "a.txt"), "changed again\n")
	git(t, work, "add", "a.txt")
	commitFile(t, work, "b.txt", "two")
	writeFile(t, filepath.Join(work, "b.txt"), "dirty\n")
	writeFile(t, filepath.Join(work, "a.txt"), "dirty too\n")
	if v := Probe(bg, ex, work, c, Facts{}); v.Status != StatusContradicted || !strings.Contains(v.Evidence, "2 tracked files") {
		t.Fatalf("two dirty files got %+v", v)
	}
}

func TestProbeTests(t *testing.T) {
	cases := []struct {
		name string
		f    Facts
		want Status
	}{
		{"no run recorded", Facts{CurrentFingerprint: "abc"}, StatusUnknown},
		{"failed on this tree", Facts{LastCheck: &store.SessionCheck{Command: "go test ./...", Status: "failed", Fingerprint: "abc", OutputTail: "ok\nFAIL foo 0.1s\n"}, CurrentFingerprint: "abc"}, StatusContradicted},
		// The Stop that triggers evaluation also queues a fresh check; until it
		// runs, the latest row is an older tree's and must not contradict.
		{"failed on an earlier tree", Facts{LastCheck: &store.SessionCheck{Command: "go test ./...", Status: "failed", Fingerprint: "old", OutputTail: "FAIL\n"}, CurrentFingerprint: "abc"}, StatusUnknown},
		{"passed on this tree", Facts{LastCheck: &store.SessionCheck{Command: "go test ./...", Status: "passed", Fingerprint: "abc"}, CurrentFingerprint: "abc"}, StatusVerified},
		{"passed then tree changed", Facts{LastCheck: &store.SessionCheck{Command: "go test ./...", Status: "passed", Fingerprint: "abc"}, CurrentFingerprint: "def"}, StatusUnknown},
		{"passed with no fingerprint", Facts{LastCheck: &store.SessionCheck{Command: "go test ./...", Status: "passed"}}, StatusUnknown},
		{"still running", Facts{LastCheck: &store.SessionCheck{Command: "go test ./...", Status: "running"}}, StatusUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if v := Probe(bg, executor.NewLocal(), t.TempDir(), Claim{Kind: KindTestsPass}, tc.f); v.Status != tc.want {
				t.Fatalf("got %+v, want %s", v, tc.want)
			}
		})
	}
}

func TestProbeURL(t *testing.T) {
	ex := executor.NewLocal()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	goneURL := gone.URL
	gone.Close()

	if v := Probe(bg, ex, t.TempDir(), Claim{Kind: KindURLOK, Target: ok.URL}, Facts{}); v.Status != StatusVerified {
		t.Fatalf("200 got %+v", v)
	}
	v := Probe(bg, ex, t.TempDir(), Claim{Kind: KindURLOK, Target: bad.URL}, Facts{})
	if v.Status != StatusContradicted || !strings.Contains(v.Evidence, "HTTP 500") {
		t.Fatalf("500 got %+v", v)
	}
	if v := Probe(bg, ex, t.TempDir(), Claim{Kind: KindURLOK, Target: goneURL}, Facts{}); v.Status != StatusContradicted {
		t.Fatalf("closed server got %+v, want contradicted", v)
	}
	if v := Probe(bg, ex, t.TempDir(), Claim{Kind: KindURLOK, Target: "ftp://x"}, Facts{}); v.Status != StatusUnknown {
		t.Fatalf("non-http got %+v, want unknown", v)
	}
}

func TestProbePort(t *testing.T) {
	if _, err := exec.LookPath("ss"); err != nil {
		t.Skip("ss not installed")
	}
	ex := executor.NewLocal()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strings.Split(l.Addr().String(), ":")[len(strings.Split(l.Addr().String(), ":"))-1]
	if v := Probe(bg, ex, t.TempDir(), Claim{Kind: KindPortListening, Target: port}, Facts{}); v.Status != StatusVerified {
		t.Fatalf("listening port got %+v", v)
	}
	l.Close()
	time.Sleep(50 * time.Millisecond)
	if v := Probe(bg, ex, t.TempDir(), Claim{Kind: KindPortListening, Target: port}, Facts{}); v.Status != StatusContradicted {
		t.Fatalf("closed port got %+v, want contradicted", v)
	}
}

func TestProbeFile(t *testing.T) {
	ex := executor.NewLocal()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "docs.md"), "x")
	if v := Probe(bg, ex, dir, Claim{Kind: KindFileExists, Target: "docs.md"}, Facts{}); v.Status != StatusVerified {
		t.Fatalf("existing got %+v", v)
	}
	if v := Probe(bg, ex, dir, Claim{Kind: KindFileExists, Target: "missing/x.md"}, Facts{}); v.Status != StatusContradicted {
		t.Fatalf("missing got %+v, want contradicted", v)
	}
}

// fakeEx answers each command from a table, so the systemd branches can be
// tested without touching real units.
type fakeEx struct {
	answers map[string]executor.Result
}

func (f fakeEx) Run(_ context.Context, cmd string, _ executor.RunOpts) (executor.Result, error) {
	for key, res := range f.answers {
		if strings.HasPrefix(cmd, key) {
			return res, nil
		}
	}
	return executor.Result{RC: 127, Stderr: "no such fake command: " + cmd}, nil
}
func (f fakeEx) ReadFile(context.Context, string, int64) ([]byte, error) { return nil, nil }
func (f fakeEx) WriteFile(context.Context, string, []byte) error         { return nil }
func (f fakeEx) Close() error                                            { return nil }

func showOut(state, load, entered string) executor.Result {
	return executor.Result{Stdout: "ActiveState=" + state + "\nLoadState=" + load + "\nActiveEnterTimestamp=" + entered + "\n"}
}

func TestProbeRestarted(t *testing.T) {
	since := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	after := "Wed 2026-10-14 11:00:00 UTC"
	before := "Tue 2026-10-13 08:01:02 UTC"
	system := func(res executor.Result) fakeEx {
		return fakeEx{answers: map[string]executor.Result{"systemctl show": res}}
	}
	cases := []struct {
		name string
		ex   fakeEx
		unit string
		want Status
	}{
		{"active after turn", system(showOut("active", "loaded", after)), "lectern.service", StatusVerified},
		{"active before turn", system(showOut("active", "loaded", before)), "lectern.service", StatusContradicted},
		{"failed", system(showOut("failed", "loaded", after)), "lectern.service", StatusContradicted},
		{"inactive", system(showOut("inactive", "loaded", "")), "lectern.service", StatusContradicted},
		{"not loaded anywhere", system(showOut("inactive", "not-found", "")), "nosuch.service", StatusUnknown},
		{"no unit named", system(showOut("active", "loaded", after)), "", StatusUnknown},
		{"user manager fallback", fakeEx{answers: map[string]executor.Result{
			"systemctl --user show": showOut("active", "loaded", after),
			"systemctl show":        showOut("inactive", "not-found", ""),
		}}, "app.service", StatusVerified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := probeRestarted(bg, tc.ex, t.TempDir(), tc.unit, since)
			if v.Status != tc.want {
				t.Fatalf("got %+v, want %s", v, tc.want)
			}
		})
	}
}

func TestParseShow(t *testing.T) {
	out := "ActiveState=active\nLoadState=loaded\nActiveEnterTimestamp=Tue 2026-10-13 08:01:02 UTC\nDescription=a=b\n"
	p := parseShow(out)
	if p["ActiveState"] != "active" || p["ActiveEnterTimestamp"] != "Tue 2026-10-13 08:01:02 UTC" || p["Description"] != "a=b" {
		t.Fatalf("parseShow: %v", p)
	}
	if ts, err := parseSystemdTime(p["ActiveEnterTimestamp"]); err != nil || !ts.Equal(time.Date(2026, 10, 13, 8, 1, 2, 0, time.UTC)) {
		t.Fatalf("parseSystemdTime: %v %v", ts, err)
	}
	if _, err := parseSystemdTime("yesterday"); err == nil {
		t.Fatal("garbage timestamp parsed without error")
	}
}

func TestEvaluateDeduplicatesAndProbes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "out.txt"), "x")
	msg := "Wrote `out.txt`. Wrote `out.txt` again. Tests are green."
	facts := Facts{LastCheck: &store.SessionCheck{Command: "go test ./...", Status: "failed", Fingerprint: "fp", OutputTail: "FAIL\n"}, CurrentFingerprint: "fp"}
	res := Evaluate(bg, executor.NewLocal(), dir, msg, facts)
	if len(res) != 2 {
		t.Fatalf("want 2 results (file once, tests), got %d: %+v", len(res), res)
	}
	if res[0].Claim.Kind != KindFileExists || res[0].Verdict.Status != StatusVerified {
		t.Fatalf("file result %+v", res[0])
	}
	if res[1].Claim.Kind != KindTestsPass || res[1].Verdict.Status != StatusContradicted {
		t.Fatalf("tests result %+v", res[1])
	}
}
