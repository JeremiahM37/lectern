package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/hooks"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// The update check compares this build with the newest release and says
// whether one is out, reading the release list from LECTERN_RELEASE_BASE.
func TestCheckUpdateFindsANewerRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/latest.json" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"tag_name":"v99.0.0"}`))
	}))
	defer srv.Close()
	t.Setenv("LECTERN_RELEASE_BASE", srv.URL)
	b := &scribeBackend{opts: Options{Version: "2.10.1"}}
	st := b.CheckUpdate()
	if st.Err != "" || st.Latest != "v99.0.0" || !st.Known || !st.Newer {
		t.Fatalf("status = %+v", st)
	}
}

func TestCheckUpdateSaysWhenCurrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v2.10.1"}`))
	}))
	defer srv.Close()
	t.Setenv("LECTERN_RELEASE_BASE", srv.URL)
	st := (&scribeBackend{opts: Options{Version: "2.10.1"}}).CheckUpdate()
	if st.Newer || !st.Known {
		t.Fatalf("status = %+v", st)
	}
}

func TestCheckUpdateReportsAnUnreachableList(t *testing.T) {
	t.Setenv("LECTERN_RELEASE_BASE", "http://127.0.0.1:1")
	st := (&scribeBackend{opts: Options{Version: "2.10.1"}}).CheckUpdate()
	if st.Err == "" || st.Latest != "" || st.Newer {
		t.Fatalf("status = %+v", st)
	}
}

func TestGitCheckStates(t *testing.T) {
	if c := gitCheck(tui.GitStatus{}, "/x"); c.State != "info" {
		t.Fatalf("not a repo = %+v", c)
	}
	if c := gitCheck(tui.GitStatus{Branch: "main"}, "/x"); c.State != "ok" || c.Detail != "branch main" {
		t.Fatalf("clean = %+v", c)
	}
	if c := gitCheck(tui.GitStatus{Branch: "main", Dirty: true}, "/x"); c.State != "warn" {
		t.Fatalf("dirty = %+v", c)
	}
}

func TestDiskCheckReportsFreeSpace(t *testing.T) {
	c := diskCheck(t.TempDir())
	if c.Area != "Disk" || c.Detail == "" {
		t.Fatalf("disk = %+v", c)
	}
}

func TestLSPNameMatchesTheServersView(t *testing.T) {
	if got := lspNameOf(tui.LSPSession{Language: "go", Root: "/work/app"}); got != "go app" {
		t.Fatalf("lspNameOf = %q", got)
	}
}

// The claim wrapper passes updatedInput through, so a rewritten input reaches
// the call. Without this the agent would fall back to PreTool and lose it.
func TestClaimHooksForwardUpdatedInput(t *testing.T) {
	inner := hooks.New(hooks.Options{
		Root:         t.TempDir(),
		SessionID:    "s1",
		ConfigGlobal: []config.Hook{{Event: "PreToolUse", Matcher: "shell", Command: `cat >/dev/null; printf '%s' '{"hookSpecificOutput":{"updatedInput":{"command":"echo safe"}}}'`}},
		UserSettings: filepath.Join(t.TempDir(), "none.json"),
		Trusted:      true,
	})
	h := claimHooks{inner: inner, reg: fileClaims, owner: "me", label: "this tab", workdir: t.TempDir()}
	args := json.RawMessage(`{"command":"rm -rf build"}`)
	got, block := h.PreToolUpdate(context.Background(), "shell", args)
	if block != "" {
		t.Fatalf("block = %q", block)
	}
	if !strings.Contains(string(got), `"echo safe"`) {
		t.Fatalf("updated input was lost: %s", got)
	}
}
