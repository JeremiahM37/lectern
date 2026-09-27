package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/pluginpkg"
	"gopkg.in/yaml.v3"
)

// Source says where a plugin comes from. Exactly one of Path or URL is set;
// Index names the marketplace an id was found in.
type Source struct {
	Kind   string `json:"kind"` // path | git | index
	Path   string `json:"path,omitempty"`
	URL    string `json:"url,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
	Subdir string `json:"subdir,omitempty"`
	Index  string `json:"index,omitempty"`
	ID     string `json:"id,omitempty"`
}

// fetched is a plugin's files and, for git, what they were read at.
type fetched struct {
	Files  []pluginpkg.File
	Commit string
	Tree   string
}

var scpLike = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._/~-]+$`)

// CheckGitURL admits https, ssh and scp-like git URLs, file:// for local
// testing, and http only to this machine. git's ext:: and fd:: transports run
// programs, and a plain-http fetch from elsewhere could be changed in flight.
func CheckGitURL(raw string) error {
	if scpLike.MatchString(raw) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return fmt.Errorf("%q is not a git URL", raw)
	}
	switch u.Scheme {
	case "https", "ssh", "file":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("http git URLs are allowed only to this machine; use https")
	}
	return fmt.Errorf("git URL scheme %q is not allowed (use https, ssh or file)", u.Scheme)
}

var refRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

// gitEnv runs git with no prompts, no system or user config, and only the
// transports CheckGitURL admits.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ALLOW_PROTOCOL=https:ssh:file:http", "GIT_ASKPASS=/bin/false")
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "protocol.ext.allow=never", "-c", "transfer.fsckObjects=true", "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// fetchGit clones url, checks out commit (or ref, or the default branch),
// and reads subdir. The commit and tree it read are returned, so what is
// installed is pinned whatever the ref later points at.
func fetchGit(ctx context.Context, rawURL, ref, commit, subdir string) (*fetched, error) {
	if err := CheckGitURL(rawURL); err != nil {
		return nil, err
	}
	for _, r := range []string{ref, commit} {
		if r != "" && (!refRe.MatchString(r) || strings.Contains(r, "..")) {
			return nil, fmt.Errorf("ref %q is not a branch, tag or commit", r)
		}
	}
	if subdir != "" {
		clean := path.Clean(subdir)
		if strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("plugin path %q leaves the repository", subdir)
		}
		subdir = clean
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	tmp, err := os.MkdirTemp("", "lectern-plugin-git-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	repo := filepath.Join(tmp, "repo")
	if _, err := git(ctx, tmp, "clone", "--quiet", "--no-checkout", "--", rawURL, repo); err != nil {
		return nil, err
	}
	want := commit
	if want == "" {
		want = ref
	}
	if want == "" {
		want = "HEAD"
	} else if ref != "" && commit == "" {
		// A branch name resolves through the remote-tracking ref.
		if _, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", "origin/"+ref+"^{commit}"); err == nil {
			want = "origin/" + ref
		}
	}
	sha, err := git(ctx, repo, "rev-parse", "--verify", want+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("%s has no %s", rawURL, want)
	}
	if _, err := git(ctx, repo, "-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", sha); err != nil {
		return nil, err
	}
	treeSpec := sha + "^{tree}"
	if subdir != "" && subdir != "." {
		treeSpec = sha + ":" + subdir
	}
	tree, err := git(ctx, repo, "rev-parse", "--verify", treeSpec)
	if err != nil {
		return nil, fmt.Errorf("%s has no %s at %s", rawURL, subdir, sha[:12])
	}
	root := repo
	if subdir != "" && subdir != "." {
		root = filepath.Join(repo, filepath.FromSlash(subdir))
	}
	files, err := pluginpkg.ReadDir(root)
	if err != nil {
		return nil, err
	}
	return &fetched{Files: files, Commit: sha, Tree: tree}, nil
}

// Index is a marketplace's list of plugins.
type Index struct {
	Name    string       `json:"name" yaml:"name"`
	Plugins []IndexEntry `json:"plugins" yaml:"plugins"`
	// Commit is the marketplace commit the list was read at.
	Commit string `json:"commit" yaml:"-"`
}

// IndexEntry is one listed plugin, pinned to an exact commit.
type IndexEntry struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name,omitempty" yaml:"name,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Version     string `json:"version,omitempty" yaml:"version,omitempty"`
	Source      string `json:"source" yaml:"source"`
	Commit      string `json:"commit" yaml:"commit"`
	Path        string `json:"path,omitempty" yaml:"path,omitempty"`
}

// IndexFile is the marketplace file at a source repository's root.
const IndexFile = "lectern-plugins.yaml"

// ClaudeMarketplace is Claude Code's marketplace file, read as a source too.
const ClaudeMarketplace = ".claude-plugin/marketplace.json"

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// parseIndex reads lectern-plugins.yaml, or a Claude Code marketplace.json.
// sourceURL and commit are the marketplace's own, for entries that live in
// the same repository ("./plugins/x" in a Claude marketplace).
func parseIndex(files map[string]pluginpkg.File, sourceURL, commit string) (*Index, error) {
	if f, ok := files[IndexFile]; ok {
		dec := yaml.NewDecoder(strings.NewReader(string(f.Data)))
		dec.KnownFields(true)
		var idx Index
		if err := dec.Decode(&idx); err != nil {
			return nil, fmt.Errorf("%s: %w", IndexFile, err)
		}
		for i, e := range idx.Plugins {
			if e.ID == "" || e.Source == "" {
				return nil, fmt.Errorf("%s: plugins[%d] needs an id and a source", IndexFile, i)
			}
			if e.Source == "." || strings.HasPrefix(e.Source, "./") {
				idx.Plugins[i].Path = strings.TrimPrefix(path.Join(strings.TrimPrefix(e.Source, "./"), e.Path), "./")
				idx.Plugins[i].Source = sourceURL
				if e.Commit == "" {
					idx.Plugins[i].Commit = commit
				}
			}
			if !shaRe.MatchString(idx.Plugins[i].Commit) {
				return nil, fmt.Errorf("%s: %s must be pinned to a full commit sha", IndexFile, e.ID)
			}
		}
		idx.Commit = commit
		return &idx, nil
	}
	if f, ok := files[ClaudeMarketplace]; ok {
		return parseClaudeMarketplace(f.Data, sourceURL, commit)
	}
	return nil, fmt.Errorf("no %s (or %s) at the source's root", IndexFile, ClaudeMarketplace)
}

func parseClaudeMarketplace(data []byte, sourceURL, commit string) (*Index, error) {
	var doc struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Version     string          `json:"version"`
			Source      json.RawMessage `json:"source"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", ClaudeMarketplace, err)
	}
	idx := &Index{Name: doc.Name, Commit: commit}
	for _, p := range doc.Plugins {
		e := IndexEntry{ID: p.Name, Name: p.Name, Description: p.Description, Version: p.Version}
		var rel string
		var obj struct {
			Source string `json:"source"`
			Repo   string `json:"repo"`
			URL    string `json:"url"`
			Ref    string `json:"ref"`
			SHA    string `json:"sha"`
			Path   string `json:"path"`
		}
		switch {
		case json.Unmarshal(p.Source, &rel) == nil:
			e.Source, e.Commit, e.Path = sourceURL, commit, strings.TrimPrefix(path.Clean(rel), "./")
		case json.Unmarshal(p.Source, &obj) == nil:
			switch obj.Source {
			case "github":
				e.Source = "https://github.com/" + obj.Repo + ".git"
			case "url", "git":
				e.Source = obj.URL
			default:
				continue // npm, pip and the like are not git sources
			}
			e.Commit, e.Path = obj.SHA, obj.Path
		default:
			continue
		}
		// A Claude marketplace may list a plugin by branch; Lectern installs
		// only exact commits from an index, so such an entry is skipped.
		if !shaRe.MatchString(e.Commit) {
			continue
		}
		idx.Plugins = append(idx.Plugins, e)
	}
	return idx, nil
}
