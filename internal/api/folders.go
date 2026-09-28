package api

import (
	"net/http"
	"path"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// FoldersMarker starts the folder-listing script, so the mock executor can
// recognise it (internal/executor/mock.go).
const FoldersMarker = "# lectern-folders"

// foldersScript lists the sub-folders of one directory on a machine: the
// folder picker in "Start an agent" (docs/design/simple-ui.md). It prints the
// resolved directory, the machine's home, then one "name<TAB>git" line per
// sub-folder. Read-only, names only, hidden folders left out, like a file
// manager's default view; capped so a huge directory stays a quick answer.
const foldersScript = FoldersMarker + `
p=__PATH__
case "$p" in "" | "~") p=$HOME ;; "~/"*) p=$HOME/${p#"~/"} ;; esac
cd -- "$p" 2>/dev/null || { echo "cannot open that folder" >&2; exit 2; }
pwd
printf '%s\n' "$HOME"
n=0
for d in */; do
  [ -d "$d" ] || continue
  n=$((n + 1)); [ "$n" -gt 500 ] && break
  g=0; [ -e "$d.git" ] && g=1
  printf '%s\t%s\n' "${d%/}" "$g"
done`

type folderEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Git  bool   `json:"git"`
	// ProjectID is set when this folder is already a project on the machine.
	ProjectID int64 `json:"project_id,omitempty"`
}

type folderListing struct {
	Path    string        `json:"path"`
	Parent  string        `json:"parent,omitempty"`
	Home    string        `json:"home"`
	Folders []folderEntry `json:"folders"`
	// ProjectID is set when the listed folder itself is already a project.
	ProjectID int64 `json:"project_id,omitempty"`
}

// targetFolders is GET /api/targets/{id}/folders?path=: the directory browser
// behind "Choose a folder". The path defaults to the machine's home; "~/x" is
// relative to it. Like the import scan it runs on the machine itself, through
// its executor, so it works the same for this computer and an SSH machine.
func (s *Server) targetFolders(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such machine")
		return
	}
	target, err := s.DB.Target(id)
	if err != nil {
		httpError(w, 404, "no such machine")
		return
	}
	want := strings.TrimSpace(r.URL.Query().Get("path"))
	if strings.ContainsAny(want, "\x00\n") {
		httpError(w, 400, "not a folder path")
		return
	}
	if want != "" && want != "~" && !strings.HasPrefix(want, "~/") && !path.IsAbs(want) {
		httpError(w, 400, "use an absolute folder path, or one starting with ~/")
		return
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		httpError(w, 503, "could not connect to %s", target.Name)
		return
	}
	res, err := ex.Run(r.Context(), foldersCommand(want), executor.RunOpts{Timeout: 20})
	if err != nil {
		httpError(w, 502, "could not list folders on %s", target.Name)
		return
	}
	if !res.OK() {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = "cannot open that folder"
		}
		httpError(w, 404, "%s", msg)
		return
	}
	lines := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
	if len(lines) < 2 || !path.IsAbs(lines[0]) {
		httpError(w, 502, "unreadable folder listing")
		return
	}
	out := folderListing{Path: lines[0], Home: lines[1], Folders: []folderEntry{}}
	if out.Path != "/" {
		out.Parent = path.Dir(out.Path)
	}
	projects := map[string]int64{}
	if rows, err := s.DB.Projects(); err == nil {
		for _, p := range rows {
			if p.TargetID == target.ID {
				projects[strings.TrimRight(p.RepoPath, "/")] = p.ID
			}
		}
	}
	out.ProjectID = projects[strings.TrimRight(out.Path, "/")]
	for _, line := range lines[2:] {
		name, git, ok := strings.Cut(line, "\t")
		if !ok || name == "" {
			continue
		}
		full := path.Join(out.Path, name)
		out.Folders = append(out.Folders, folderEntry{Name: name, Path: full, Git: git == "1", ProjectID: projects[full]})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

func foldersCommand(want string) string {
	return strings.Replace(foldersScript, "__PATH__", shellq.Quote(want), 1)
}
