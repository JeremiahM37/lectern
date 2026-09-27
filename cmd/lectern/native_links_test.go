package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/filelinks"
)

// The link bindings run this binary with --terminal-link; under `go test`
// that binary is the test itself, so it answers the same way main does.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == terminalLinkFlag {
		os.Exit(terminalLinkCommand(os.Args[2:]))
	}
	os.Exit(m.Run())
}

const ownerPDF = "/home/admin/.formwork/application-testing-20260927/Jeremiah_Mackey_Cerebras.pdf"

// linkRig is a real private attachment server (the one `lectern attach`
// starts) showing the exact bytes Codex printed for the owner's link, a stand-
// in Lectern API holding that PDF, and a stand-in for the desktop's opener
// that records what it was asked to open.
type linkRig struct {
	t        *testing.T
	tmux     string
	plan     *nativeWrapPlan
	pane     string
	root     string
	home     string
	opened   string
	pdf      []byte
	mu       sync.Mutex
	requests []string
}

func (r *linkRig) asked() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.requests, " ")
}

func newLinkRig(t *testing.T, screen []byte, remote bool, shown string) *linkRig {
	t.Helper()
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	r := &linkRig{t: t, tmux: tmuxPath, root: t.TempDir(), home: t.TempDir(), pdf: []byte("%PDF-1.4\n% the owner's résumé\n%%EOF\n")}
	if err := os.WriteFile(filepath.Join(r.root, "hello.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.requests = append(r.requests, req.URL.Path+"?"+req.URL.RawQuery)
		r.mu.Unlock()
		if req.Header.Get("Authorization") != "Bearer scoped-secret" {
			w.WriteHeader(401)
			return
		}
		name := req.URL.Query().Get("path")
		switch strings.TrimPrefix(req.URL.Path, "/api/term/session/17") {
		case "/info":
			json.NewEncoder(w).Encode(map[string]any{"workdir": r.root, "files_available": true})
		case "/exists":
			_, err := os.Stat(filepath.Join(r.root, name))
			json.NewEncoder(w).Encode(map[string]any{"exists": err == nil, "path": name})
		case "/external":
			if name != ownerPDF {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"detail":"No such file or directory"}`)
				return
			}
			w.Write(r.pdf)
		case "/file":
			data, err := os.ReadFile(filepath.Join(r.root, name))
			if err != nil {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"detail":"No such file or directory"}`)
				return
			}
			w.Write(data)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(api.Close)

	// The desktop's opener: it records its argument and keeps a copy, since
	// the opened file is removed a day later.
	bin := t.TempDir()
	r.opened = filepath.Join(t.TempDir(), "opened.log")
	opener := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> " + shellQuoteTest(r.opened) + "\n" +
		"if [ -f \"$1\" ]; then cp \"$1\" " + shellQuoteTest(r.opened) + ".copy; stat -c %a \"$(dirname \"$1\")\" >> " + shellQuoteTest(r.opened) + "; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "xdg-open"), []byte(opener), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", r.home)
	t.Setenv("DISPLAY", ":99")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("SSH_CONNECTION", "")
	if remote {
		t.Setenv("SSH_CONNECTION", "192.0.2.1 50000 192.0.2.2 22")
	}

	dir, err := os.MkdirTemp("", "lk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	screenFile := filepath.Join(dir, "screen.bin")
	if err := os.WriteFile(screenFile, screen, 0o600); err != nil {
		t.Fatal(err)
	}
	argv := []string{"sh", "-c", "printf '\\033[2J\\033[H'; cat " + shellQuoteTest(screenFile) + "; exec sleep 600"}
	r.plan, err = newNativeWrapPlan(dir, filepath.Join(dir, "sock"), nativeControls{Kind: "session", ID: "17", Base: api.URL, Token: "scoped-secret"}, argv, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.plan.write(); err != nil {
		t.Fatal(err)
	}
	if err := r.plan.start(tmuxPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command(tmuxPath, "-S", r.plan.socket, "kill-server").Run() })
	r.pane = strings.TrimSpace(r.run("display-message", "-p", "-t", r.plan.session, "#{pane_id}"))
	r.waitScreen(shown)
	return r
}

func shellQuoteTest(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (r *linkRig) run(args ...string) string {
	r.t.Helper()
	out, err := exec.Command(r.tmux, append([]string{"-S", r.plan.socket}, args...)...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("tmux %v: %v: %s", args, err, out)
	}
	return string(out)
}

func (r *linkRig) screen() string {
	return r.run("capture-pane", "-p", "-t", r.pane)
}

func (r *linkRig) waitScreen(text string) {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(r.screen(), text) {
		if time.Now().After(deadline) {
			r.t.Fatalf("%q never appeared:\n%s", text, r.screen())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// at is the pane cell of the offset-th character of text on screen.
func (r *linkRig) at(text string, offset int) (int, int) {
	r.t.Helper()
	for y, row := range strings.Split(r.screen(), "\n") {
		if i := strings.Index(row, text); i >= 0 {
			return len([]rune(row[:i])) + offset, y
		}
	}
	r.t.Fatalf("%q is not on screen:\n%s", text, r.screen())
	return 0, 0
}

// script runs the link script as the bindings do, with the server's environment.
func (r *linkRig) script(args ...string) int {
	cmd := exec.Command(r.plan.linkScript, args...)
	cmd.Env = r.plan.serverEnv()
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		r.t.Fatal(err)
	}
	return 0
}

func (r *linkRig) click(verb, text string, offset int) int {
	x, y := r.at(text, offset)
	return r.script(verb, r.pane, strconv.Itoa(x), strconv.Itoa(y), "--hyperlink=")
}

func (r *linkRig) waitOpened(want int) []string {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(r.opened)
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(data) > 0 && len(lines) >= want {
			return lines
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("the opener was not called %d time(s): %q", want, data)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func codexScreen(t *testing.T) []byte {
	data, err := os.ReadFile("../../internal/filelinks/testdata/codex-markdown-link.bin")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The owner's report, natively: every row of the wrapped path opens the PDF
// on this machine, fetched read-only into a private folder.
func TestNativeDoubleClickOpensTheWrappedPDFOnThisMachine(t *testing.T) {
	r := newLinkRig(t, codexScreen(t), false, "Jeremiah_Mackey_Cerebras.pdf)")
	for i, row := range []string{"/home/admin/.formwork/", "application-testing-20260927/", "Jeremiah_Mackey_Cerebras.pdf)"} {
		if code := r.click("click", row, 3); code != 0 {
			t.Fatalf("%s: exit %d", row, code)
		}
		lines := r.waitOpened(2 * (i + 1))
		opened, mode := lines[len(lines)-2], lines[len(lines)-1]
		if filepath.Base(opened) != "Jeremiah_Mackey_Cerebras.pdf" || !strings.HasPrefix(opened, os.TempDir()) {
			t.Fatalf("opened %q", opened)
		}
		if mode != "700" {
			t.Fatalf("the opened file's folder is mode %s, want 700", mode)
		}
		if data, _ := os.ReadFile(r.opened + ".copy"); string(data) != string(r.pdf) {
			t.Fatalf("opened bytes %q", data)
		}
	}
	// A web address opens as itself.
	if code := r.click("click", "https://example.com/docs/page", 4); code != 0 {
		t.Fatalf("url: exit %d", code)
	}
	if lines := r.waitOpened(7); lines[6] != "https://example.com/docs/page" {
		t.Fatalf("opened %q", lines[6])
	}
	// Words that are not links fall back to tmux's own double-click.
	if code := r.click("click", "review:", 1); code != 1 {
		t.Fatalf("a plain word was taken as a link: exit %d", code)
	}
}

// A bare name is a link only when the workspace has it; a missing outside
// file names the path that was tried.
func TestNativeBareNamesAndMissingFiles(t *testing.T) {
	r := newLinkRig(t, []byte("wrote hello.txt and Missing_Report.pdf\r\nsee /tmp/nowhere/gone.pdf\r\n"), false, "gone.pdf")
	if code := r.click("click", "Missing_Report.pdf", 2); code != 1 {
		t.Fatalf("a missing bare name was offered: exit %d", code)
	}
	if code := r.click("click", "hello.txt", 2); code != 0 {
		t.Fatalf("hello.txt: exit %d", code)
	}
	if lines := r.waitOpened(2); filepath.Base(lines[0]) != "hello.txt" {
		t.Fatalf("opened %q", lines[0])
	}
	if code := r.click("click", "/tmp/nowhere/gone.pdf", 2); code != 0 {
		t.Fatalf("an absolute path is a link: exit %d", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(r.asked(), "gone.pdf") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if data, _ := os.ReadFile(r.opened); strings.Contains(string(data), "gone.pdf") {
		t.Fatal("a missing file reached the opener")
	}
}

// The right-click menu, and what its actions do.
func TestNativeRightClickMenuActions(t *testing.T) {
	r := newLinkRig(t, codexScreen(t), false, "Jeremiah_Mackey_Cerebras.pdf)")
	if code := r.click("menu", "application-testing-20260927/", 2); code != 0 {
		t.Fatalf("menu: exit %d", code)
	}
	menu, err := os.ReadFile(filepath.Join(r.plan.dir, "menu.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`display-menu -T "##[align=centre]…`, `"Open on this machine" o`, `"Download to ~/Downloads" d`, `"Copy path" c`, `"Send path to the agent" s`, `"Open in web viewer" w`} {
		if !strings.Contains(string(menu), want) {
			t.Fatalf("menu is missing %q:\n%s", want, menu)
		}
	}
	// The menu parses as tmux configuration (display-menu needs a client, so
	// it fails for that reason only).
	out, _ := exec.Command(r.tmux, "-S", r.plan.socket, "source-file", "-n", filepath.Join(r.plan.dir, "menu.conf")).CombinedOutput()
	if len(out) > 0 {
		t.Fatalf("menu does not parse: %s", out)
	}
	files, _ := filepath.Glob(filepath.Join(r.plan.dir, "link-*.json"))
	if len(files) != 1 {
		t.Fatalf("link files %v", files)
	}
	act := func(action string) {
		t.Helper()
		if code := r.script("act", action, files[0]); code != 0 {
			t.Fatalf("%s: exit %d", action, code)
		}
	}
	act("download")
	data, err := os.ReadFile(filepath.Join(r.home, "Downloads", "Jeremiah_Mackey_Cerebras.pdf"))
	if err != nil || string(data) != string(r.pdf) {
		t.Fatalf("download: %v %q", err, data)
	}
	act("download")
	if _, err := os.Stat(filepath.Join(r.home, "Downloads", "Jeremiah_Mackey_Cerebras (1).pdf")); err != nil {
		t.Fatalf("a second download must not overwrite the first: %v", err)
	}
	act("copy")
	if got := r.run("show-buffer"); got != ownerPDF {
		t.Fatalf("copied %q", got)
	}
	act("send")
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(r.run("capture-pane", "-p", "-J", "-t", r.pane), "docs/page)"+ownerPDF+" ") {
		if time.Now().After(deadline) {
			t.Fatalf("the path was not typed for the agent:\n%s", r.screen())
		}
		time.Sleep(50 * time.Millisecond)
	}
	act("web")
	lines := r.waitOpened(1)
	if want := "/terminal/session/17?open=" + strings.ReplaceAll(strings.ReplaceAll(ownerPDF, "/", "%2F"), "_", "_"); !strings.HasSuffix(lines[len(lines)-1], want) {
		t.Fatalf("web viewer %q, want …%s", lines[len(lines)-1], want)
	}
	// A link file from anywhere else is refused.
	stray := filepath.Join(t.TempDir(), "link-x.json")
	os.WriteFile(stray, []byte(`{"Kind":"url","URL":"https://evil.example"}`), 0o600)
	if code := r.script("act", "open", stray); code == 0 {
		t.Fatal("a link file outside the attachment was honoured")
	}
	// Not a link: no menu, so tmux shows its own.
	if code := r.click("menu", "review:", 1); code != 1 {
		t.Fatalf("menu on a plain word: exit %d", code)
	}
}

// On a server over SSH nothing can open on "this machine": a web address is
// copied to the operator's clipboard (OSC 52) instead, and the menu offers to
// view a file in the terminal.
func TestNativeLinksOverSSHCopyInsteadOfOpening(t *testing.T) {
	r := newLinkRig(t, codexScreen(t), true, "Jeremiah_Mackey_Cerebras.pdf)")
	if code := r.click("click", "https://example.com/docs/page", 4); code != 0 {
		t.Fatalf("url: exit %d", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, _ := exec.Command(r.tmux, "-S", r.plan.socket, "show-buffer").Output()
		if string(out) == "https://example.com/docs/page" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the link was not copied: %q", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(r.opened); err == nil {
		t.Fatal("something was opened on a server")
	}
	r.click("menu", "application-testing-20260927/", 2)
	menu, _ := os.ReadFile(filepath.Join(r.plan.dir, "menu.conf"))
	if !strings.Contains(string(menu), `"View here" v`) || strings.Contains(string(menu), "Open on this machine") || strings.Contains(string(menu), "Download") {
		t.Fatalf("server menu:\n%s", menu)
	}
}

// Real mouse input, through a real tmux client: a double-click on the wrapped
// path opens it, a double-click on a word still selects the word, and output
// that merely looks like mouse input does nothing.
func TestNativeMouseBindingsOnARealClient(t *testing.T) {
	// The program also prints what a double-click on the path would send.
	fake := "\x1b[<0;10;18M\x1b[<0;10;18m"
	r := newLinkRig(t, append(codexScreen(t), []byte(fake+fake+"\r\n")...), false, "Jeremiah_Mackey_Cerebras.pdf)")
	outer := filepath.Join(r.plan.dir, "outer")
	client := "env TERM=xterm-256color " + shellQuoteTest(r.tmux) + " -S " + shellQuoteTest(r.plan.socket) + " attach -t " + r.plan.session
	if out, err := exec.Command(r.tmux, "-S", outer, "-f", "/dev/null", "new-session", "-d", "-x", "100", "-y", "30", "-s", "outer", client).CombinedOutput(); err != nil {
		t.Fatalf("outer: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command(r.tmux, "-S", outer, "kill-server").Run() })
	mouse := func(button, x, y int) {
		// SGR mouse reports, press and release, as a terminal sends them.
		seq := fmt.Sprintf("\x1b[<%d;%d;%dM\x1b[<%d;%d;%dm", button, x, y, button, x, y)
		if out, err := exec.Command(r.tmux, append([]string{"-S", outer, "send-keys", "-t", "outer", "-H"}, hexBytes(seq)...)...).CombinedOutput(); err != nil {
			t.Fatalf("send mouse: %v %s", err, out)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, _ := exec.Command(r.tmux, "-S", outer, "capture-pane", "-p", "-t", "outer").Output()
		if strings.Contains(string(out), "Double-click") && strings.Contains(string(out), "Cerebras.pdf)") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("client never showed the attachment:\n%s", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The client reads mouse reports once it has asked its terminal for them.
	deadline = time.Now().Add(40 * time.Second)
	for {
		out, _ := exec.Command(r.tmux, "-S", outer, "display-message", "-p", "-t", "outer", "#{mouse_any_flag}").Output()
		if strings.TrimSpace(string(out)) == "1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the client never turned mouse reporting on (%s)", time.Since(deadline.Add(-40*time.Second)))
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(r.opened); err == nil {
		t.Fatal("printed mouse sequences opened something")
	}
	// The pane starts below the status line, and terminal cells count from 1.
	x, y := r.at("application-testing-20260927/", 5)
	mouse(0, x+1, y+2)
	mouse(0, x+1, y+2)
	if lines := r.waitOpened(2); filepath.Base(lines[0]) != "Jeremiah_Mackey_Cerebras.pdf" {
		t.Fatalf("opened %q", lines[0])
	}
	// A word: tmux selects and copies it, as without Lectern.
	x, y = r.at("Both", 1)
	mouse(0, x+1, y+2)
	mouse(0, x+1, y+2)
	deadline = time.Now().Add(5 * time.Second)
	for {
		out, _ := exec.Command(r.tmux, "-S", r.plan.socket, "show-buffer").Output()
		if string(out) == "Both" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a double-click on a word no longer selects it: %q", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// A right-click on the path opens the link menu; Escape closes it.
	x, y = r.at("Jeremiah_Mackey", 3)
	mouse(2, x+1, y+2)
	deadline = time.Now().Add(5 * time.Second)
	for {
		out, _ := exec.Command(r.tmux, "-S", outer, "capture-pane", "-p", "-t", "outer").Output()
		if strings.Contains(string(out), "Open on this machine") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no link menu:\n%s", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	exec.Command(r.tmux, "-S", outer, "send-keys", "-t", "outer", "Escape").Run()
	// None of it came from the output that imitated a double-click: the
	// opener ran once, for the real one.
	if lines := r.waitOpened(2); len(lines) != 2 {
		t.Fatalf("opened %q", lines)
	}
}

// hexBytes is s as send-keys -H arguments, one byte each.
func hexBytes(s string) []string {
	parts := make([]string, len(s))
	for i := 0; i < len(s); i++ {
		parts[i] = hex.EncodeToString([]byte{s[i]})
	}
	return parts
}

func TestWrappedRowsFollowTmux(t *testing.T) {
	plain := "short\nabcdefghij\nklm\nnext\n"
	joined := "short\nabcdefghijklm\nnext\n"
	rows := wrappedRows(plain, joined)
	got := []bool{}
	for _, row := range rows {
		got = append(got, row.Wrapped)
	}
	if fmt.Sprint(got) != "[false false true false]" {
		t.Fatalf("wrapped %v", got)
	}
	if cellToRune("日本/a.txt", 4) != 2 || cellToRune("abc", 1) != 1 {
		t.Fatal("cells are not mapped to characters")
	}
}

func TestBoundCommandReadsBackTmuxDefaults(t *testing.T) {
	listing := `bind-key -T root DoubleClick1Pane select-pane -t = \; if-shell -F "#{||:#{pane_in_mode},#{mouse_any_flag}}" { send-keys -M } { copy-mode -H ; send-keys -X select-word ; run-shell -d 0.3 ; send-keys -X copy-pipe-and-cancel }` + "\n"
	command, ok := boundCommand(listing, "DoubleClick1Pane")
	if !ok || !strings.HasPrefix(command, `select-pane -t = ; if-shell -F "#{||`) || strings.Contains(command, `\;`) {
		t.Fatalf("command %q", command)
	}
	if _, ok := boundCommand(listing, "MouseDown3Pane"); ok {
		t.Fatal("found a binding that is not there")
	}
	if got := topLevelSemicolons(`a "x \; y" \; b { c \; d }`); got != `a "x \; y" ; b { c \; d }` {
		t.Fatalf("got %q", got)
	}
	conf := linkBindings("/tmp/a b/link.sh", "/tmp/a b/menu.conf", map[string]string{"DoubleClick1Pane": "DBL", "MouseDown3Pane": "RIGHT"})
	for _, want := range []string{
		`bind-key -T root DoubleClick1Pane if-shell -F "#{pane_in_mode}" { DBL } { if-shell "! '/tmp/a b/link.sh' click #{pane_id} #{mouse_x} #{mouse_y} --hyperlink=#{q:mouse_hyperlink}" { DBL } }`,
		`bind-key -T root MouseDown3Pane if-shell -F "#{pane_in_mode}" { RIGHT } { if-shell "'/tmp/a b/link.sh' menu #{pane_id} #{mouse_x} #{mouse_y} --hyperlink=#{q:mouse_hyperlink}" { source-file "/tmp/a b/menu.conf" } { RIGHT } }`,
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("bindings missing %q:\n%s", want, conf)
		}
	}
}

func TestLinkScriptCarriesNoCredential(t *testing.T) {
	plan := testWrapPlan(t, []string{"tmux", "attach", "-t", "agent"}, "")
	if strings.Contains(plan.linkBody, "scoped-secret") || strings.Contains(plan.linkBody, "LECTERN_AUTH_TOKEN") {
		t.Fatalf("link script carries the credential: %s", plan.linkBody)
	}
	for _, want := range []string{insertSocketEnv + "=" + plan.socket, linkDirEnv + "=" + plan.dir, terminalLinkFlag + " session 17 http://127.0.0.1:9110 \"$@\""} {
		if !strings.Contains(plan.linkBody, want) {
			t.Fatalf("link script is missing %q: %s", want, plan.linkBody)
		}
	}
	// And the attachment inside the private server never sees the credential.
	if !strings.Contains(plan.inner, "-u LECTERN_AUTH_TOKEN") {
		t.Fatalf("inner attachment keeps the credential: %s", plan.inner)
	}
	// Outside an attachment the entry point refuses to run.
	t.Setenv(insertSocketEnv, "")
	if code := terminalLinkCommand([]string{"session", "17", "http://127.0.0.1:9110", "click", "%0", "1", "1"}); code != 2 {
		t.Fatalf("ran without a private server: %d", code)
	}
}

func TestOpeningRefusesRunnableFilesAndCleansNames(t *testing.T) {
	for name, want := range map[string]bool{"report.pdf": true, "a.PNG": true, "notes.md": true, "run.sh": false, "setup.EXE": false, "x.desktop": false, "Makefile": false, "evil.js": false} {
		if safeToOpen(name) != want {
			t.Errorf("safeToOpen(%q) != %v", name, want)
		}
	}
	for in, want := range map[string]string{"Résumé plan.pdf": "Résumé plan.pdf", "..\x1b[2J.pdf": "_[2J.pdf", "": "file", "a:b?.txt": "a_b_.txt"} {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
	link := filelinks.Link{Kind: "file", Path: ownerPDF, External: true}
	e := &linkEnv{kind: "session", id: "17", dir: t.TempDir()}
	if menu := e.menu(link, filepath.Join(e.dir, "link-1.json")); !strings.Contains(menu, `{ run-shell -b "`+filepath.Join(e.dir, "link.sh")+` act copy `) {
		t.Fatalf("menu actions do not run the link script:\n%s", menu)
	}
}
