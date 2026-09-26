package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

func TestAutonomyOverlayCommandReceiptsAndFailures(t *testing.T) {
	root := t.TempDir()
	base, candidate, output := filepath.Join(root, "base"), filepath.Join(root, "candidate"), filepath.Join(root, "output")
	for _, dir := range []string{base, candidate} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"WORKSHOP.md": "original", "implementation.py": "answer = 42\n"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(candidate, "WORKSHOP.md"), []byte("corrected provenance"), 0644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := autonomyOverlayCommand("autonomy-overlay", []string{base, candidate, output}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	var receipt autonomy.CompletionOverlayReceipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ path, want string }{{base, receipt.BaselineSHA256}, {output, receipt.DerivedSHA256}} {
		stdout.Reset()
		stderr.Reset()
		if code := autonomyOverlayCommand("autonomy-overlay-inspect", []string{check.path}, &stdout, &stderr); code != 0 {
			t.Fatalf("inspect: %d %s", code, stderr.String())
		}
		var got map[string]string
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["tree_sha256"] != check.want || len(check.want) != 64 {
			t.Fatalf("digest mismatch: %v want %s", got, check.want)
		}
	}
	if err := os.WriteFile(filepath.Join(candidate, "implementation.py"), []byte("answer = 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := autonomyOverlayCommand("autonomy-overlay", []string{base, candidate, filepath.Join(root, "rejected")}, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
		t.Fatalf("invalid candidate code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "rejected")); !os.IsNotExist(err) {
		t.Fatalf("rejected artifact exists: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := autonomyOverlayCommand("autonomy-overlay", []string{filepath.Join(root, "missing"), candidate, output}, &stdout, &stderr); code != 1 {
		t.Fatalf("missing baseline should be operational: %d %s", code, stderr.String())
	}
}

func TestAutonomyOverlayInspectRejectsLinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "leak")); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := autonomyOverlayCommand("autonomy-overlay-inspect", []string{root}, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
		t.Fatalf("link accepted: %d %s %s", code, stdout.String(), stderr.String())
	}
}
