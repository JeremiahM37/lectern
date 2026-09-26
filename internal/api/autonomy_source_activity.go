package api

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const autoActivityLimit = 64 << 10

// Activity is a hint about uncommitted work, never source content or permission.
// Unknown must not be interpreted as a clean checkout.
func autoSourceActivity(ctx context.Context, dir string) map[string]any {
	result := map[string]any{"status": "unknown", "observed_at": time.Now().UTC().Format(time.RFC3339), "scope": "Local working-tree activity only; non-atomic and may overcount files due to omitted local exclusions or attributes. Check /tasks and shared context for ownership; neither clean status nor an empty task list establishes clearance. Existing admissions remain valid."}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// A temporary Git directory prevents status from reading concurrently edited
	// repository configuration. No filter/fsmonitor/hook command is inherited.
	gitDir, cleanup, err := autoActivityGitDir(ctx, dir)
	if err != nil {
		return result
	}
	defer cleanup()
	raw, err := autoActivityOutput(autoSourceCommand(ctx, dir, "--git-dir="+gitDir, "--work-tree="+dir, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "status", "--porcelain=v1", "-z", "--untracked-files=normal", "--ignore-submodules=all"))
	if err != nil {
		return result
	}
	categories, staged, unstaged, untracked, err := autoParseActivity(raw)
	if err != nil {
		return result
	}
	result["status"] = "complete"
	result["categories"] = categories
	result["staged"] = staged
	result["unstaged"] = unstaged
	result["untracked"] = untracked
	return result
}

func autoActivityOutput(cmd *exec.Cmd) ([]byte, error) {
	p, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(p, autoActivityLimit+1))
	if len(data) > autoActivityLimit || readErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errors.New("activity unavailable")
	}
	if err = cmd.Wait(); err != nil {
		return nil, err
	}
	return data, nil
}

func autoParseActivity(raw []byte) (map[string]int, bool, bool, bool, error) {
	categories := map[string]int{}
	staged, unstaged, untracked := false, false, false
	fields := strings.Split(string(raw), "\x00")
	if len(raw) > 0 && fields[len(fields)-1] != "" {
		return nil, false, false, false, errors.New("incomplete activity")
	}
	for i := 0; i < len(fields)-1; i++ {
		entry := fields[i]
		if len(entry) < 4 || entry[2] != ' ' {
			return nil, false, false, false, errors.New("invalid activity")
		}
		code, path := entry[:2], entry[3:]
		categories[autoActivityCategory(path)]++
		if code == "??" {
			untracked = true
		} else {
			staged = staged || code[0] != ' '
			unstaged = unstaged || code[1] != ' '
		}
		if strings.ContainsAny(code, "RC") {
			i++
			if i >= len(fields)-1 {
				return nil, false, false, false, errors.New("incomplete rename")
			}
			oldCategory := autoActivityCategory(fields[i])
			if oldCategory != autoActivityCategory(path) {
				categories[oldCategory]++
			}
		}
	}
	return categories, staged, unstaged, untracked, nil
}

func autoActivityCategory(path string) string {
	p := strings.ToLower(path)
	if strings.HasPrefix(p, "web/") || strings.HasPrefix(p, "frontend/") || strings.HasPrefix(p, "ui/") || strings.HasPrefix(p, "static/") || strings.HasSuffix(p, ".tsx") || strings.HasSuffix(p, ".jsx") {
		return "frontend"
	}
	if strings.HasPrefix(p, "e2e/") || strings.HasPrefix(p, "test") || strings.Contains(p, "/test") || strings.HasSuffix(p, "_test.go") {
		return "tests"
	}
	if strings.HasPrefix(p, "docs/") || strings.HasSuffix(p, ".md") {
		return "documentation"
	}
	if strings.HasPrefix(p, "internal/") || strings.HasPrefix(p, "cmd/") || strings.HasPrefix(p, "server/") || strings.HasPrefix(p, "backend/") {
		return "backend"
	}
	if strings.HasPrefix(p, ".") || strings.Contains(p, "docker") || p == "go.mod" || p == "go.sum" || strings.Contains(p, "package") || p == "makefile" {
		return "build/configuration"
	}
	return "other"
}

// Capture a bounded index with a detached HEAD and an object-directory link.
// Split indexes and unsupported repository layouts fail unknown, never clean.
func autoActivityGitDir(ctx context.Context, dir string) (string, func(), error) {
	query := func(args ...string) (string, error) {
		raw, err := autoActivityOutput(autoSourceCommand(ctx, dir, args...))
		return strings.TrimSpace(string(raw)), err
	}
	head, err := autoSourceRevision(ctx, dir, "HEAD")
	if err != nil {
		return "", nil, err
	}
	format, err := query("rev-parse", "--show-object-format")
	if err != nil || format != "sha1" && format != "sha256" {
		return "", nil, errors.New("unsupported object format")
	}
	repositoryDir, err := query("rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", nil, err
	}
	objects, err := query("rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return "", nil, err
	}
	data, err := autoReadActivityIndex(filepath.Join(repositoryDir, "index"))
	if err != nil {
		return "", nil, err
	}
	stage, err := os.MkdirTemp("", "lectern-source-activity-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(stage) }
	fail := func(e error) (string, func(), error) { cleanup(); return "", nil, e }
	config := "[core]\nrepositoryformatversion = 0\nbare = false\n"
	if format == "sha256" {
		config = "[core]\nrepositoryformatversion = 1\nbare = false\n[extensions]\nobjectformat = sha256\n"
	}
	// Preserve only these boolean filesystem conventions, never arbitrary config.
	for _, key := range []string{"filemode", "ignorecase", "symlinks"} {
		value, e := query("config", "--bool", "--get", "core."+key)
		if e == nil && (value == "true" || value == "false") {
			config += "[core]\n" + key + " = " + value + "\n"
		}
	}
	for name, body := range map[string][]byte{"HEAD": []byte(head + "\n"), "index": data, "config": []byte(config)} {
		if e := os.WriteFile(filepath.Join(stage, name), body, 0600); e != nil {
			return fail(e)
		}
	}
	if e := os.Mkdir(filepath.Join(stage, "refs"), 0700); e != nil {
		return fail(e)
	}
	if e := os.Symlink(objects, filepath.Join(stage, "objects")); e != nil {
		return fail(e)
	}
	return stage, cleanup, nil
}
