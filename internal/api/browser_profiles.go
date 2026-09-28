package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/browser"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Browser profiles, cookie import and downloads (docs/browser.md).
//
// A session in a project gets that project's own persistent profile by
// default, so a login made there is kept for the project's next session and
// never seen by another project's browser. Named profiles sit beside it. A
// session with no project uses a throwaway profile unless it names one.

var profileLabelRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,30}$`)

// profileScope is the prefix of every profile a session may use.
func profileScope(sess *store.Session) string {
	if sess.ProjectID != nil {
		return fmt.Sprintf("p%d", *sess.ProjectID)
	}
	return "local"
}

// profileFor turns the label the pane or an agent names into the profile's
// full name: "" is the project's default (or a throwaway one with no project),
// "temporary" always a throwaway one.
func profileFor(sess *store.Session, label string) (string, error) {
	switch label {
	case "":
		if sess.ProjectID != nil {
			return profileScope(sess) + "-default", nil
		}
		return "", nil
	case "temporary":
		return "", nil
	}
	if !profileLabelRe.MatchString(label) {
		return "", invalid("a profile name is 1 to 31 lowercase letters, digits, - or _")
	}
	return profileScope(sess) + "-" + label, nil
}

func profileLabel(full string) string {
	if full == "" {
		return "temporary"
	}
	_, label, _ := strings.Cut(full, "-")
	return label
}

// downloadsDir is where a session's downloads land: in its workspace, beside
// the files agents are sent, and excluded from git the same way.
func downloadsDir(workdir string) string {
	if !path.IsAbs(workdir) || strings.ContainsAny(workdir, "\x00\r\n") {
		return ""
	}
	return path.Join(workdir, ".lectern", "downloads")
}

// downloadFinisher gives a finished download its own name back (the browser
// saves it under an id), without overwriting an earlier one, and for a
// browser on the control plane copies it into the session's workspace.
func (s *Server) downloadFinisher(sess *store.Session, sb *sessionBrowser, run browser.Runner, target executor.Executor) func(browser.Download) string {
	return func(d browser.Download) string {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		name := strings.Map(func(r rune) rune {
			if r < 32 || r == '/' || r == '\\' {
				return '_'
			}
			return r
		}, path.Base(d.Name))
		if name == "" || name == "." || name == ".." {
			name = "download"
		}
		dest := downloadsDir(sess.Workdir)
		if dest == "" {
			return ""
		}
		finalize := fmt.Sprintf(`src=%s; dir=%s; name=%s
[ -f "$src" ] || exit 1
base="${name%%.*}"; ext=""; [ "$base" != "$name" ] && ext=".${name##*.}"
out="$dir/$name"; i=1
while [ -e "$out" ]; do out="$dir/$base ($i)$ext"; i=$((i+1)); done
mv -- "$src" "$out" && chmod 600 "$out" && printf '%%s' "$out"`, shellq.Quote(d.Path), shellq.Quote(path.Dir(d.Path)), shellq.Quote(name))
		out, err := run(ctx, finalize)
		if err != nil || out == "" {
			return ""
		}
		if sb.where != "host" {
			s.excludeLectern(ctx, target, sess.Workdir)
			return strings.TrimSpace(out)
		}
		// Carry the file from the control plane to the session's machine.
		data, err := readLocalFile(strings.TrimSpace(out), 200<<20)
		if err != nil {
			s.Log.Warn("browser: could not read a download", "err", err)
			return strings.TrimSpace(out)
		}
		final := path.Join(dest, path.Base(strings.TrimSpace(out)))
		if r, err := target.Run(ctx, "mkdir -p "+shellq.Quote(dest)+" && chmod 700 "+shellq.Quote(dest), executor.RunOpts{Timeout: 30}); err != nil || !r.OK() {
			return strings.TrimSpace(out)
		}
		if err := target.WriteFile(ctx, final, data); err != nil {
			s.Log.Warn("browser: could not copy a download to the workspace", "err", err)
			return strings.TrimSpace(out)
		}
		_, _ = run(ctx, "rm -f -- "+shellq.Quote(strings.TrimSpace(out)))
		s.excludeLectern(ctx, target, sess.Workdir)
		return final
	}
}

// excludeLectern keeps .lectern out of git in an adopted repository, as
// attachments already do.
func (s *Server) excludeLectern(ctx context.Context, ex executor.Executor, workdir string) {
	ignore := fmt.Sprintf("if git -C %s rev-parse --git-dir >/dev/null 2>&1; then ex_file=$(git -C %s rev-parse --path-format=absolute --git-path info/exclude) && { grep -qxF '.lectern/' \"$ex_file\" 2>/dev/null || printf '\\n.lectern/\\n' >> \"$ex_file\"; }; fi", shellq.Quote(workdir), shellq.Quote(workdir))
	_, _ = ex.Run(ctx, ignore, executor.RunOpts{Timeout: 30})
}

// browserProfiles lists the profiles this session may use.
func (s *Server) browserProfiles(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	target, err := s.DB.Target(sess.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := ex.Run(ctx, `ls -1 "$HOME/.lectern/browser-profiles" 2>/dev/null; true`, executor.RunOpts{Timeout: 20})
	if err != nil {
		respondErr(w, err)
		return
	}
	scope := profileScope(sess) + "-"
	labels := map[string]bool{}
	if sess.ProjectID != nil {
		labels["default"] = true
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if l, ok := strings.CutPrefix(strings.TrimSpace(line), scope); ok && profileLabelRe.MatchString(l) {
			labels[l] = true
		}
	}
	out := make([]string, 0, len(labels)+1)
	for l := range labels {
		out = append(out, l)
	}
	sort.Strings(out)
	out = append(out, "temporary")
	current := ""
	if sb := s.browserFor(sess.ID); sb != nil {
		current = profileLabel(sb.profile)
	}
	def, _ := profileFor(sess, "")
	writeJSON(w, 200, map[string]any{"profiles": out, "current": current, "default": profileLabel(def),
		"where": "~/.lectern/browser-profiles on " + target.Name})
}

// deleteBrowserProfile removes one of the session's profiles, and with it
// every cookie and login in it.
func (s *Server) deleteBrowserProfile(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	if !s.requireHuman(w, r, "deleting a browser profile") {
		return
	}
	label := r.PathValue("profile")
	if label == "temporary" || label == "" {
		httpError(w, 422, "a temporary profile is removed with its browser")
		return
	}
	full, err := profileFor(sess, label)
	if err != nil {
		respondErr(w, err)
		return
	}
	if sb := s.browserFor(sess.ID); sb != nil && sb.profile == full {
		httpError(w, 409, "this profile is open in the session's browser; close the browser first")
		return
	}
	target, err := s.DB.Target(sess.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cmd := fmt.Sprintf(`p="$HOME/.lectern/browser-profiles/"%s; [ -d "$p" ] && [ ! -L "$p" ] || exit 3
if [ -L "$p/SingletonLock" ]; then pid=$(readlink "$p/SingletonLock" | sed 's/.*-//'); [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && exit 4; fi
rm -rf -- "$p"`, shellq.Quote(full))
	res, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 30})
	switch {
	case err != nil:
		respondErr(w, err)
	case res.RC == 3:
		httpError(w, 404, "no such profile")
	case res.RC == 4:
		httpError(w, 409, "another browser is using this profile")
	case !res.OK():
		httpError(w, 502, "could not delete the profile: %s", strings.TrimSpace(res.Stderr))
	default:
		w.WriteHeader(204)
	}
}

// importBrowserCookies loads cookies into the session's browser: from a file
// the operator sends (Netscape cookies.txt or JSON), or from a Chrome profile
// on the browser's own machine. Either way the reading and decrypting happen
// on that machine, and only counts come back.
func (s *Server) importBrowserCookies(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	if !s.requireHuman(w, r, "importing cookies") {
		return
	}
	var kind, source string
	var domains []string
	var data []byte
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.Body = http.MaxBytesReader(w, r.Body, 6<<20)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			httpError(w, 413, "choose one cookies file up to 5 MiB")
			return
		}
		defer r.MultipartForm.RemoveAll()
		f, _, err := r.FormFile("file")
		if err != nil {
			httpError(w, 400, "choose a cookies file")
			return
		}
		data, err = io.ReadAll(io.LimitReader(f, 5<<20+1))
		f.Close()
		if err != nil || len(data) > 5<<20 {
			httpError(w, 413, "the cookies file is larger than 5 MiB")
			return
		}
		kind = "file"
		domains = splitDomains(r.FormValue("domains"))
	} else {
		var in struct {
			Profile string `json:"profile_dir"`
			Domains string `json:"domains"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			httpError(w, 422, "invalid JSON body")
			return
		}
		kind, source, domains = "chrome", strings.TrimSpace(in.Profile), splitDomains(in.Domains)
		if source == "" {
			source = "auto"
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	sb, err := s.ensureBrowser(ctx, sess, browser.Viewport{}, "")
	if err != nil {
		actError(w, err)
		return
	}
	if kind == "chrome" && sb.where == "host" {
		httpError(w, 409, "this session's browser runs on the Lectern host, so a Chrome profile on its machine "+
			"cannot be imported without carrying its cookies off that machine; import a cookies file instead")
		return
	}
	if kind == "file" {
		nonce := make([]byte, 8)
		_, _ = rand.Read(nonce)
		source = sb.proc.Dir + "/import-" + hex.EncodeToString(nonce) + ".cookies"
		if err := s.writeWhereBrowserRuns(ctx, sess, sb, source, data); err != nil {
			httpError(w, 502, "could not stage the cookies file: %s", err)
			return
		}
	}
	res, err := browser.ImportCookiesWith(ctx, sb.run, sb.lectern, sb.proc, kind, source, domains)
	if err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	s.Log.Info("browser: cookies imported", "session", sess.ID, "kind", kind, "count", res.Imported)
	writeJSON(w, 200, res)
}

func splitDomains(raw string) []string {
	var out []string
	for _, d := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// writeWhereBrowserRuns puts bytes on the machine the session's browser runs
// on: the session's target, or the control plane for a stand-in browser.
func (s *Server) writeWhereBrowserRuns(ctx context.Context, sess *store.Session, sb *sessionBrowser, dest string, data []byte) error {
	var ex executor.Executor = executor.NewLocal()
	if sb.where != "host" {
		target, err := s.DB.Target(sess.TargetID)
		if err != nil {
			return err
		}
		if ex, err = s.Reg.For(target); err != nil {
			return err
		}
	}
	if err := ex.WriteFile(ctx, dest, data); err != nil {
		return err
	}
	res, err := ex.Run(ctx, "chmod 600 "+shellq.Quote(dest), executor.RunOpts{Timeout: 20})
	if err == nil && !res.OK() {
		err = errors.New(strings.TrimSpace(res.Stderr))
	}
	return err
}

func readLocalFile(p string, limit int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("the download is larger than %d MiB", limit>>20)
	}
	return data, err
}
