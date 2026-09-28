package main

// Paths and links an agent prints, in a native attachment (lectern attach,
// lectern claude, the dashboard's attach; docs/terminal-client.md).
//
// The private tmux server this client starts (native_attach.go) runs on the
// operator's own machine, so double-clicking a path or web address opens it
// there: a web address in the default browser, a file — fetched read-only
// through the Lectern API, which only lets a person read outside the
// workspace — with the default app. A right-click offers the same things as
// a menu. There is no "open on the client" channel anywhere: the only way in
// is a mouse event on this private server's pane, which output printed by an
// agent cannot produce, and the API credential lives only in this server's
// environment (the attachment inside it runs without it).
//
// Detection is internal/filelinks, the port of the web terminal's, so both
// find the same links, including paths an agent's TUI wrapped across rows.

import (
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
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/filelinks"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/mattn/go-runewidth"
)

// terminalLinkFlag is the private entry point the link bindings run. It is a
// flag, not a verb, so it can never shadow a subcommand or an agent name, and
// it never starts or reaches a local runtime by itself.
const terminalLinkFlag = "--terminal-link"

// linkDirEnv is the attachment's private 0700 directory, where a found link is
// kept for the menu's actions.
const linkDirEnv = "LECTERN_LINK_DIR"

// linkFileLimit matches the server's cap on what it will send.
const linkFileLimit = 25 << 20

// terminalLinkCommand runs `lectern --terminal-link KIND ID BASE VERB ...`:
//
//	click PANE X Y [--hyperlink=URI]  a double-click: open what is there
//	menu PANE X Y [--hyperlink=URI]   a right-click: write its menu
//	act ACTION LINKFILE               one menu action (open, download, copy, send, web, view)
//	view LINKFILE                     the viewer popup when nothing can open locally
//
// click and menu exit 1 when the cell holds no link, so the binding falls
// back to tmux's own double-click and right-click.
func terminalLinkCommand(args []string) int {
	if len(args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: lectern "+terminalLinkFlag+" KIND ID BASE VERB ...")
		return 2
	}
	e := &linkEnv{
		kind: args[0], id: args[1], base: strings.TrimRight(args[2], "/"),
		token:  os.Getenv("LECTERN_AUTH_TOKEN"),
		socket: os.Getenv(insertSocketEnv), target: os.Getenv(insertTargetEnv),
		dir: os.Getenv(linkDirEnv),
	}
	if err := validateControlsTarget(e.kind, e.id); err != nil || e.socket == "" || e.dir == "" {
		fmt.Fprintln(os.Stderr, "lectern: terminal links run only inside a native attachment")
		return 2
	}
	verb, rest := args[3], args[4:]
	switch verb {
	case "click", "menu":
		if len(rest) < 3 {
			return 2
		}
		x, errX := strconv.Atoi(rest[1])
		y, errY := strconv.Atoi(rest[2])
		if errX != nil || errY != nil {
			return 2
		}
		hyperlink := ""
		for _, arg := range rest[3:] {
			if value, ok := strings.CutPrefix(arg, "--hyperlink="); ok {
				hyperlink = value
			}
			if value, ok := strings.CutPrefix(arg, "--client="); ok {
				e.client = value
			}
		}
		e.pane = rest[0]
		link, ok := e.detect(rest[0], x, y, hyperlink)
		if !ok {
			return 1
		}
		file, err := e.saveLink(link)
		if err != nil {
			return 1
		}
		if verb == "click" {
			// The binding waits for this process; the slow part (a download)
			// runs on its own so the terminal never stalls.
			if err := e.spawn("act", "open", file); err != nil {
				return 1
			}
			return 0
		}
		if err := os.WriteFile(filepath.Join(e.dir, "menu.conf"), []byte(e.menu(link, file)), 0o600); err != nil {
			return 1
		}
		return 0
	case "act":
		if len(rest) != 2 {
			return 2
		}
		link, err := e.loadLink(rest[1])
		if err != nil {
			e.say(failure(rest[0], err))
			return 1
		}
		if delay, _ := strconv.Atoi(os.Getenv(linkDelayEnv)); delay > 0 {
			time.Sleep(time.Duration(min(delay, 2000)) * time.Millisecond)
		}
		if rest[0] != "view" && rest[0] != "web" {
			e.flash(link)
		}
		if err := e.act(rest[0], link); err != nil {
			e.say(failure(rest[0], err))
			return 1
		}
		return 0
	case "flash":
		if len(rest) != 1 {
			return 2
		}
		link, err := e.loadLink(rest[0])
		if err != nil {
			return 1
		}
		e.showFlash(link)
		return 0
	case "hints-open":
		if len(rest) != 4 {
			return 2
		}
		e.pane, e.client = rest[0], rest[1]
		return e.openHints(rest[2], rest[3])
	case "hints":
		if len(rest) != 2 {
			return 2
		}
		e.pane, e.client = rest[0], rest[1]
		return e.hints(os.Stdin, os.Stdout)
	case "view":
		if len(rest) != 1 {
			return 2
		}
		link, err := e.loadLink(rest[0])
		if err == nil {
			err = e.view(link)
		}
		if err != nil {
			fmt.Println(err)
			fmt.Print("\nPress Enter to close.")
			_, _ = fmt.Scanln()
			return 1
		}
		return 0
	}
	return 2
}

type linkEnv struct {
	kind, id, base, token string
	socket, target, dir   string
	// client and pane the link was found on, when known: status messages
	// and the highlight go to that client, over that pane.
	client, pane string
	directAction func(string, filelinks.Link) error
}

// ---- finding the link under the pointer

func (e *linkEnv) tmux(args ...string) (string, error) {
	out, err := exec.Command("tmux", append([]string{"-S", e.socket}, args...)...).Output()
	return string(out), err
}

// say shows a short message on the attached terminal's status line.
func (e *linkEnv) say(message string) {
	if e.directAction != nil {
		fmt.Fprintln(os.Stderr, message)
		return
	}
	args := []string{"display-message", "-d", "5000"}
	if e.client != "" {
		args = append(args, "-c", e.client)
	}
	_, _ = e.tmux(append(args, "--", strings.ReplaceAll(message, "#", "##"))...)
}

func (e *linkEnv) detect(pane string, x, y int, hyperlink string) (filelinks.Link, bool) {
	workdir := e.workdir()
	if hyperlink != "" {
		if link, ok := filelinks.HyperlinkTarget(hyperlink, workdir); ok && e.usable(link, workdir) {
			return link, true
		}
	}
	rows, width, err := e.paneRows(pane)
	if err != nil || y < 0 || y >= len(rows) {
		return filelinks.Link{}, false
	}
	link, ok := filelinks.LinkAt(rows, y, cellToRune(rows[y].Text, x), workdir, width)
	if !ok || !e.usable(link, workdir) {
		return filelinks.Link{}, false
	}
	return link, true
}

// usable: a bare workspace name is a link only if that file exists, and a
// workspace path needs a workspace to be in.
func (e *linkEnv) usable(link filelinks.Link, workdir string) bool {
	if link.Kind == "url" || link.External {
		return true
	}
	if workdir == "" {
		return false
	}
	if !link.Verify {
		return true
	}
	var answer struct{ Exists, Directory bool }
	data, err := e.get("/exists?path=" + url.QueryEscape(link.Path))
	return err == nil && json.Unmarshal(data, &answer) == nil && answer.Exists && !answer.Directory
}

// paneRows is the visible pane as rows, with the rows tmux itself wrapped
// marked, found by comparing the plain capture with the joined one.
func (e *linkEnv) paneRows(pane string) ([]filelinks.Row, int, error) {
	plain, err := e.tmux("capture-pane", "-p", "-N", "-t", pane)
	if err != nil {
		return nil, 0, err
	}
	joined, err := e.tmux("capture-pane", "-p", "-J", "-t", pane)
	if err != nil {
		return nil, 0, err
	}
	size, err := e.tmux("display-message", "-p", "-t", pane, "#{pane_width}")
	if err != nil {
		return nil, 0, err
	}
	width, _ := strconv.Atoi(strings.TrimSpace(size))
	return wrappedRows(plain, joined), width, nil
}

// wrappedRows marks each row of plain that tmux reports, in joined, as the
// continuation of the row before it.
func wrappedRows(plain, joined string) []filelinks.Row {
	lines := strings.Split(strings.TrimSuffix(plain, "\n"), "\n")
	rows := make([]filelinks.Row, len(lines))
	for i, line := range lines {
		rows[i] = filelinks.Row{Text: strings.TrimRight(line, " ")}
	}
	i := 0
	for _, whole := range strings.Split(strings.TrimSuffix(joined, "\n"), "\n") {
		if i >= len(lines) {
			break
		}
		whole = strings.TrimRight(whole, " ")
		acc := lines[i]
		for i+1 < len(lines) && strings.TrimRight(acc, " ") != whole && strings.HasPrefix(whole, acc+strings.TrimRight(lines[i+1], " ")) {
			i++
			acc += lines[i]
			rows[i].Wrapped = true
		}
		i++
	}
	return rows
}

// cellToRune turns a screen column into a character index: a wide character
// fills two cells but is one character.
func cellToRune(text string, cell int) int {
	at := 0
	for i, r := range []rune(text) {
		width := runewidth.RuneWidth(r)
		if cell < at+max(width, 1) {
			return i
		}
		at += width
	}
	return len([]rune(text)) + (cell - at)
}

// workdir is the attachment's workspace, asked once per attachment.
func (e *linkEnv) workdir() string {
	cache := filepath.Join(e.dir, "workdir")
	if data, err := os.ReadFile(cache); err == nil {
		return string(data)
	}
	var info struct {
		Workdir        string `json:"workdir"`
		FilesAvailable bool   `json:"files_available"`
	}
	data, err := e.get("/info")
	if err != nil || json.Unmarshal(data, &info) != nil {
		return ""
	}
	if !info.FilesAvailable || !strings.HasPrefix(info.Workdir, "/") {
		info.Workdir = ""
	}
	_ = os.WriteFile(cache, []byte(info.Workdir), 0o600)
	return info.Workdir
}

// ---- the API

func (e *linkEnv) request(suffix string) (*http.Response, error) {
	endpoint := e.base + "/api/term/" + url.PathEscape(e.kind) + "/" + url.PathEscape(e.id) + suffix
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	res, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		defer res.Body.Close()
		var problem struct {
			Detail string `json:"detail"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&problem)
		if problem.Detail == "" {
			problem.Detail = res.Status
		}
		return nil, errors.New(problem.Detail)
	}
	return res, nil
}

func (e *linkEnv) get(suffix string) ([]byte, error) {
	res, err := e.request(suffix)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(io.LimitReader(res.Body, 1<<20))
}

// fetch saves the file a link names into folder under its own name, read-only
// through the API: the workspace route inside it, the person-only outside
// route otherwise. It returns where it landed.
func (e *linkEnv) fetch(link filelinks.Link, folder string, unique bool) (string, error) {
	route := "/file?path="
	if link.External {
		route = "/external?path="
	}
	res, err := e.request(route + url.QueryEscape(link.Path))
	if err != nil {
		return "", fmt.Errorf("could not open %s: %v", link.Path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, linkFileLimit+1))
	if err != nil {
		return "", err
	}
	if len(data) > linkFileLimit {
		return "", fmt.Errorf("%s is larger than 25 MiB", link.Path)
	}
	name := safeFileName(path.Base(link.Path))
	target := filepath.Join(folder, name)
	if unique {
		target = uniquePath(folder, name)
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(target)
		return "", err
	}
	return target, f.Close()
}

var unsafeName = regexp.MustCompile(`[\x00-\x1f\x7f/\\:*?"<>|]`)

// safeFileName keeps a name and its extension but nothing that could make it
// a path or confuse the program that opens it.
func safeFileName(name string) string {
	name = strings.TrimLeft(unsafeName.ReplaceAllString(name, "_"), ".-")
	if name == "" {
		return "file"
	}
	return name
}

func uniquePath(folder, name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 0; ; n++ {
		candidate := filepath.Join(folder, name)
		if n > 0 {
			candidate = filepath.Join(folder, fmt.Sprintf("%s (%d)%s", stem, n, ext))
		}
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
}

// ---- keeping a found link for the menu

// linkRecord is a found link and where it was found.
type linkRecord struct {
	filelinks.Link
	Client string `json:",omitempty"`
	Pane   string `json:",omitempty"`
}

func (e *linkEnv) saveLink(link filelinks.Link) (string, error) {
	data, err := json.Marshal(linkRecord{Link: link, Client: e.client, Pane: e.pane})
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(e.dir, "link-*.json")
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.Write(data)
	return f.Name(), err
}

func (e *linkEnv) loadLink(file string) (filelinks.Link, error) {
	var link filelinks.Link
	// Only link files this attachment wrote, in its own private directory.
	if filepath.Dir(filepath.Clean(file)) != filepath.Clean(e.dir) {
		return link, errors.New("unknown link")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return link, errors.New("that link has expired")
	}
	var record linkRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return link, err
	}
	if e.client == "" {
		e.client = record.Client
	}
	if e.pane == "" {
		e.pane = record.Pane
	}
	return record.Link, nil
}

// spawn runs another step of this command on its own, detached from tmux.
func (e *linkEnv) spawn(args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	argv := append([]string{terminalLinkFlag, e.kind, e.id, e.base}, args...)
	cmd := exec.Command(self, argv...)
	cmd.SysProcAttr = detachedProcess()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// ---- the right-click menu

// tmuxQuoted is s as one double-quoted tmux word, with # doubled so format
// expansion shows it as written.
func tmuxQuoted(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "#", "##")
	return `"` + r.Replace(s) + `"`
}

func (e *linkEnv) menu(link filelinks.Link, file string) string {
	script := filepath.Join(e.dir, "link.sh")
	run := func(action string) string {
		return "{ run-shell -b " + tmuxQuoted(shellq.Quote(script)+" act "+action+" "+shellq.Quote(file)) + " }"
	}
	title := link.URL
	if link.Kind == "file" {
		title = link.Path
	}
	if len([]rune(title)) > 60 {
		title = "…" + string([]rune(title)[len([]rune(title))-59:])
	}
	items := []string{"display-menu", "-T", tmuxQuoted("#[align=centre]" + title), "-t", "=", "-x", "M", "-y", "M"}
	item := func(label, key, action string) {
		items = append(items, tmuxQuoted(label), key, run(action))
	}
	_, local := localOpener()
	if link.Kind == "url" {
		if local {
			item("Open in browser", "o", "open")
		} else {
			item("Copy link to open in your browser", "o", "open")
		}
		item("Copy link", "c", "copy")
	} else {
		if local {
			item("Open on this machine", "o", "open")
			item("Download to ~/Downloads", "d", "download")
		} else {
			item("View here", "v", "view")
		}
		item("Copy path", "c", "copy")
		item("Send path to the agent", "s", "send")
		item("Open in web viewer", "w", "web")
	}
	return strings.Join(append(items, splitMenuItems...), " ") + "\n"
}

// ---- what the actions do

func (e *linkEnv) act(action string, link filelinks.Link) error {
	switch action {
	case "open":
		return e.open(link)
	case "download":
		return e.download(link)
	case "copy":
		text := link.URL
		if link.Kind == "file" {
			text = e.absolute(link)
		}
		return e.copy(text, "Copied "+text)
	case "send":
		if link.Kind != "file" {
			return errors.New("only a path can be sent to the agent")
		}
		args, err := insertSendKeysArgs(e.socket, e.target, e.shellWord(link)+" ")
		if err != nil {
			return err
		}
		if _, err := e.tmux(args[2:]...); err != nil {
			return err
		}
		e.say("Sent " + e.absolute(link) + " to the agent")
		return nil
	case "web":
		viewer := e.base + "/terminal/" + url.PathEscape(strings.TrimSuffix(e.kind, "-shell")) + "/" + url.PathEscape(e.id) + "?open=" + url.QueryEscape(link.Path)
		if link.Line > 0 {
			viewer += "#L" + strconv.Itoa(link.Line)
		}
		return e.openURL(viewer)
	case "view":
		file, err := e.saveLink(link)
		if err != nil {
			return err
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		command := shellq.Quote(self) + " " + terminalLinkFlag + " " + shellq.Quote(e.kind) + " " + shellq.Quote(e.id) + " " + shellq.Quote(e.base) + " view " + shellq.Quote(file)
		_, err = e.tmux("display-popup", "-E", "-w", "90%", "-h", "85%", "-T", strings.ReplaceAll(path.Base(link.Path), "#", "##"), command)
		return err
	}
	return fmt.Errorf("unknown action %q", action)
}

func (e *linkEnv) absolute(link filelinks.Link) string {
	if link.External {
		return link.Path
	}
	return strings.TrimRight(e.workdir(), "/") + "/" + link.Path
}

// shellWord is the path as the agent's shell should read it: quoted, with a
// leading ~/ left for the shell to expand.
func (e *linkEnv) shellWord(link filelinks.Link) string {
	if rest, ok := strings.CutPrefix(link.Path, "~/"); ok && link.External {
		return "~/" + shellq.Quote(rest)
	}
	return shellq.Quote(e.absolute(link))
}

// copy puts text on the clipboard of the terminal this client runs in (tmux
// forwards it with OSC 52), which works over SSH too.
func (e *linkEnv) copy(text, message string) error {
	if e.directAction != nil {
		return e.directAction("copy", filelinks.Link{Kind: "url", URL: text})
	}
	if _, err := e.tmux("set-buffer", "-w", "--", text); err != nil {
		return err
	}
	e.say(message)
	return nil
}

func (e *linkEnv) openURL(target string) error {
	open, ok := localOpener()
	if !ok {
		return e.copy(target, "Copied link, open it in your browser: "+target)
	}
	e.say("Opening " + target + "…")
	if err := open(target); err != nil {
		return err
	}
	e.say("Opened " + target)
	return nil
}

func (e *linkEnv) open(link filelinks.Link) error {
	if link.Kind == "url" {
		return e.openURL(link.URL)
	}
	open, ok := localOpener()
	if !ok {
		// Nothing on this machine can show it: show it in the terminal.
		return e.act("view", link)
	}
	if !safeToOpen(link.Path) {
		return fmt.Errorf("not opening %s: it could run as a program; use Download or View instead", path.Base(link.Path))
	}
	e.say("Opening " + path.Base(link.Path) + "…")
	sweepOpened()
	folder, err := os.MkdirTemp("", openedPrefix)
	if err != nil {
		return err
	}
	file, err := e.fetch(link, folder, false)
	if err != nil {
		os.RemoveAll(folder)
		return err
	}
	if err := open(file); err != nil {
		return err
	}
	e.say("Opened " + path.Base(file))
	return nil
}

func (e *linkEnv) download(link filelinks.Link) error {
	if link.Kind != "file" {
		return errors.New("only a file can be downloaded")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	folder := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return err
	}
	file, err := e.fetch(link, folder, true)
	if err != nil {
		return err
	}
	shown := file
	if rest, ok := strings.CutPrefix(file, home+string(filepath.Separator)); ok {
		shown = "~/" + rest
	}
	e.say("Downloaded to " + shown)
	return nil
}

// view shows a file in the terminal, for when this machine has nothing to
// open it with (a client on a server over SSH): text in a pager, a PDF as
// its text.
func (e *linkEnv) view(link filelinks.Link) error {
	if link.Kind == "url" {
		fmt.Printf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\\n", link.URL, link.URL)
		fmt.Print("\nPress Enter to close.")
		_, _ = fmt.Scanln()
		return nil
	}
	folder, err := os.MkdirTemp("", openedPrefix)
	if err != nil {
		return err
	}
	defer os.RemoveAll(folder)
	file, err := e.fetch(link, folder, false)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	pager := "less"
	if _, err := exec.LookPath("less"); err != nil {
		pager = "more"
	}
	var cmd *exec.Cmd
	switch {
	case strings.HasPrefix(string(data), "%PDF-"):
		if _, err := exec.LookPath("pdftotext"); err != nil {
			return fmt.Errorf("%s is a PDF (%d KiB); install pdftotext to read it here, or use Open in web viewer", path.Base(link.Path), len(data)>>10)
		}
		cmd = exec.Command("sh", "-c", `pdftotext -layout "$1" - | `+pager, "sh", file)
	case !textual(data):
		return fmt.Errorf("%s is not text (%d KiB); use Open in web viewer", path.Base(link.Path), len(data)>>10)
	default:
		cmd = exec.Command(pager, file)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func textual(data []byte) bool {
	head := data[:min(len(data), 8192)]
	for _, b := range head {
		if b == 0 {
			return false
		}
	}
	return true
}

// ---- opening on this machine

const openedPrefix = "lectern-open-"

// sweepOpened removes files opened more than a day ago: the app they were
// opened in has long read them.
func sweepOpened() {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), openedPrefix+"*"))
	for _, folder := range matches {
		info, err := os.Lstat(folder)
		if err != nil || !info.IsDir() || time.Since(info.ModTime()) < 24*time.Hour {
			continue
		}
		if !ownedByMe(info) {
			continue
		}
		_ = os.RemoveAll(folder)
	}
}

// Files the default app would run rather than show. Opening one from a
// terminal link is refused; it can still be downloaded or viewed.
var runnable = map[string]bool{
	".desktop": true, ".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".command": true, ".tool": true,
	".app": true, ".exe": true, ".bat": true, ".cmd": true, ".com": true, ".msi": true, ".msix": true,
	".ps1": true, ".psm1": true, ".vbs": true, ".vbe": true, ".js": true, ".jse": true, ".wsf": true,
	".wsh": true, ".hta": true, ".scr": true, ".pif": true, ".lnk": true, ".url": true, ".jar": true,
	".appimage": true, ".run": true, ".deb": true, ".rpm": true, ".pkg": true, ".dmg": true, ".apk": true,
	".scpt": true, ".workflow": true, ".terminal": true, ".inetloc": true, ".webloc": true, ".reg": true,
	".py": true, ".pyw": true, ".pl": true, ".rb": true, ".cpl": true, ".gadget": true, ".xpi": true,
}

func safeToOpen(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return ext != "" && ext != "." && !runnable[ext]
}

// localOpener is how this machine opens a file or address with its default
// app, if it can: none over SSH, where the machine is a server.
func localOpener() (func(string) error, bool) {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return nil, false
	}
	run := func(name string, args ...string) func(string) error {
		return func(target string) error {
			cmd := exec.Command(name, append(args, target)...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
			done := make(chan error, 1)
			if err := cmd.Start(); err != nil {
				return err
			}
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					return fmt.Errorf("%s could not open it: %v", name, err)
				}
			case <-time.After(15 * time.Second):
			}
			return nil
		}
	}
	switch runtime.GOOS {
	case "darwin":
		return run("open"), true
	case "windows":
		return run("rundll32", "url.dll,FileProtocolHandler"), true
	}
	if wsl() {
		if _, err := exec.LookPath("wslview"); err == nil {
			return run("wslview"), true
		}
		if _, err := exec.LookPath("explorer.exe"); err == nil {
			return func(target string) error {
				if !strings.Contains(target, "://") {
					out, err := exec.Command("wslpath", "-w", target).Output()
					if err != nil {
						return err
					}
					target = strings.TrimSpace(string(out))
				}
				// explorer.exe reports failure even when it opened the file.
				_ = exec.Command("explorer.exe", target).Run()
				return nil
			}, true
		}
		return nil, false
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil, false
	}
	if _, err := exec.LookPath("xdg-open"); err != nil {
		return nil, false
	}
	return run("xdg-open"), true
}

func wsl() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(data)), "microsoft")
}

// ---- the bindings, on this attachment's private tmux server only

// bindLinks binds double-click and right-click on the private server. Each
// binding asks the link script first and, when the cell holds no link, runs
// exactly what tmux itself had bound (read back from this server, so it
// matches the installed tmux): a double-click still selects a word, a
// right-click still opens tmux's menu. The operator's own tmux is untouched.
func (p *nativeWrapPlan) bindLinks(tmuxPath string) error {
	// The whole root table: tmux 3.7 prints nothing for `list-keys -T root
	// KEY`, which left a 3.7 client with no link bindings at all.
	listing, _ := exec.Command(tmuxPath, "-S", p.socket, "list-keys", "-T", "root").Output()
	defaults := mouseDefaults(string(listing))
	conf := linkBindings(p.linkScript, filepath.Join(p.dir, "menu.conf"), defaults)
	if conf == "" {
		return errors.New("tmux has no mouse bindings to extend")
	}
	file := filepath.Join(p.dir, "links.conf")
	if err := os.WriteFile(file, []byte(conf), 0o600); err != nil {
		return err
	}
	out, err := exec.Command(tmuxPath, "-S", p.socket, "source-file", file).CombinedOutput()
	if err != nil {
		return fmt.Errorf("link bindings: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Tmux's own mouse bindings (3.3 to 3.7), for a tmux whose listing cannot be
// read back: the link bindings then fall back to exactly these.
const (
	defaultDoubleClick = `select-pane -t = ; if-shell -F "#{||:#{pane_in_mode},#{mouse_any_flag}}" { send-keys -M } { copy-mode -H ; send-keys -X select-word ; run-shell -d 0.3 ; send-keys -X copy-pipe-and-cancel }`
	defaultRightClick  = `if-shell -F -t = "#{||:#{mouse_any_flag},#{&&:#{pane_in_mode},#{?#{m/r:(copy|view)-mode,#{pane_mode}},0,1}}}" { select-pane -t = ; send-keys -M } { display-menu -T "#[align=centre]#{pane_index} (#{pane_id})" -t = -x M -y M "Horizontal Split" h { split-window -h } "Vertical Split" v { split-window -v } '' "#{?#{>:#{window_panes},1},,-}#{?window_zoomed_flag,Unzoom,Zoom}" z { resize-pane -Z } Kill X { kill-pane } }`
)

// mouseDefaults reads tmux's own double-click and right-click bindings from
// a root-table listing, falling back to the built-in ones.
func mouseDefaults(listing string) map[string]string {
	defaults := map[string]string{"DoubleClick1Pane": defaultDoubleClick, "MouseDown3Pane": defaultRightClick}
	for key := range defaults {
		if command, ok := boundCommand(listing, key); ok {
			defaults[key] = command
		}
	}
	return defaults
}

// boundCommand is the command list-keys shows for key in the root table, as
// text that parses again inside a { } block.
func boundCommand(listing, key string) (string, bool) {
	pattern := regexp.MustCompile(`^bind-key\s+(?:-r\s+)?(?:-N\s+"(?:[^"\\]|\\.)*"\s+)?-T\s+root\s+` + regexp.QuoteMeta(key) + `\s+(.+)$`)
	for _, line := range strings.Split(listing, "\n") {
		if m := pattern.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			return topLevelSemicolons(m[1]), true
		}
	}
	return "", false
}

// topLevelSemicolons turns list-keys' top-level "\;" separators into the
// plain ";" a { } block uses, leaving quoted text and nested blocks alone.
func topLevelSemicolons(command string) string {
	var out strings.Builder
	depth := 0
	var quote byte
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote == '"' && c == '\\' && i+1 < len(command):
			out.WriteByte(c)
			i++
			c = command[i]
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '{':
			depth++
		case c == '}':
			depth--
		case c == '\\' && depth == 0 && i+1 < len(command) && command[i+1] == ';':
			i++
			c = ';'
		}
		out.WriteByte(c)
	}
	return out.String()
}

// tmuxDQ is s inside a double-quoted tmux string; format sequences in it
// still expand.
func tmuxDQ(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(s) + `"`
}

// linkBindings is the tmux configuration for the two bindings. In copy mode
// both keep tmux's meaning, since the screen is then not what is on it.
// splitMenuItems open a shell on the session's machine beside the agent
// (native_split.go): split-window runs the private server's default-command.
var splitMenuItems = []string{"''", `"Split: shell in project"`, "|", "{ split-window -h }", `"Split: shell in project (stacked)"`, "_", "{ split-window -v }"}

func linkBindings(script, menu string, defaults map[string]string) string {
	ask := func(verb string) string {
		return strings.ReplaceAll(shellq.Quote(script), "#", "##") + " " + verb +
			" #{pane_id} #{mouse_x} #{mouse_y} --hyperlink=#{q:mouse_hyperlink} --client=#{q:client_name}"
	}
	var lines []string
	if double, ok := defaults["DoubleClick1Pane"]; ok {
		lines = append(lines, "bind-key -T root DoubleClick1Pane if-shell -F "+tmuxDQ("#{pane_in_mode}")+
			" { "+double+" } { if-shell "+tmuxDQ("! "+ask("click"))+" { "+double+" } }")
	}
	if right, ok := defaults["MouseDown3Pane"]; ok {
		// Off a link, tmux's own menu, led by the Lectern splits.
		right = strings.Replace(right, "-x M -y M ", "-x M -y M "+strings.Join(splitMenuItems, " ")+" ", 1)
		lines = append(lines, "bind-key -T root MouseDown3Pane if-shell -F "+tmuxDQ("#{pane_in_mode}")+
			" { "+right+" } { if-shell "+tmuxDQ(ask("menu"))+" { source-file "+tmuxDQ(menu)+" } { "+right+" } }")
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}
