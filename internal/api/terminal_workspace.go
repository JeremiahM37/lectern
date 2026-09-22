package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/terminal"
	"github.com/JeremiahM37/lectern/v2/web"
)

func (s *Server) terminalPage(w http.ResponseWriter, r *http.Request) {
	body, err := web.Assets.ReadFile("static/terminal.html")
	if err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(body)
}

// Resolve a workspace from durable records; never accept a client-supplied root.
func (s *Server) terminalDirectory(kind, rawID string) (string, int64, error) {
	kind = strings.TrimSuffix(kind, "-shell")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return "", 0, err
	}
	switch kind {
	case "session":
		row, err := s.DB.Session(id)
		if err != nil {
			return "", 0, err
		}
		return row.Workdir, row.TargetID, nil
	case "project":
		row, err := s.DB.Project(id)
		if err != nil {
			return "", 0, err
		}
		return row.RepoPath, row.TargetID, nil
	case "attempt":
		row, err := s.DB.Attempt(id)
		if err != nil {
			return "", 0, err
		}
		project, err := s.DB.ProjectForAttempt(id)
		if err != nil {
			return "", 0, err
		}
		return row.WorktreePath, project.TargetID, nil
	}
	return "", 0, fmt.Errorf("not a terminal")
}

func (s *Server) terminalInfo(w http.ResponseWriter, r *http.Request) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	att, target, err := s.resolveAttachment(kind, id)
	if err != nil {
		httpError(w, 404, "%s", err)
		return
	}
	argv, err := terminal.AttachArgv(att, target)
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	dir, _, _ := s.terminalDirectory(kind, id)
	shellURL := ""
	if kind == "session" || kind == "attempt" {
		shellURL = "/term/" + kind + "-shell/" + id
	}
	writeJSON(w, 200, map[string]any{"kind": kind, "id": id, "tmux_session": att.TmuxSession, "target": target.Name,
		"workdir": dir, "terminal_url": att.BasePath(), "shell_url": shellURL, "attach_argv": argv,
		"files_available": path.IsAbs(dir) && target.Kind != "sandbox",
		"desktop_uri":     "lectern://attach/" + kind + "/" + id,
		// The remote peer is a hosted control plane. Mark the command so a
		// newer CLI's no-API local default cannot start a second local database.
		"desktop_command": "ssh -t lectern /usr/local/bin/lectern --hosted-attach attach " + kind + " " + id})
}

func (s *Server) terminalHistory(w http.ResponseWriter, r *http.Request) {
	att, target, err := s.resolveAttachment(r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		httpError(w, 404, "%s", err)
		return
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return
	}
	if target.Kind == "sandbox" {
		ex = executor.NewPct(att.SandboxVMID)
	}
	result, err := ex.Run(r.Context(), "tmux capture-pane -p -J -S -100000 -t "+shellq.Quote("="+att.TmuxSession+":"), executor.RunOpts{Timeout: 20})
	if err != nil || !result.OK() {
		httpError(w, 502, "could not read terminal history")
		return
	}
	truncated := false
	if len(result.Stdout) > 8<<20 {
		result.Stdout = result.Stdout[len(result.Stdout)-(8<<20):]
		truncated = true
	}
	writeJSON(w, 200, map[string]any{"text": result.Stdout, "truncated": truncated, "limit_lines": 100000})
}

func (s *Server) terminalUpload(w http.ResponseWriter, r *http.Request) {
	_, _, err := s.resolveAttachment(r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		httpError(w, 404, "%s", err)
		return
	}
	dir, targetID, err := s.terminalDirectory(r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		httpError(w, 404, "%s", err)
		return
	}
	s.uploadAttachment(w, r, targetID, dir)
}

// File operations run on the actual target. Resolve symlinks beneath the saved
// workspace and cap regular-file reads before returning bytes through the API.
const terminalFileScript = `import os,sys,json,base64,stat
root=os.path.realpath(sys.argv[1]); rel=sys.argv[2]; action=sys.argv[3]
def inside(p): return os.path.commonpath([root,p])==root
def bookkeeping(p):
 if len(sys.argv)<5 or sys.argv[4]!='grouped': return False
 name=os.path.relpath(p,root)
 return name in ('.lectern-lock','.lectern-state.json','.lectern-process.json','.lectern-state.next','.agentdeck-lock','.agentdeck-state.json','.agentdeck-process.json','.agentdeck-state.next') or (os.sep not in name and (name.startswith('.lectern-write-') or name.startswith('.agentdeck-write-')))
try:
 p=os.path.realpath(os.path.join(root,rel))
 if not inside(p): raise ValueError('path is outside this workspace')
 if bookkeeping(p): raise ValueError('workspace bookkeeping is not a project file')
 if action=='list':
  entries=[]
  with os.scandir(p) as scan:
   for e in scan:
    if len(entries)>=500: break
    dest=os.path.realpath(e.path)
    if not inside(dest) or bookkeeping(dest): continue
    try:
     st=e.stat()
     if not stat.S_ISREG(st.st_mode) and not stat.S_ISDIR(st.st_mode): continue
     entries.append(dict(name=e.name,path=os.path.relpath(e.path,root),directory=stat.S_ISDIR(st.st_mode),size=st.st_size))
    except OSError: continue
  entries.sort(key=lambda e:(not e['directory'],e['name'].lower()))
  print(json.dumps(dict(entries=entries,path=os.path.relpath(p,root),limit=500)))
 else:
  fd=os.open(p,os.O_RDONLY|os.O_NONBLOCK)
  with os.fdopen(fd,'rb') as f:
   st=os.fstat(f.fileno())
   if not inside(os.path.realpath('/proc/self/fd/'+str(f.fileno()))): raise ValueError('path left workspace')
   if not stat.S_ISREG(st.st_mode): raise ValueError('choose a regular file')
   if st.st_size>25*1024*1024: raise ValueError('file exceeds 25 MiB download limit')
   data=f.read(25*1024*1024+1)
   if len(data)>25*1024*1024: raise ValueError('file exceeds 25 MiB download limit')
   print(json.dumps(dict(data=base64.b64encode(data).decode())))
except (OSError,ValueError) as e:
 print(json.dumps(dict(error=str(e))))
 sys.exit(1)
`

func (s *Server) terminalFileResult(w http.ResponseWriter, r *http.Request, action string) (map[string]json.RawMessage, bool) {
	_, target, err := s.resolveAttachment(r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		httpError(w, 404, "%s", err)
		return nil, false
	}
	dir, _, err := s.terminalDirectory(r.PathValue("kind"), r.PathValue("id"))
	if err != nil || !path.IsAbs(dir) || target.Kind == "sandbox" {
		httpError(w, 409, "files unavailable for this workspace")
		return nil, false
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return nil, false
	}
	mode := ""
	// Continuations may share an owned root without owning the allocation.
	// Resolve its role from durable records, not filenames supplied by clients.
	workspace, loadErr := s.Sessions.WorkspaceAt(target.ID, dir)
	if loadErr != nil {
		respondErr(w, loadErr)
		return nil, false
	}
	if workspace != nil {
		mode = "grouped"
	}
	cmd := "python3 -c " + shellq.Quote(terminalFileScript) + " " + shellq.Quote(dir) + " " + shellq.Quote(r.URL.Query().Get("path")) + " " + shellq.Quote(action) + " " + shellq.Quote(mode)
	result, err := ex.Run(r.Context(), cmd, executor.RunOpts{Timeout: 60})
	var out map[string]json.RawMessage
	if err != nil || json.Unmarshal([]byte(result.Stdout), &out) != nil {
		httpError(w, 502, "could not read workspace files")
		return nil, false
	}
	if !result.OK() {
		var message string
		json.Unmarshal(out["error"], &message)
		httpError(w, 400, "%s", message)
		return nil, false
	}
	return out, true
}
func (s *Server) terminalFiles(w http.ResponseWriter, r *http.Request) {
	out, ok := s.terminalFileResult(w, r, "list")
	if ok {
		writeJSON(w, 200, out)
	}
}
func (s *Server) terminalFile(w http.ResponseWriter, r *http.Request) {
	out, ok := s.terminalFileResult(w, r, "read")
	if !ok {
		return
	}
	var encoded string
	json.Unmarshal(out["data"], &encoded)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		respondErr(w, err)
		return
	}
	name := path.Base(r.URL.Query().Get("path"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}
