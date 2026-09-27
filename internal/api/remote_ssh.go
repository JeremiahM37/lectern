package api

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sshconfig"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// SSH depth (docs/ssh.md): import hosts from the control plane's
// ~/.ssh/config, per-target SSH options, a visible connection state with a
// reconnect, the target's listening ports for forwarding, and file/folder
// downloads. Everything that reads the server's own SSH setup, changes how a
// target is reached or copies files off it needs a signed-in person.

// sshConfigPath is where hosts are imported from: ~/.ssh/config, or
// LECTERN_SSH_CONFIG.
func sshConfigPath() string {
	if p := os.Getenv("LECTERN_SSH_CONFIG"); p != "" {
		return p
	}
	return sshconfig.DefaultPath()
}

type sshHostView struct {
	sshconfig.Host
	TargetID   int64  `json:"target_id,omitempty"`
	TargetName string `json:"target_name,omitempty"`
}

func (s *Server) listSSHHosts(w http.ResponseWriter, r *http.Request) {
	if !s.requireHuman(w, r, "reading this server's SSH config") {
		return
	}
	hosts, err := sshconfig.List(r.Context(), sshConfigPath())
	if err != nil {
		writeJSON(w, 200, map[string]any{"hosts": []any{}, "path": sshConfigPath(), "error": err.Error()})
		return
	}
	targets, _ := s.DB.Targets()
	out := make([]sshHostView, 0, len(hosts))
	for _, h := range hosts {
		v := sshHostView{Host: h}
		for _, t := range targets {
			if t.Kind != "ssh" {
				continue
			}
			if executor.ParseSSHOptions(t.SSHJSON).Alias == h.Alias || t.Name == h.Alias {
				v.TargetID, v.TargetName = t.ID, t.Name
			}
		}
		out = append(out, v)
	}
	_, openssh := exec.LookPath("ssh")
	writeJSON(w, 200, map[string]any{"hosts": out, "path": sshConfigPath(), "openssh": openssh == nil})
}

type sshImportIn struct {
	Aliases []string `json:"aliases"`
	// Transport is "openssh" (default when ssh(1) is installed: the config
	// entry applies in full) or "builtin".
	Transport string `json:"transport"`
}

// importSSHHosts turns ssh_config aliases into ssh targets.
func (s *Server) importSSHHosts(w http.ResponseWriter, r *http.Request) {
	if !s.requireHuman(w, r, "importing SSH hosts") {
		return
	}
	var in sshImportIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	if len(in.Aliases) == 0 {
		httpError(w, 422, "choose at least one host")
		return
	}
	known, err := sshconfig.Aliases(sshConfigPath())
	if err != nil {
		httpError(w, 409, "cannot read %s: %s", sshConfigPath(), err)
		return
	}
	transport := in.Transport
	if transport == "" {
		transport = "builtin"
		if _, err := exec.LookPath("ssh"); err == nil {
			transport = "openssh"
		}
	}
	type result struct {
		Alias  string        `json:"alias"`
		Target *store.Target `json:"target,omitempty"`
		Error  string        `json:"error,omitempty"`
	}
	var results []result
	for _, alias := range in.Aliases {
		if !slices.Contains(known, alias) {
			results = append(results, result{Alias: alias, Error: "not in the SSH config"})
			continue
		}
		h := sshconfig.Resolve(r.Context(), sshConfigPath(), alias)
		opts := executor.SSHOptions{Alias: alias, ProxyJump: h.ProxyJump, ForwardAgent: h.ForwardAgent,
			IdentityAgent: h.IdentityAgent, Transport: transport}
		if h.GSSAPI {
			opts.Options = append(opts.Options, "GSSAPIAuthentication=yes")
		}
		if err := opts.Validate(); err != nil {
			results = append(results, result{Alias: alias, Error: err.Error()})
			continue
		}
		name := alias
		if _, err := s.DB.TargetByName(name); err == nil {
			results = append(results, result{Alias: alias, Error: "a machine with this name exists"})
			continue
		}
		key := ""
		if transport == "builtin" && len(h.IdentityFiles) > 0 {
			key = h.IdentityFiles[0]
		}
		user := h.User
		if user == "" {
			user = "root"
		}
		t, err := s.DB.InsertTarget(&store.Target{Name: name, Kind: "ssh", Host: h.HostName, Port: h.Port,
			User: user, KeyPath: key, MaxConcurrent: 4, ContextJSON: "[]", SSHJSON: opts.JSON()})
		if err != nil {
			results = append(results, result{Alias: alias, Error: err.Error()})
			continue
		}
		results = append(results, result{Alias: alias, Target: t})
	}
	s.Reg.Reset()
	writeJSON(w, 200, map[string]any{"results": results})
}

func (s *Server) targetParam(w http.ResponseWriter, r *http.Request) (*store.Target, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such target")
		return nil, false
	}
	t, err := s.DB.Target(id)
	if err != nil {
		httpError(w, 404, "no such target")
		return nil, false
	}
	return t, true
}

// putTargetSSH replaces a target's SSH options.
func (s *Server) putTargetSSH(w http.ResponseWriter, r *http.Request) {
	t, ok := s.targetParam(w, r)
	if !ok || !s.requireHuman(w, r, "changing how a machine is reached") {
		return
	}
	var opts executor.SSHOptions
	if err := decodeBody(r, &opts); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	opts.Alias = strings.TrimSpace(opts.Alias)
	opts.ProxyJump = strings.TrimSpace(opts.ProxyJump)
	opts.EditorHost = strings.TrimSpace(opts.EditorHost)
	if err := opts.Validate(); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	if opts.Transport == "openssh" {
		if _, err := exec.LookPath("ssh"); err != nil {
			httpError(w, 409, "the OpenSSH transport needs the ssh client installed on the Lectern server")
			return
		}
	}
	if err := s.DB.Update("targets", t.ID, map[string]any{"ssh_json": opts.JSON()}); err != nil {
		respondErr(w, err)
		return
	}
	s.Reg.Forget(t.ID)
	out, _ := s.DB.Target(t.ID)
	writeJSON(w, 200, out)
}

// targetConnection reports the connection state. ?probe=1 runs a trivial
// command first, which reconnects a dropped connection, so the answer is
// current rather than what the last command saw.
func (s *Server) targetConnection(w http.ResponseWriter, r *http.Request) {
	t, ok := s.targetParam(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("probe") == "1" {
		s.probeTarget(r.Context(), t)
	}
	writeJSON(w, 200, s.connStatus(t))
}

func (s *Server) probeTarget(ctx context.Context, t *store.Target) {
	if ex, err := s.Reg.For(t); err == nil {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		_, _ = ex.Run(cctx, "true", executor.RunOpts{Timeout: 15})
	}
}

func (s *Server) connStatus(t *store.Target) executor.ConnStatus {
	ex := s.Reg.Cached(t.ID)
	if rep, ok := ex.(executor.StatusReporter); ok {
		return rep.ConnStatus()
	}
	st := executor.ConnStatus{State: "idle", Transport: t.Kind}
	if ex != nil {
		st.State = "connected"
	}
	return st
}

// reconnectTarget drops the cached connection and dials a fresh one.
func (s *Server) reconnectTarget(w http.ResponseWriter, r *http.Request) {
	t, ok := s.targetParam(w, r)
	if !ok {
		return
	}
	before := s.connStatus(t)
	s.Reg.Forget(t.ID)
	s.probeTarget(r.Context(), t)
	after := s.connStatus(t)
	if after.State == "connected" {
		after.Reconnects = before.Reconnects + 1
	}
	s.Bus.Publish("board", "target.connection", map[string]any{"id": t.ID, "status": after})
	writeJSON(w, 200, after)
}

// listeningPortsCmd prints the TCP ports listening on the target, one per
// line, with the owning process where the tool can see it.
const listeningPortsCmd = `if command -v ss >/dev/null 2>&1; then ss -Hltnp 2>/dev/null || ss -Hltn; ` +
	`elif command -v netstat >/dev/null 2>&1; then netstat -ltnp 2>/dev/null | tail -n +3; fi`

type listeningPort struct {
	Port    int    `json:"port"`
	Address string `json:"address"`
	Process string `json:"process,omitempty"`
}

// parseListening reads ss or netstat output.
func parseListening(out string) []listeningPort {
	seen := map[int]bool{}
	var ports []listeningPort
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		local := ""
		process := ""
		if fields[0] == "LISTEN" && len(fields) >= 4 { // ss
			local = fields[3]
			if len(fields) >= 6 {
				process = fields[5]
			}
		} else if strings.HasPrefix(fields[0], "tcp") && len(fields) >= 4 { // netstat
			local = fields[3]
			if len(fields) >= 7 {
				process = fields[6]
			}
		} else {
			continue
		}
		i := strings.LastIndex(local, ":")
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(local[i+1:])
		if err != nil || seen[port] {
			continue
		}
		seen[port] = true
		if p := strings.Index(process, `(("`); p >= 0 {
			process = strings.SplitN(process[p+3:], `"`, 2)[0]
		}
		ports = append(ports, listeningPort{Port: port, Address: local[:i], Process: process})
	}
	sort.Slice(ports, func(a, b int) bool { return ports[a].Port < ports[b].Port })
	return ports
}

func (s *Server) targetPorts(w http.ResponseWriter, r *http.Request) {
	t, ok := s.targetParam(w, r)
	if !ok {
		return
	}
	ex, err := s.Reg.For(t)
	if err != nil {
		respondErr(w, err)
		return
	}
	res, err := ex.Run(r.Context(), listeningPortsCmd, executor.RunOpts{Timeout: 20})
	if err != nil {
		httpError(w, 502, "%s", err)
		return
	}
	writeJSON(w, 200, map[string]any{"ports": parseListening(res.Stdout)})
}

// downloadFromTarget streams a file, or a folder as .tar.gz, off a target.
func (s *Server) downloadFromTarget(w http.ResponseWriter, r *http.Request) {
	t, ok := s.targetParam(w, r)
	if !ok || !s.requireHuman(w, r, "downloading files from a machine") {
		return
	}
	p := strings.TrimSpace(r.URL.Query().Get("path"))
	if p == "" || strings.ContainsAny(p, "\x00\n") || !(strings.HasPrefix(p, "/") || p == "~" || strings.HasPrefix(p, "~/")) {
		httpError(w, 422, "path must be absolute or start with ~/")
		return
	}
	ex, err := s.Reg.For(t)
	if err != nil {
		respondErr(w, err)
		return
	}
	streamer, ok := ex.(executor.Streamer)
	if !ok {
		httpError(w, 409, "this kind of machine cannot stream downloads")
		return
	}
	// ~ is left for the target's shell to expand; everything else is quoted.
	word := executor.ShellQuote(p)
	if p == "~" {
		word = "~"
	} else if strings.HasPrefix(p, "~/") {
		word = `~/` + executor.ShellQuote(p[2:])
	}
	kind, err := ex.Run(r.Context(), fmt.Sprintf(`p=%s; if [ -d "$p" ]; then echo dir; elif [ -f "$p" ]; then echo file; else echo none; fi`, word),
		executor.RunOpts{Timeout: 20})
	if err != nil {
		httpError(w, 502, "%s", err)
		return
	}
	name := path.Base(strings.TrimRight(p, "/"))
	if name == "~" || name == "/" || name == "." || name == "" {
		name = "home"
	}
	var cmd string
	switch strings.TrimSpace(kind.Stdout) {
	case "file":
		cmd = "cat -- " + word
		w.Header().Set("Content-Type", "application/octet-stream")
	case "dir":
		cmd = fmt.Sprintf(`p=%s; cd "$p/.." && tar -czf - -- "$(basename "$p")"`, word)
		name += ".tar.gz"
		w.Header().Set("Content-Type", "application/gzip")
	default:
		httpError(w, 404, "%s does not exist on %s", p, t.Name)
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "no-store")
	s.Log.Info("download from target", "target", t.Name, "path", p)
	cw := &countingWriter{w: w}
	res, err := streamer.Stream(r.Context(), cmd, cw, 3600)
	if err != nil || !res.OK() {
		s.Log.Warn("download failed", "target", t.Name, "path", p, "err", err, "stderr", clipEnd(res.Stderr, 300))
		if cw.n == 0 {
			w.Header().Del("Content-Disposition")
			detail := res.Stderr
			if err != nil {
				detail = err.Error()
			}
			httpError(w, 502, "download failed: %s", clipEnd(strings.TrimSpace(detail), 300))
		}
		// Once bytes have gone out, a truncated body is the only signal left.
	}
}

type countingWriter struct {
	w http.ResponseWriter
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
