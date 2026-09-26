package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSourceActivityDoesNotRunFiltersOrExposeNames(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("workshop activity inspection is Linux-only")
	}
	_, _, dir := sourceFixture(t)
	ctx := context.Background()
	marker := filepath.Join(t.TempDir(), "ran")
	for _, args := range [][]string{{"config", "core.fsmonitor", "touch " + marker}, {"config", "filter.evil.clean", "touch " + marker}, {"config", "filter.evil.process", "touch " + marker}, {"config", "filter.evil.required", "true"}} {
		if err := autoGit(ctx, dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("sample.txt filter=evil\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("dirty secret text"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "web", "ui"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "ui", "private-name.tsx"), []byte("private UI"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	row := autoSourceActivity(ctx, dir)
	if row["status"] != "complete" || row["unstaged"] != true || row["untracked"] != true {
		t.Fatal(row)
	}
	if row["categories"].(map[string]int)["frontend"] != 1 {
		t.Fatal(row)
	}
	raw, _ := json.Marshal(row)
	for _, secret := range []string{dir, "sample.txt", "private-name", "dirty secret", "private UI"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("path/content exposed: %s", raw)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("external command executed", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil || string(before) != string(after) {
		t.Fatal("index changed", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "sample.txt"))
	if string(body) != "dirty secret text" {
		t.Fatal("working file changed")
	}
}

func TestSourceActivityUnknownAndCategories(t *testing.T) {
	_, _, dir := sourceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if row := autoSourceActivity(ctx, dir); row["status"] != "unknown" {
		t.Fatal(row)
	}
	if row := autoSourceActivity(context.Background(), t.TempDir()); row["status"] != "unknown" {
		t.Fatal(row)
	}
	raw := []byte(" M README.md\x00R  web/ui/new\nname.tsx\x00unknown/old\tname\x00?? strange\nfilename\x00")
	cats, staged, unstaged, untracked, err := autoParseActivity(raw)
	if err != nil || !staged || !unstaged || !untracked || cats["documentation"] != 1 || cats["frontend"] != 1 || cats["other"] != 2 {
		t.Fatal(cats, err)
	}
	cats, _, _, _, err = autoParseActivity([]byte(" M README.md\x00"))
	if err != nil || cats["frontend"] != 0 {
		t.Fatal(cats, err)
	}
	if _, _, _, _, err := autoParseActivity([]byte("R  incomplete\x00")); err == nil {
		t.Fatal("truncated rename accepted")
	}
	// A bounded read is not a clean or partial observation.
	cmd := autoSourceCommand(context.Background(), dir, "config", "--list")
	if err := autoGit(context.Background(), dir, "config", "activity.large", strings.Repeat("x", autoActivityLimit+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := autoActivityOutput(cmd); err == nil {
		t.Fatal("oversized activity accepted")
	}
}

func TestSourceActivitySanitizedConfigSurvivesConcurrentFilterChange(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("workshop activity inspection is Linux-only")
	}
	_, _, dir := sourceFixture(t)
	ctx := context.Background()
	stage, cleanup, err := autoActivityGitDir(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	marker := filepath.Join(t.TempDir(), "executed")
	// Introduce a previously unknown driver AFTER preparing the inspection.
	for _, args := range [][]string{{"config", "filter.late.clean", "touch " + marker}, {"config", "filter.late.process", "touch " + marker}, {"config", "core.fsmonitor", "touch " + marker}} {
		if err := autoGit(ctx, dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("sample.txt filter=late\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("changed after capture"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := autoActivityOutput(autoSourceCommand(ctx, dir, "--git-dir="+stage, "--work-tree="+dir, "status", "--porcelain=v1", "-z", "--untracked-files=normal", "--ignore-submodules=all"))
	if err != nil || !strings.Contains(string(raw), "sample.txt") {
		t.Fatal(string(raw), err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("concurrently introduced command executed", err)
	}
}
