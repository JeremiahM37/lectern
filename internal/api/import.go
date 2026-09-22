package api

import (
	"context"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/shellq"
	"github.com/JeremiahM37/lectern/internal/store"
)

// scanScript lists the directories under a root that look like projects.
//
// It runs on the TARGET, through the executor, so importing works the same
// whether your code lives on this box, an LXC, or a machine across the tailnet.
//
// A candidate is anything with a git repo, a build manifest, or a project-shaped
// document (a HANDOFF.md is exactly the marker of a long-running project). Broad
// on purpose: the operator picks from the list, and the projects most worth
// tracking are often the research directories that never got a go.mod.
const scanScript = `for d in __ROOT__/*/; do
  [ -d "$d" ] || continue
  name=$(basename "$d"); git=0; [ -d "$d/.git" ] && git=1
  marker=""
  for f in go.mod package.json pyproject.toml Cargo.toml requirements.txt Makefile \
           CLAUDE.md AGENTS.md HANDOFF.md GOAL.md DESIGN.md STATUS.md README.md; do
    [ -f "$d$f" ] && marker="$f" && break
  done
  [ "$git" = 1 ] || [ -n "$marker" ] || continue
  branch=""; last=""
  if [ "$git" = 1 ]; then
    branch=$(git -C "$d" branch --show-current 2>/dev/null)
    last=$(git -C "$d" log -1 --format=%cr 2>/dev/null)
  fi
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$name" "${d%/}" "$git" "$marker" "$branch" "$last"
done`

// Candidate is a directory that could become a project.
type importCandidate struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Git        bool   `json:"git"`
	Marker     string `json:"marker"`
	Branch     string `json:"branch"`
	LastCommit string `json:"last_commit"`
	// Registered is set when a project already points at this path, so the UI
	// never offers to import the same thing twice.
	Registered bool  `json:"registered"`
	ProjectID  int64 `json:"project_id,omitempty"`
	// VerifyCmd is a suggestion derived from the build manifest, never applied
	// unless the caller asks for it.
	VerifyCmd string `json:"verify_cmd,omitempty"`
}

// verifyFor proposes the command that would prove a change in this project is
// good. A suggestion only: a wrong verify command badges every attempt red.
func verifyFor(marker string) string {
	switch marker {
	case "go.mod":
		return "go test ./..."
	case "pyproject.toml", "requirements.txt":
		return "pytest -q"
	case "package.json":
		return "npm test"
	case "Cargo.toml":
		return "cargo test"
	}
	return ""
}

func (s *Server) scanRoot(ctx context.Context, target *store.Target, root string) ([]importCandidate, error) {
	ex, err := s.Reg.For(target)
	if err != nil {
		return nil, err
	}
	r, err := ex.Run(ctx, scanCommand(root), executor.RunOpts{Timeout: 120})
	if err != nil {
		return nil, err
	}
	existing := map[string]int64{}
	if projects, err := s.DB.Projects(); err == nil {
		for _, p := range projects {
			existing[strings.TrimRight(p.RepoPath, "/")] = p.ID
		}
	}
	out := []importCandidate{}
	for _, line := range strings.Split(r.Stdout, "\n") {
		parts := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(parts) < 6 || parts[1] == "" {
			continue
		}
		c := importCandidate{
			Name: parts[0], Path: parts[1], Git: parts[2] == "1",
			Marker: parts[3], Branch: parts[4], LastCommit: parts[5],
		}
		c.VerifyCmd = verifyFor(c.Marker)
		if id, ok := existing[strings.TrimRight(c.Path, "/")]; ok {
			c.Registered, c.ProjectID = true, id
		}
		out = append(out, c)
	}
	return out, nil
}

// scanCommand substitutes the root into the scan script. A placeholder rather
// than a format verb, because the script is full of shell `%` expansions that a
// format string would eat.
func scanCommand(root string) string {
	return strings.ReplaceAll(scanScript, "__ROOT__",
		shellq.Quote(strings.TrimRight(root, "/")))
}

// scanProjects previews what an import would find, so nothing is registered
// before the operator has seen the list.
func (s *Server) scanProjects(w http.ResponseWriter, r *http.Request) {
	target, ok := s.importTarget(w, r.URL.Query().Get("target_id"))
	if !ok {
		return
	}
	root := r.URL.Query().Get("root")
	if root == "" {
		httpError(w, 400, "a root directory is required")
		return
	}
	found, err := s.scanRoot(r.Context(), target, root)
	if err != nil {
		httpError(w, 502, "scan failed: %s", err.Error())
		return
	}
	writeJSON(w, 200, found)
}

type importIn struct {
	TargetID int64 `json:"target_id"`
	// Root scans a directory; Paths registers specific directories. Either or
	// both — a scan is convenient, an explicit list is exact.
	Root  string   `json:"root"`
	Paths []string `json:"paths"`
	// Verify applies the suggested test command for each project's build system.
	Verify bool `json:"verify"`
	// DryRun reports what would be registered and writes nothing.
	DryRun bool `json:"dry_run"`
}

type importResult struct {
	Imported []*store.Project  `json:"imported"`
	Skipped  []importCandidate `json:"skipped"`
	Planned  []importCandidate `json:"planned,omitempty"`
}

// importProjects registers directories as projects in bulk.
//
// This is what turns lectern from an empty board into a view of the work you
// already have: point it at where your code lives and everything becomes
// dispatchable and, more importantly, session-able.
func (s *Server) importProjects(w http.ResponseWriter, r *http.Request) {
	var in importIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	target, ok := s.importTargetID(w, in.TargetID)
	if !ok {
		return
	}
	var candidates []importCandidate
	if in.Root != "" {
		found, err := s.scanRoot(r.Context(), target, in.Root)
		if err != nil {
			httpError(w, 502, "scan failed: %s", err.Error())
			return
		}
		candidates = found
	}
	// explicit paths are added verbatim: a directory the scan's heuristics miss
	// is still a project if the operator says it is
	seen := map[string]bool{}
	for _, c := range candidates {
		seen[strings.TrimRight(c.Path, "/")] = true
	}
	existing := map[string]int64{}
	if projects, err := s.DB.Projects(); err == nil {
		for _, p := range projects {
			existing[strings.TrimRight(p.RepoPath, "/")] = p.ID
		}
	}
	for _, p := range in.Paths {
		p = strings.TrimRight(strings.TrimSpace(p), "/")
		if p == "" || seen[p] {
			continue
		}
		c := importCandidate{Name: path.Base(p), Path: p}
		if id, ok := existing[p]; ok {
			c.Registered, c.ProjectID = true, id
		}
		candidates = append(candidates, c)
	}
	if len(candidates) == 0 {
		httpError(w, 400, "nothing to import — give a root to scan or explicit paths")
		return
	}

	res := importResult{Imported: []*store.Project{}, Skipped: []importCandidate{}}
	for _, c := range candidates {
		if c.Registered {
			res.Skipped = append(res.Skipped, c)
			continue
		}
		if in.DryRun {
			res.Planned = append(res.Planned, c)
			continue
		}
		branch := c.Branch
		if branch == "" {
			branch = "main"
		}
		verify := ""
		if in.Verify {
			verify = c.VerifyCmd
		}
		proj, err := s.DB.InsertProject(&store.Project{
			Name: c.Name, TargetID: target.ID, RepoPath: c.Path,
			DefaultBaseBranch: branch, VerifyCmd: verify,
		})
		if err != nil {
			c.Registered = false
			res.Skipped = append(res.Skipped, c)
			continue
		}
		s.provisionProjectMemory(r.Context(), proj)
		s.Bus.Publish("board", "project", proj)
		res.Imported = append(res.Imported, proj)
	}
	writeJSON(w, 200, res)
}

func (s *Server) importTarget(w http.ResponseWriter, raw string) (*store.Target, bool) {
	var id int64
	if raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			httpError(w, 400, "target_id must be a number")
			return nil, false
		}
		id = parsed
	}
	return s.importTargetID(w, id)
}

// importTargetID falls back to the single local target when none is named,
// which is the case on a homelab install with one box.
func (s *Server) importTargetID(w http.ResponseWriter, id int64) (*store.Target, bool) {
	if id != 0 {
		t, err := s.DB.Target(id)
		if err != nil {
			httpError(w, 400, "no such target")
			return nil, false
		}
		return t, true
	}
	targets, err := s.DB.Targets()
	if err != nil || len(targets) == 0 {
		httpError(w, 400, "no targets registered")
		return nil, false
	}
	for _, t := range targets {
		if t.Kind == "local" || t.Kind == "mock" {
			return t, true
		}
	}
	return targets[0], true
}
