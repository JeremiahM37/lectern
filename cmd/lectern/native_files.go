package main

// The file pane of a native attachment (docs/terminal-client.md, "Files beside
// the agent"). Ctrl+] f opens the session's workspace as a tree beside the
// agent: a sidebar when the window has room, an overlay when it does not, so
// a terminal tiled to half a screen keeps the agent readable. The pane runs
// here, on the operator's machine, and reads the workspace through the same
// files API as the web terminal's explorer, so a session on an SSH host or in
// a container looks the same as a local one, and nothing is installed on the
// target. Files open in a pager or $EDITOR in a popup over the window; a save
// goes back through the API with the hash it was opened at, so an agent's
// edit in the meantime is never overwritten silently.
//
// `lectern browse` runs the same pane in a terminal split or window of the
// operator's own (kitty, WezTerm, a new tile in a tiling window manager),
// next to whichever attachment was focused last.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/cmd/lectern/localruntime"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// terminalFilesFlag is the private entry point the Ctrl+] f binding and the
// pane's own popups run.
const terminalFilesFlag = "--terminal-files"

// filesPaneOption marks the file pane in the private tmux server, so the
// toggle finds it again.
const filesPaneOption = "@lectern_files"

// Sidebar sizing. Below filesSidebarMinWindow columns the pane opens as an
// overlay instead: a tiled terminal is often 80-100 columns, and a sidebar
// there would leave the agent too narrow to read.
const (
	filesSidebarMinWindow = 100
	filesSidebarMin       = 24
	filesSidebarMax       = 44
)

// filesAPI is the session's workspace through the Lectern API.
type filesAPI struct {
	kind, id, base, token string
	client                *http.Client
}

func newFilesAPI(kind, id, base, token string) *filesAPI {
	return &filesAPI{kind: kind, id: id, base: strings.TrimRight(base, "/"), token: token, client: &http.Client{Timeout: 90 * time.Second}}
}

func (a *filesAPI) do(ctx context.Context, method, suffix string, body io.Reader, header map[string]string) (*http.Response, error) {
	endpoint := a.base + "/api/term/" + url.PathEscape(a.kind) + "/" + url.PathEscape(a.id) + suffix
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return a.client.Do(req)
}

// filesProblem is a non-2xx answer; Status 409 on a save is a conflict.
type filesProblem struct {
	Status int
	Detail string
	SHA    string
}

func (p *filesProblem) Error() string { return p.Detail }

func problemFrom(res *http.Response) error {
	var body struct {
		Detail string `json:"detail"`
		Error  string `json:"error"`
		SHA    string `json:"sha256"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&body)
	detail := body.Detail
	if detail == "" {
		detail = body.Error
	}
	if detail == "" {
		detail = res.Status
	}
	return &filesProblem{Status: res.StatusCode, Detail: detail, SHA: body.SHA}
}

func (a *filesAPI) getJSON(ctx context.Context, suffix string, out any) error {
	res, err := a.do(ctx, "GET", suffix, nil, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return problemFrom(res)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

type fileEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Size      int64  `json:"size"`
	Mtime     int64  `json:"mtime"`
	Link      bool   `json:"link"`
}

func (a *filesAPI) list(ctx context.Context, dir string) ([]fileEntry, error) {
	var out struct {
		Entries []fileEntry `json:"entries"`
	}
	err := a.getJSON(ctx, "/files?path="+url.QueryEscape(dir), &out)
	return out.Entries, err
}

type gitState struct {
	Repository bool              `json:"repository"`
	Status     map[string]string `json:"status"`
	Prefix     string            `json:"prefix"`
}

// gitStatus answers with paths relative to the workspace: git reports them
// from the repository's top, which may be above it.
func (a *filesAPI) gitStatus(ctx context.Context) (map[string]string, error) {
	var out gitState
	if err := a.getJSON(ctx, "/git-status", &out); err != nil {
		return nil, err
	}
	status := map[string]string{}
	for name, kind := range out.Status {
		rel, ok := strings.CutPrefix(name, out.Prefix)
		if !ok || rel == "" {
			continue
		}
		status[rel] = kind
	}
	return status, nil
}

func (a *filesAPI) index(ctx context.Context) ([]string, error) {
	var out struct {
		Files []string `json:"files"`
	}
	err := a.getJSON(ctx, "/index", &out)
	return out.Files, err
}

// read returns a file's bytes and the hash a save must name.
func (a *filesAPI) read(ctx context.Context, rel string, limit int64) ([]byte, string, error) {
	res, err := a.do(ctx, "GET", "/file?path="+url.QueryEscape(rel), nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, "", problemFrom(res)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit))
	return data, res.Header.Get("X-Lectern-Sha256"), err
}

// write saves data if the file on disk is still the one opened at base.
func (a *filesAPI) write(ctx context.Context, rel string, data []byte, base string) error {
	res, err := a.do(ctx, "PUT", "/file?path="+url.QueryEscape(rel), bytes.NewReader(data), map[string]string{"X-Lectern-Base": base})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return problemFrom(res)
	}
	return nil
}

func (a *filesAPI) diff(ctx context.Context, rel string) (string, error) {
	var out struct {
		Path  string `json:"path"`
		Patch string `json:"patch"`
	}
	if err := a.getJSON(ctx, "/changes?path="+url.QueryEscape(rel), &out); err != nil {
		return "", err
	}
	return out.Patch, nil
}

type watchAnswer struct {
	Token   string `json:"token"`
	Changed bool   `json:"changed"`
	Mode    string `json:"mode"`
}

// watch long-polls until one of dirs (or git's HEAD/index) changes.
func (a *filesAPI) watch(ctx context.Context, dirs []string, token string) (watchAnswer, error) {
	body, _ := json.Marshal(map[string]any{"dirs": dirs, "token": token, "timeout": 25})
	var out watchAnswer
	res, err := a.do(ctx, "POST", "/watch", bytes.NewReader(body), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return out, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return out, problemFrom(res)
	}
	return out, json.NewDecoder(res.Body).Decode(&out)
}

func (a *filesAPI) webURL(rel string) string {
	return a.base + "/terminal/" + url.PathEscape(strings.TrimSuffix(a.kind, "-shell")) + "/" + url.PathEscape(a.id) + "?open=" + url.QueryEscape(rel)
}

// ---- the private entry point

// terminalFilesCommand runs `lectern --terminal-files KIND ID BASE VERB ...`:
//
//	toggle WINDOW WIDTH CLIENT   Ctrl+] f: open, focus or close the pane
//	pane [--popup]               the pane itself
//	view PATH | edit PATH | diff PATH   a popup over the window
func terminalFilesCommand(args []string) int {
	if len(args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: lectern "+terminalFilesFlag+" KIND ID BASE VERB ...")
		return 2
	}
	kind, id, base, verb, rest := args[0], args[1], args[2], args[3], args[4:]
	if err := validateControlsTarget(kind, id); err != nil {
		fmt.Fprintln(os.Stderr, "lectern: "+err.Error())
		return 2
	}
	api := newFilesAPI(kind, id, base, os.Getenv("LECTERN_AUTH_TOKEN"))
	switch verb {
	case "toggle":
		if len(rest) != 3 {
			return 2
		}
		if err := toggleFilesPane(args[:3], rest[0], rest[1], rest[2]); err != nil {
			_ = exec.Command("tmux", "-S", os.Getenv(insertSocketEnv), "display-message", "-d", "5000", "Files: "+strings.ReplaceAll(err.Error(), "#", "##")).Run()
			return 1
		}
		return 0
	case "pane":
		popup := len(rest) > 0 && rest[0] == "--popup"
		if err := runFilesPane(api, filesHost{tmux: true, popup: popup, self: args[:3]}); err != nil {
			fmt.Fprintln(os.Stderr, "lectern: "+err.Error())
			fmt.Fprint(os.Stderr, "Press Enter to close.")
			_, _ = fmt.Scanln()
			return 1
		}
		return 0
	case "view", "edit", "diff":
		if len(rest) != 1 {
			return 2
		}
		var err error
		switch verb {
		case "view":
			err = viewWorkspaceFile(api, rest[0])
		case "edit":
			err = editWorkspaceFile(api, rest[0], os.Stdin, os.Stdout)
		default:
			err = diffWorkspaceFile(api, rest[0])
		}
		if err != nil {
			fmt.Println("\n" + err.Error())
			fmt.Print("\nPress Enter to close.")
			_, _ = fmt.Scanln()
			return 1
		}
		return 0
	}
	return 2
}

// ---- Ctrl+] f

func privateTmux(args ...string) (string, error) {
	socket := os.Getenv(insertSocketEnv)
	if socket == "" {
		return "", errors.New("the file pane runs only inside a native attachment")
	}
	out, err := exec.Command("tmux", append([]string{"-S", socket}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// filesSidebarWidth is the sidebar's width in a window this wide: the width
// the operator last dragged it to, else 28% of the window, within bounds.
func filesSidebarWidth(window int, remembered int) int {
	width := window * 28 / 100
	if remembered > 0 {
		width = remembered
	}
	width = max(filesSidebarMin, min(width, filesSidebarMax*2))
	if remembered <= 0 {
		width = min(width, filesSidebarMax)
	}
	// Never take more than half: the agent stays the main thing on screen.
	return min(width, window/2)
}

func filesWidthFile() string {
	if dir := os.Getenv(linkDirEnv); dir != "" {
		return filepath.Join(dir, "files-width")
	}
	return ""
}

// toggleFilesPane: no pane opens one (focused); a pane elsewhere is focused;
// the focused pane closes, remembering the width it was dragged to.
func toggleFilesPane(self []string, window, widthText, client string) error {
	panes, err := privateTmux("list-panes", "-t", window, "-F", "#{pane_id} #{pane_active} #{pane_width} #{"+filesPaneOption+"}")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(panes, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 4 && fields[3] == "1" {
			if fields[1] != "1" {
				_, err := privateTmux("select-pane", "-t", fields[0])
				return err
			}
			if file := filesWidthFile(); file != "" {
				_ = os.WriteFile(file, []byte(fields[2]), 0o600)
			}
			_, err := privateTmux("kill-pane", "-t", fields[0])
			return err
		}
	}
	selfBin, err := os.Executable()
	if err != nil {
		return err
	}
	// A new pane gets the server's environment, not this script's: hand the
	// attachment's private socket, session and folder on explicitly.
	command := []string{"env"}
	for _, name := range []string{insertSocketEnv, insertTargetEnv, linkDirEnv} {
		command = append(command, name+"="+os.Getenv(name))
	}
	command = append(command, selfBin, terminalFilesFlag)
	command = append(command, self...)
	command = append(command, "pane")
	windowWidth, _ := strconv.Atoi(widthText)
	if windowWidth < filesSidebarMinWindow {
		args := []string{"display-popup", "-E", "-w", "92%", "-h", "88%", "-T", " Files "}
		if client != "" {
			args = append(args, "-c", client)
		}
		_, err := privateTmux(append(args, "--", quoteWords(append(command, "--popup")))...)
		return err
	}
	remembered := 0
	if file := filesWidthFile(); file != "" {
		if data, err := os.ReadFile(file); err == nil {
			remembered, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
	}
	width := filesSidebarWidth(windowWidth, remembered)
	pane, err := privateTmux("split-window", "-h", "-b", "-f", "-l", strconv.Itoa(width), "-t", window, "-P", "-F", "#{pane_id}", "--", quoteWords(command))
	if err != nil {
		return err
	}
	_, err = privateTmux("set-option", "-p", "-t", pane, filesPaneOption, "1")
	return err
}

func quoteWords(argv []string) string {
	words := make([]string, len(argv))
	for i, word := range argv {
		words[i] = shellq.Quote(word)
	}
	return strings.Join(words, " ")
}

// agentPane is the pane the attachment's agent runs in.
func agentPane() (string, error) {
	out, err := privateTmux("list-panes", "-s", "-F", "#{pane_id} #{"+agentPaneOption+"}")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[1] == "1" {
			return fields[0], nil
		}
	}
	return "", errors.New("the agent's pane is gone")
}

// ---- view, diff and edit

func filesTempDir() (string, error) {
	return os.MkdirTemp("", openedPrefix)
}

// fetchWorkspaceFile saves rel into a private folder for a pager or editor.
func fetchWorkspaceFile(api *filesAPI, rel string) (file, sha string, data []byte, cleanup func(), err error) {
	data, sha, err = api.read(context.Background(), rel, linkFileLimit+1)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("could not open %s: %v", rel, err)
	}
	if len(data) > linkFileLimit {
		return "", "", nil, nil, fmt.Errorf("%s is larger than 25 MiB", rel)
	}
	folder, err := filesTempDir()
	if err != nil {
		return "", "", nil, nil, err
	}
	file = filepath.Join(folder, safeFileName(path.Base(rel)))
	if err := os.WriteFile(file, data, 0o600); err != nil {
		os.RemoveAll(folder)
		return "", "", nil, nil, err
	}
	return file, sha, data, func() { os.RemoveAll(folder) }, nil
}

// pagerFor shows file with line numbers: bat when it is installed (syntax
// colours), else less.
func pagerFor(file, title string) *exec.Cmd {
	for _, bat := range []string{"bat", "batcat"} {
		if p, err := exec.LookPath(bat); err == nil {
			return exec.Command(p, "--paging=always", "--style=numbers,header", "--file-name", title, file)
		}
	}
	if p, err := exec.LookPath("less"); err == nil {
		return exec.Command(p, "-R", "-N", "-P", title+" (q closes)", file)
	}
	return exec.Command("more", file)
}

func runAttached(cmd *exec.Cmd) error {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func viewWorkspaceFile(api *filesAPI, rel string) error {
	file, _, data, cleanup, err := fetchWorkspaceFile(api, rel)
	if err != nil {
		return err
	}
	defer cleanup()
	switch {
	case strings.HasPrefix(string(data), "%PDF-"):
		if _, err := exec.LookPath("pdftotext"); err != nil {
			return fmt.Errorf("%s is a PDF; install pdftotext to read it here, or open it in the web viewer:\n%s", rel, api.webURL(rel))
		}
		return runAttached(exec.Command("sh", "-c", `pdftotext -layout "$1" - | less -R`, "sh", file))
	case !textual(data):
		return fmt.Errorf("%s is not text (%d KiB). Open it in the web viewer:\n%s", rel, len(data)>>10, api.webURL(rel))
	}
	return runAttached(pagerFor(file, rel))
}

func diffWorkspaceFile(api *filesAPI, rel string) error {
	patch, err := api.diff(context.Background(), rel)
	if err != nil {
		return err
	}
	if strings.TrimSpace(patch) == "" {
		return fmt.Errorf("%s has no uncommitted changes", rel)
	}
	folder, err := filesTempDir()
	if err != nil {
		return err
	}
	defer os.RemoveAll(folder)
	file := filepath.Join(folder, "changes.diff")
	if p, err := exec.LookPath("delta"); err == nil {
		_ = os.WriteFile(file, []byte(patch), 0o600)
		return runAttached(exec.Command("sh", "-c", `"$1" --paging=always < "$2"`, "sh", p, file))
	}
	if err := os.WriteFile(file, []byte(colorPatch(patch)), 0o600); err != nil {
		return err
	}
	return runAttached(exec.Command("less", "-R", "-P", rel+" changes (q closes)", file))
}

// colorPatch colours a unified diff for less -R.
func colorPatch(patch string) string {
	var b strings.Builder
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "):
			b.WriteString("\x1b[1m" + line + "\x1b[0m")
		case strings.HasPrefix(line, "@@"):
			b.WriteString("\x1b[36m" + line + "\x1b[0m")
		case strings.HasPrefix(line, "+"):
			b.WriteString("\x1b[32m" + line + "\x1b[0m")
		case strings.HasPrefix(line, "-"):
			b.WriteString("\x1b[31m" + line + "\x1b[0m")
		default:
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// editorArgv is $VISUAL, $EDITOR, or the first common editor installed.
func editorArgv() []string {
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return []string{"sh", "-c", value + ` "$1"`, "sh"}
		}
	}
	for _, name := range []string{"nvim", "vim", "micro", "nano", "vi"} {
		if p, err := exec.LookPath(name); err == nil {
			return []string{p}
		}
	}
	return nil
}

// editWorkspaceFile edits a copy here and saves it back with the hash it was
// opened at. When the agent changed the file meanwhile, it asks.
func editWorkspaceFile(api *filesAPI, rel string, in io.Reader, out io.Writer) error {
	file, sha, data, cleanup, err := fetchWorkspaceFile(api, rel)
	if err != nil {
		return err
	}
	defer cleanup()
	if !textual(data) {
		return fmt.Errorf("%s is not text; open it in the web viewer:\n%s", rel, api.webURL(rel))
	}
	argv := editorArgv()
	if argv == nil {
		return errors.New("no editor found; set $EDITOR")
	}
	before := sha256.Sum256(data)
	for {
		if err := runAttached(exec.Command(argv[0], append(argv[1:], file)...)); err != nil {
			return fmt.Errorf("the editor failed: %v", err)
		}
		edited, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if sha256.Sum256(edited) == before {
			return nil
		}
		err = api.write(context.Background(), rel, edited, sha)
		var problem *filesProblem
		if err == nil {
			fmt.Fprintf(out, "Saved %s.\n", rel)
			time.Sleep(400 * time.Millisecond)
			return nil
		}
		if !errors.As(err, &problem) || problem.Status != 409 {
			keep := keepCopy(rel, edited)
			return fmt.Errorf("could not save %s: %v\nYour version is in %s", rel, err, keep)
		}
		fmt.Fprintf(out, "\n%s changed on disk while you were editing it (the agent may have written it).\n", rel)
		fmt.Fprint(out, "[o] overwrite with yours  [e] edit again  [k] keep theirs, save yours as a copy  > ")
		var answer string
		_, _ = fmt.Fscanln(in, &answer)
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "o":
			if err := api.write(context.Background(), rel, edited, "any"); err != nil {
				return fmt.Errorf("could not save %s: %v\nYour version is in %s", rel, err, keepCopy(rel, edited))
			}
			fmt.Fprintf(out, "Saved %s.\n", rel)
			return nil
		case "e":
			if problem.SHA != "" {
				sha = problem.SHA
			} else {
				sum := sha256.Sum256(edited)
				sha = hex.EncodeToString(sum[:])
			}
			continue
		default:
			fmt.Fprintf(out, "Kept the file on disk. Your version: %s\n", keepCopy(rel, edited))
			fmt.Fprint(out, "Press Enter to close.")
			_, _ = fmt.Fscanln(in)
			return nil
		}
	}
}

// keepCopy saves an edit that could not be written where the person can find it.
func keepCopy(rel string, data []byte) string {
	home, _ := os.UserHomeDir()
	folder := filepath.Join(home, "Downloads")
	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		folder = home
	}
	target := uniquePath(folder, safeFileName(path.Base(rel))+".mine")
	if os.WriteFile(target, data, 0o600) != nil {
		return "(could not be saved)"
	}
	return target
}

// ---- `lectern files`

const filesUsage = "usage: lectern browse [--session current|ID|KIND/ID] [--pick]"

// filesCommand is `lectern browse`: the file pane in this terminal, for the
// session attached in a neighbouring window (or --session).
func filesCommand(cfg *config.Config, args []string) error {
	session, pick := "current", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session":
			if i+1 >= len(args) {
				return errors.New(filesUsage)
			}
			i++
			session = args[i]
		case "--pick":
			pick = true
		case "-h", "--help":
			fmt.Println(filesUsage)
			return nil
		default:
			return errors.New(filesUsage)
		}
	}
	if !interactiveTerminal() {
		return errors.New("lectern browse needs a terminal")
	}
	state, token, err := resolveAttachState(cfg, session, pick)
	if err != nil {
		return err
	}
	_ = os.Setenv("LECTERN_AUTH_TOKEN", token)
	api := newFilesAPI(state.Kind, state.ID, state.Base, token)
	return runFilesPane(api, filesHost{self: []string{state.Kind, state.ID, state.Base}})
}

// resolveAttachState finds the attachment `lectern split` and `lectern browse`
// work beside, and the credential that reaches its server.
func resolveAttachState(cfg *config.Config, session string, pick bool) (attachState, string, error) {
	var state attachState
	if session == "current" {
		var err error
		state, err = chooseAttachState(readAttachStates(), os.Getenv, pick, os.Stdin, os.Stderr, interactiveTerminal())
		if err != nil {
			return state, "", err
		}
	} else {
		kind, id := "session", session
		if k, rest, ok := strings.Cut(session, "/"); ok {
			kind, id = k, rest
		}
		state = attachState{Kind: kind, ID: id, Base: env("LECTERN_API", "http://127.0.0.1:"+strconv.Itoa(cfg.Port)), AttachHost: os.Getenv("LECTERN_ATTACH_HOST")}
		for _, known := range readAttachStates() {
			if known.Kind == kind && known.ID == id {
				state = known
				break
			}
		}
	}
	token := cfg.AuthToken
	if state.Local {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		binary, err := os.Executable()
		if err != nil {
			return state, "", err
		}
		ep, err := localruntime.Ensure(ctx, binary, cfg)
		if err != nil {
			return state, "", err
		}
		state.Base, token = ep.URL, ep.Token
		_ = os.Setenv("TMUX_TMPDIR", ep.TmuxDir)
	} else if state.TmuxDir != "" {
		_ = os.Setenv("TMUX_TMPDIR", state.TmuxDir)
	}
	return state, token, nil
}
