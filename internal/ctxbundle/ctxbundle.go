// Package ctxbundle stages the files a dispatched agent should read before it
// starts.
//
// A dispatched agent opens a FRESH session: no conversation history, and on an
// ssh/pct/sandbox target none of the control plane user's Claude config either.
// The same task therefore runs with far less knowledge remotely than it does
// locally, and nothing in the timeline says so — the output is just quietly
// worse.
//
// Context paths close that gap. The control plane reads the listed files off its
// OWN filesystem (one place to curate) and stages them into the worktree at
// .lectern/context/ through the Executor, so every target kind gets
// byte-identical context.
//
// Caps are deliberate and LOUD: an oversized or missing file is reported in the
// staged index and in the prompt, never dropped silently.
package ctxbundle

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Subdir is where staged files land inside the runtime dir.
const Subdir = "context"

// IndexName is the manifest written alongside the staged files.
const IndexName = "INDEX.md"

// Caps on a single file and on the whole bundle.
var (
	MaxFileBytes  = 256 * 1024
	MaxTotalBytes = 1024 * 1024
)

// File is one staged context file.
type File struct {
	Name   string // flat, collision-free name inside the staging dir
	Source string // where it came from on the control plane
	Data   []byte
}

// Resolve expands glob patterns to existing files, reporting what missed.
func Resolve(patterns []string) ([]string, []string) {
	var files, notes []string
	seen := map[string]bool{}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		matches, _ := filepath.Glob(pattern)
		sort.Strings(matches)
		var hits []string
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && st.Mode().IsRegular() {
				hits = append(hits, m)
			}
		}
		if len(hits) == 0 {
			notes = append(notes, pattern+" — no such file (skipped)")
			continue
		}
		for _, p := range hits {
			key, err := filepath.Abs(p)
			if err != nil {
				key = p
			}
			if !seen[key] {
				seen[key] = true
				files = append(files, p)
			}
		}
	}
	return files, notes
}

// stageNames derives flat, collision-free filenames for the staging dir by
// prefixing parent directory segments until each name is unique.
func stageNames(paths []string) []string {
	names := make([]string, 0, len(paths))
	used := map[string]bool{}
	for _, p := range paths {
		clean := filepath.Clean(p)
		parts := strings.Split(clean, string(filepath.Separator))
		name := parts[len(parts)-1]
		parents := parts[:len(parts)-1]
		for used[name] && len(parents) > 0 {
			name = parents[len(parents)-1] + "_" + name
			parents = parents[:len(parents)-1]
		}
		base, n := name, 2
		for used[name] {
			name = fmt.Sprintf("%s.%d", base, n)
			n++
		}
		used[name] = true
		names = append(names, name)
	}
	return names
}

// Collect reads the configured paths into staged files plus notes about
// everything that was truncated, unreadable or capped out.
func Collect(patterns []string) ([]File, []string) {
	paths, notes := Resolve(patterns)
	names := stageNames(paths)
	out := []File{}
	total := 0
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s — unreadable (%v) (skipped)", path, err))
			continue
		}
		if len(data) > MaxFileBytes {
			data = append(data[:MaxFileBytes],
				[]byte(fmt.Sprintf("\n\n[truncated by lectern at %d bytes]\n", MaxFileBytes))...)
			notes = append(notes, fmt.Sprintf("%s — truncated to %d bytes", path, MaxFileBytes))
		}
		if total+len(data) > MaxTotalBytes {
			notes = append(notes, fmt.Sprintf("%s — bundle hit the %d-byte cap (skipped)",
				path, MaxTotalBytes))
			continue
		}
		total += len(data)
		out = append(out, File{Name: names[i], Source: path, Data: data})
	}
	return out, notes
}

// IndexMarkdown is the manifest staged next to the files, so an agent can see
// where each one came from and what could not be staged.
func IndexMarkdown(files []File, notes []string) string {
	var b strings.Builder
	b.WriteString("# Staged context\n\nFiles copied here by lectern from the control plane at dispatch.\n\n")
	for _, f := range files {
		fmt.Fprintf(&b, "- `%s` — from `%s` (%d bytes)\n", f.Name, f.Source, len(f.Data))
	}
	if len(notes) > 0 {
		b.WriteString("\n## Not staged\n\n")
		for _, n := range notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	return b.String()
}

// PromptPrefix is the header prepended to the task prompt so the agent cannot
// miss the bundle — or the fact that part of it is absent.
func PromptPrefix(files []File, notes []string) string {
	if len(files) == 0 && len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Context\n\nRead these staged files before doing anything else — " +
		"they carry conventions and constraints this task depends on:")
	for _, f := range files {
		fmt.Fprintf(&b, "\n- .lectern/%s/%s", Subdir, f.Name)
	}
	if len(notes) > 0 {
		b.WriteString("\n\nContext that could NOT be staged (work without it, and say so if it blocks you):")
		for _, n := range notes {
			fmt.Fprintf(&b, "\n- %s", n)
		}
	}
	b.WriteString("\n\n---\n\n")
	return b.String()
}
