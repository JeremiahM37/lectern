package api

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// These tests use direct handlers and temporary files/SQLite only. No real
// runner, model invocation, host tmux, or task scheduler is involved.
func autoTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Server{DB: db, Auth: &auth.Resolver{Mode: auth.ModeTailscale}}
}

func TestAutonomyAgentsCannotChangeHumanToggle(t *testing.T) {
	s := &Server{Auth: &auth.Resolver{Mode: auth.ModeTailscale}}
	for _, tc := range []struct {
		name, method, path, body string
		handler                  http.HandlerFunc
	}{
		{"enable", "PUT", "/api/autonomy", `{"enabled":true}`, s.putAutonomy},
		{"disable", "PUT", "/api/autonomy", `{"enabled":false}`, s.putAutonomy},
		{"run", "POST", "/api/autonomy/run", `{}`, s.startAutonomy},
		{"stop", "POST", "/api/autonomy/stop", `{}`, s.stopAutonomy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("Tailscale-User-Login", "owner@example.invalid")
			r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindLocal}))
			w := httptest.NewRecorder()
			tc.handler(w, r)
			if w.Code != 403 {
				t.Fatalf("non-human request accepted: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestAutonomyHumanStopPersistsWithoutStartingWork(t *testing.T) {
	s := autoTestServer(t)
	a, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	a.Config.Enabled = true
	a.State, _ = autonomy.NewState("2026-09-24")
	if err = s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/autonomy/stop", strings.NewReader(`{}`))
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindTailscale, Human: true}))
	w := httptest.NewRecorder()
	s.stopAutonomy(w, r)
	if w.Code != 200 {
		t.Fatalf("stop: %d %s", w.Code, w.Body.String())
	}
	got, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Enabled || got.Status != "off" || got.State.Phase != autonomy.Paused || len(got.Jobs) != 0 {
		t.Fatalf("stop not persisted: %+v", got)
	}
}

func TestAutonomyHumanCannotRunDisabledDay(t *testing.T) {
	s := autoTestServer(t)
	r := httptest.NewRequest("POST", "/api/autonomy/run", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindTailscale, Human: true}))
	w := httptest.NewRecorder()
	s.startAutonomy(w, r)
	if w.Code != 409 {
		t.Fatalf("disabled run: %d", w.Code)
	}
}

func TestAutonomyAuditorsDoNotSeeEachOthersVotes(t *testing.T) {
	s := &Server{}
	state, err := autonomy.NewState("2026-09-24")
	if err != nil {
		t.Fatal(err)
	}
	state.Phase = autonomy.Audit
	approved := true
	state.Audits["auditor_a"] = autonomy.Verdict{Approve: &approved, Reason: "OTHER_AUDITOR_PRIVATE_VERDICT"}
	state.Reports[12] = json.RawMessage(`{"approve":true,"reason":"OTHER_AUDITOR_PRIVATE_REPORT"}`)
	a := &autoRecord{Config: autonomy.DefaultConfig(), State: state}
	prompt := s.autoPrompt(context.Background(), a, "auditor_b", &store.Project{Name: "test"})
	if strings.Contains(prompt, "OTHER_AUDITOR_PRIVATE") {
		t.Fatal("auditor B was shown auditor A's verdict")
	}
	if len(state.Audits) != 1 || len(state.Reports) != 1 {
		t.Fatal("masking deleted durable audit history")
	}
}

func TestAutonomyReceiptCannotDispatchThroughNormalTaskAPI(t *testing.T) {
	s := autoTestServer(t)
	target, err := s.DB.InsertTarget(&store.Target{Name: "test", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.DB.InsertProject(&store.Project{Name: "test", TargetID: target.ID, RepoPath: "/unused"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.DB.InsertTask(&store.Task{ProjectID: project.ID, Title: "receipt", Status: "backlog", CreatedBy: autoOwner})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		r := httptest.NewRequest(method, "/api/tasks/1/dispatch", nil)
		r.SetPathValue("id", fmt.Sprint(task.ID))
		w := httptest.NewRecorder()
		if _, ok := s.taskParam(w, r); ok || w.Code != 409 {
			t.Fatalf("receipt mutation %s allowed: %d", method, w.Code)
		}
	}
}

func TestAutonomyCorruptPersistenceFailsClosed(t *testing.T) {
	s := autoTestServer(t)
	if err := s.DB.SetSetting(autoKey, `{"config":{"enabled":true},`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.loadAuto(); err == nil {
		t.Fatal("corrupt enabled state loaded")
	}
}

func autoTestTar(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg && h.Size > 0 {
			if _, err := tw.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAutonomyArchiveRejectsUnsafeEntries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header *tar.Header
	}{
		{"parent", &tar.Header{Name: "../outside", Mode: 0600, Typeflag: tar.TypeReg, Size: 1}},
		{"absolute", &tar.Header{Name: "/outside", Mode: 0600, Typeflag: tar.TypeReg, Size: 1}},
		{"git hook", &tar.Header{Name: ".git/hooks/post-checkout", Mode: 0755, Typeflag: tar.TypeReg, Size: 1}},
		{"symlink", &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc"}},
		{"hardlink", &tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "../outside"}},
		{"fifo", &tar.Header{Name: "pipe", Typeflag: tar.TypeFifo}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := autoExtract(bytes.NewReader(autoTestTar(t, tc.header)), t.TempDir()); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	root := t.TempDir()
	dest := filepath.Join(root, "work")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "link")); err != nil {
		t.Fatal(err)
	}
	data := autoTestTar(t, &tar.Header{Name: "link/pwned", Mode: 0600, Typeflag: tar.TypeReg, Size: 1})
	if err := autoExtract(bytes.NewReader(data), dest); err == nil {
		t.Fatal("existing symlink escaped extraction root")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned")); !os.IsNotExist(err) {
		t.Fatal("wrote outside extraction root")
	}
}

func TestAutonomyArchiveRetainsOnlySafePermissions(t *testing.T) {
	root := t.TempDir()
	data := autoTestTar(t, &tar.Header{Name: "src/test.sh", Mode: 04777, Typeflag: tar.TypeReg, Size: 3})
	if err := autoExtract(bytes.NewReader(data), root); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "src/test.sh")
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0022 != 0 || info.Mode()&os.ModeSetuid != 0 {
		t.Fatalf("unsafe permissions: %v", info.Mode())
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "xxx" {
		t.Fatalf("content %q: %v", b, err)
	}
}

func TestAutonomyReportReaderRejectsLinksAndOversize(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "report.json")
	if err := os.WriteFile(p, []byte(`{"approve":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if b, err := autoReadRegular(p, 100); err != nil || !json.Valid(b) {
		t.Fatalf("valid report: %q %v", b, err)
	}
	if _, err := autoReadRegular(p, 2); err == nil {
		t.Fatal("oversize report accepted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if _, err := autoReadRegular(link, 100); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := autoReadRegular(root, 100); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestAutonomyProxyRejectsPrivateAndMappedAddresses(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.2.1", "100.100.100.100", "169.254.169.254", "0.0.0.0", "224.0.0.1", "::1", "::ffff:127.0.0.1", "::ffff:100.100.100.100", "fc00::1", "fe80::1", "64:ff9b::a00:1", "2002:7f00:1::1"} {
		if autoPublicIP(netip.MustParseAddr(s)) {
			t.Errorf("private/special address accepted: %s", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !autoPublicIP(netip.MustParseAddr(s)) {
			t.Errorf("public address rejected: %s", s)
		}
	}
	for _, addr := range []string{"127.0.0.1:443", "[::1]:80", "8.8.8.8:22"} {
		if conn, err := autoPublicDial(context.Background(), "tcp", addr); err == nil {
			conn.Close()
			t.Errorf("dial accepted forbidden destination: %s", addr)
		}
	}
}

func TestAutonomyReadBridgeRefusesMutationAndBadPaths(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		method, path string
		code         int
	}{
		{"POST", "/tasks", 405}, {"DELETE", "/grimoire/read?path=note.md", 405},
		{"GET", "/api/settings", 404}, {"GET", "/grimoire/search", 400},
		{"GET", "/grimoire/read?path=../secret.md", 400},
		{"GET", "/grimoire/read?path=%2Fetc%2Fsecret.md", 400},
		{"GET", "/grimoire/read?path=note.md%3Fadmin=true", 400},
		{"GET", "/grimoire/read?path=note.md%00", 400},
	} {
		w := httptest.NewRecorder()
		s.autoReadBridge(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.code {
			t.Errorf("%s %s: got %d want %d", tc.method, tc.path, w.Code, tc.code)
		}
	}
}
