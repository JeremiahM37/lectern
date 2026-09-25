// Package install holds tests for install.sh — the curl|sh entry point new
// users run first. It lives at the repo root because that's where the script
// is: a real user's `curl .../install.sh | sh` fetches this exact file.
package install

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestInstallShShellcheckClean lints install.sh with shellcheck's `sh` dialect
// when shellcheck is available. It skips (not fails) when the tool isn't
// installed — CI images that carry it get the check; a laptop without it
// still gets every other test.
func TestInstallShShellcheckClean(t *testing.T) {
	if _, err := exec.LookPath("shellcheck"); err != nil {
		t.Skip("shellcheck not installed")
	}
	out, err := exec.Command("shellcheck", "-s", "sh", "install.sh").CombinedOutput()
	if err != nil {
		t.Fatalf("shellcheck found issues in install.sh:\n%s", out)
	}
}

// TestInstallShAgainstFakeReleaseServer runs the real install.sh end to end
// against a local httptest server standing in for GitHub's release CDN
// (LECTERN_RELEASE_BASE), so this test never touches the network and never
// depends on a real Lectern release existing. It checks the whole path a new
// user's terminal takes: download, checksum verification, install into
// LECTERN_INSTALL_DIR, and a single clear next step at the end.
func TestInstallShAgainstFakeReleaseServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh targets POSIX sh; Windows uses install.ps1")
	}
	for _, tool := range []string{"sh", "curl", "tar", "install"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		t.Skipf("unsupported CPU for this test: %s", arch)
	}
	osName := runtime.GOOS
	archiveName := fmt.Sprintf("lectern_%s_%s.tar.gz", osName, arch)

	work := t.TempDir()
	fakeBin := filepath.Join(work, "lectern")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\necho 'lectern version vTEST (fake)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(work, archiveName)
	if out, err := exec.Command("tar", "czf", archivePath, "-C", work, "lectern").CombinedOutput(); err != nil {
		t.Fatalf("build fake release archive: %v\n%s", err, out)
	}
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archiveBytes)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	mux := http.NewServeMux()
	mux.HandleFunc("/"+archiveName, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archiveBytes)
	})
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("fresh install", func(t *testing.T) {
		installDir := t.TempDir()
		out := runInstallSh(t, srv.URL, installDir)
		installed := filepath.Join(installDir, "lectern")
		if _, err := os.Stat(installed); err != nil {
			t.Fatalf("lectern was not installed at %s: %v\noutput:\n%s", installed, err, out)
		}
		if !strings.Contains(out, "'"+filepath.Join(installDir, "lectern")+" up'") {
			t.Errorf("install.sh should end with one clear next step naming `lectern up`:\n%s", out)
		}
		if !strings.Contains(out, "vTEST") {
			t.Errorf("install.sh should print the installed version:\n%s", out)
		}
	})

	t.Run("idempotent re-run", func(t *testing.T) {
		installDir := t.TempDir()
		runInstallSh(t, srv.URL, installDir)
		out := runInstallSh(t, srv.URL, installDir) // second run, same dir
		installed := filepath.Join(installDir, "lectern")
		if _, err := os.Stat(installed); err != nil {
			t.Fatalf("lectern missing after a second run: %v\noutput:\n%s", err, out)
		}
	})

	t.Run("bad checksum is rejected", func(t *testing.T) {
		badMux := http.NewServeMux()
		badMux.HandleFunc("/"+archiveName, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(archiveBytes)
		})
		badMux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("0000000000000000000000000000000000000000000000000000000000000000  " + archiveName + "\n"))
		})
		badSrv := httptest.NewServer(badMux)
		defer badSrv.Close()
		installDir := t.TempDir()
		cmd := exec.Command("sh", "install.sh")
		cmd.Env = append(os.Environ(),
			"LECTERN_RELEASE_BASE="+badSrv.URL,
			"LECTERN_INSTALL_DIR="+installDir,
			"LECTERN_VERSION=vTEST",
		)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("install.sh should refuse a checksum mismatch:\n%s", out)
		}
		if !strings.Contains(string(out), "checksum mismatch") {
			t.Errorf("expected a checksum mismatch message, got:\n%s", out)
		}
		if _, statErr := os.Stat(filepath.Join(installDir, "lectern")); statErr == nil {
			t.Errorf("a rejected checksum must not leave a binary installed")
		}
	})
}

func runInstallSh(t *testing.T, releaseBase, installDir string) string {
	t.Helper()
	cmd := exec.Command("sh", "install.sh")
	cmd.Env = append(os.Environ(),
		"LECTERN_RELEASE_BASE="+releaseBase,
		"LECTERN_INSTALL_DIR="+installDir,
		"LECTERN_VERSION=vTEST",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	return string(out)
}
