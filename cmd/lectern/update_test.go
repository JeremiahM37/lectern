package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakeRelease(t *testing.T, tag, program string, corrupt bool) *httptest.Server {
	t.Helper()
	archive := fmt.Sprintf("lectern_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "lectern", Mode: 0o755, Size: int64(len(program)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(program))
	_ = tw.Close()
	_ = gz.Close()
	data := buf.Bytes()
	sum := sha256.Sum256(data)
	if corrupt {
		sum[0] ^= 0xff
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"tag_name":%q}`, tag) })
	mux.HandleFunc("/"+archive, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) })
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), archive)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testUpdater(t *testing.T, srv *httptest.Server, exe string) (*updater, *bytes.Buffer) {
	var out bytes.Buffer
	return &updater{exe: exe, latestURL: srv.URL + "/latest.json", releaseURL: srv.URL,
		goos: runtime.GOOS, arch: runtime.GOARCH, client: srv.Client(), out: &out}, &out
}

func TestUpdateReplacesTheRunningProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test program is a shell script")
	}
	srv := fakeRelease(t, "v9.9.9", "#!/bin/sh\necho 9.9.9\n", false)
	// The desktop installer's layout: ~/.local/bin/lectern is a wrapper
	// script that execs ~/.local/lib/lectern/client, so the running program
	// is the client. It is replaced; the wrapper is not touched.
	home := t.TempDir()
	client := filepath.Join(home, ".local", "lib", "lectern", "client")
	wrapper := filepath.Join(home, ".local", "bin", "lectern")
	for path, body := range map[string]string{client: "old client", wrapper: "#!/bin/sh\nexec " + client + " \"$@\"\n"} {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	u, out := testUpdater(t, srv, client)
	updated, err := u.run(t.Context(), "2.6.2", "", false)
	if err != nil || !updated {
		t.Fatalf("update: %v %v\n%s", updated, err, out)
	}
	if got, _ := os.ReadFile(client); !strings.Contains(string(got), "echo 9.9.9") {
		t.Fatalf("client not replaced: %q", got)
	}
	if got, _ := os.ReadFile(wrapper); !strings.Contains(string(got), "exec "+client) {
		t.Fatalf("wrapper changed: %q", got)
	}
	// Already current: nothing to do.
	if updated, err := u.run(t.Context(), "9.9.9", "", false); err != nil || updated || !strings.Contains(out.String(), "is the latest release") {
		t.Fatalf("up to date: %v %v %s", updated, err, out)
	}
}

func TestUpdateRefusesABadDownload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test program is a shell script")
	}
	exe := filepath.Join(t.TempDir(), "lectern")
	_ = os.WriteFile(exe, []byte("old"), 0o755)
	u, _ := testUpdater(t, fakeRelease(t, "v9.9.9", "#!/bin/sh\necho 9.9.9\n", true), exe)
	if _, err := u.run(t.Context(), "2.6.2", "", false); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("bad checksum: %v", err)
	}
	u, _ = testUpdater(t, fakeRelease(t, "v9.9.9", "#!/bin/sh\nexit 3\n", false), exe)
	if _, err := u.run(t.Context(), "2.6.2", "", false); err == nil || !strings.Contains(err.Error(), "did not run") {
		t.Fatalf("broken program: %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatalf("a failed update changed the program: %q", got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(exe)); len(entries) != 1 {
		t.Fatalf("a failed update left files behind: %v", entries)
	}
}

func TestUpdateCheckOnlyAndPackageManagers(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "lectern")
	u, out := testUpdater(t, fakeRelease(t, "v9.9.9", "x", false), exe)
	if updated, err := u.run(t.Context(), "2.6.2", "", true); err != nil || updated || !strings.Contains(out.String(), "v9.9.9 is available") {
		t.Fatalf("--check: %v %v %s", updated, err, out)
	}
	if tool, cmd := managedBy("/opt/homebrew/Caskroom/lectern/2.6.2/lectern"); tool != "Homebrew" || !strings.Contains(cmd, "brew upgrade") {
		t.Fatalf("homebrew: %q %q", tool, cmd)
	}
}
