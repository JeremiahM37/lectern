package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/console"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"golang.org/x/term"
)

// nativeControlsEnv disables the client-side wrapper entirely when it is set to
// 0/off/false. Without it the resolved attachment runs exactly as it always
// has: keys go to the agent and Ctrl-b d detaches.
const nativeControlsEnv = "LECTERN_NATIVE_CONTROLS"

// nativeControls is the identity and endpoint one attached terminal needs to
// open Lectern actions from inside itself. Token is an API credential; it is
// handed only to the private tmux server this client starts and never appears
// in an argument list, a config file, or the status line.
type nativeControls struct {
	Kind    string
	ID      string
	Base    string
	Token   string
	TabView bool
}

func nativeControlsOff() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(nativeControlsEnv))) {
	case "0", "off", "false", "no", "disabled":
		return true
	}
	return false
}

func warnNativeControls(reason string) {
	fmt.Fprintln(os.Stderr, "lectern: native controls unavailable ("+reason+"); keys go straight to the agent, so Ctrl-b d still detaches.")
}

// runAttachment executes a resolved attachment exactly like syscall.Exec did,
// taking the process over so exit status and job control match a direct
// ssh/tmux invocation.
func runAttachment(argv []string, controls *nativeControls) error {
	return startAttachment(argv, controls, true)
}

// startAttachment runs a resolved attachment. replace is true for the CLI,
// which hands the process image to the attachment; it is false for the TUI
// callback, which must wait for the child before Bubble Tea resumes.
func startAttachment(argv []string, controls *nativeControls, replace bool) error {
	if controls == nil || nativeControlsOff() {
		return directAttachment(argv, replace)
	}
	if !interactiveTerminal() {
		warnNativeControls("this is not an interactive terminal")
		return directAttachment(argv, replace)
	}
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		warnNativeControls("tmux is not installed")
		return directAttachment(argv, replace)
	}
	return runPrivateAttachment(tmuxPath, argv, *controls, replace)
}

// directAttachment is the pre-controls behavior: run the attachment command
// directly, wrapped in the workspace popup when the client is itself inside a
// tmux pane.
func directAttachment(argv []string, replace bool) error {
	argv = attachmentInWorkspace(argv, os.Getenv("TMUX"))
	binary, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	if replace {
		return syscall.Exec(binary, argv, os.Environ())
	}
	cmd := exec.Command(binary, argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// runPrivateAttachment runs the attachment on its own tmux server. Ctrl-]
// opens Lectern controls, Ctrl-] Ctrl-] sends a literal prefix to the agent,
// and ordinary Ctrl-b still reaches the agent's own tmux. The server, socket
// and temporary directory belong to this attachment: teardown kills only the
// server bound to this socket and removes this directory, never a shared tmux.
func runPrivateAttachment(tmuxPath string, argv []string, controls nativeControls, replace bool) error {
	dir, err := os.MkdirTemp("", "lectern-attach-")
	if err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	socket := filepath.Join(dir, "sock")
	plan, err := newNativeWrapPlan(dir, socket, controls, argv, os.Getenv("TMUX"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	originalTTY, _ := term.GetState(int(os.Stdin.Fd()))
	cleanup := func() {
		kill := exec.Command(tmuxPath, "-S", socket, "kill-server")
		kill.Stdout, kill.Stderr = io.Discard, io.Discard
		_ = kill.Run()
		_ = os.RemoveAll(dir)
		if originalTTY != nil {
			_ = term.Restore(int(os.Stdin.Fd()), originalTTY)
		}
	}
	defer cleanup()
	stopSignals := watchAttachmentSignals(cleanup)
	defer stopSignals()

	if err := plan.write(); err != nil {
		return err
	}
	if err := plan.start(tmuxPath); err != nil {
		return err
	}
	err = plan.attach(tmuxPath)
	if err != nil && replace {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			cleanup()
			os.Exit(exit.ExitCode())
		}
	}
	return err
}

// watchAttachmentSignals makes sure an interrupted client still tears down only
// its own private server. While attached the tty is in raw mode, so the normal
// detach path is Ctrl-b d, not a signal.
func watchAttachmentSignals(cleanup func()) func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case <-signals:
			cleanup()
			os.Exit(130)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(signals)
		close(done)
	}
}

// nativeWrapPlan is the whole generated private tmux environment for one
// attachment. It is built as data so the quoting and key bindings are testable
// without a tmux server.
type nativeWrapPlan struct {
	dir            string
	socket         string
	session        string
	confPath       string
	innerScript    string
	controlsScript string
	uploadScript   string
	controls       nativeControls
	inWorkspace    string
	innerArgv      []string
	conf           string
	inner          string
	controlsBody   string
	uploadBody     string
}

func newNativeWrapPlan(dir, socket string, controls nativeControls, argv []string, workspace string) (*nativeWrapPlan, error) {
	if len(argv) == 0 {
		return nil, errors.New("attachment command is empty")
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	plan := &nativeWrapPlan{
		dir:            dir,
		socket:         socket,
		session:        "lectern-attach",
		confPath:       filepath.Join(dir, "tmux.conf"),
		innerScript:    filepath.Join(dir, "attach.sh"),
		controlsScript: filepath.Join(dir, "controls.sh"),
		uploadScript:   filepath.Join(dir, "upload.sh"),
		controls:       controls,
		inWorkspace:    workspace,
		innerArgv:      withClientControlsMarker(argv),
	}
	plan.inner = innerScript(plan.innerArgv)
	// Both popups carry the private socket and session so a successful upload
	// can type the returned path into the attached pane no matter whether the
	// operator picked it from the Ctrl-] menu or with the upload shortcut.
	insertEnv := []string{
		insertSocketEnv + "=" + socket,
		insertTargetEnv + "=" + plan.session,
	}
	plan.controlsBody = execScriptWithEnv(insertEnv, []string{self, "controls", controls.Kind, controls.ID, "--popup"})
	plan.uploadBody = execScriptWithEnv(insertEnv, []string{self, "controls", controls.Kind, controls.ID, "--popup", "--action", "upload"})
	plan.conf = plan.tmuxConfig()
	return plan, nil
}

// innerScript runs the resolved attachment with TMUX unset so the client talks
// to the agent's own tmux (or SSH) instead of this wrapper, and with the API
// token removed so a remote or shared session never inherits this client's
// credential. Each argv word is quoted separately, so inner tmux separators
// such as `;` survive as their own argument.
func innerScript(argv []string) string {
	words := make([]string, len(argv))
	for i, word := range argv {
		words[i] = shellq.Quote(word)
	}
	return "#!/bin/sh\nexec env -u TMUX -u LECTERN_AUTH_TOKEN " + strings.Join(words, " ") + "\n"
}

func execScript(argv []string) string {
	words := make([]string, len(argv))
	for i, word := range argv {
		words[i] = shellq.Quote(word)
	}
	return "#!/bin/sh\nexec " + strings.Join(words, " ") + "\n"
}

// execScriptWithEnv runs argv with extra NAME=VALUE entries in the child
// environment. Values are quoted as whole env assignments so a path with
// spaces or shell metacharacters survives as one argument.
func execScriptWithEnv(env []string, argv []string) string {
	words := make([]string, 0, len(env)+len(argv))
	for _, entry := range env {
		words = append(words, shellq.Quote(entry))
	}
	for _, word := range argv {
		words = append(words, shellq.Quote(word))
	}
	return "#!/bin/sh\nexec env " + strings.Join(words, " ") + "\n"
}

func (p *nativeWrapPlan) tmuxConfig() string {
	hint := "#[bold]Ctrl+] m#[default] controls · Ctrl-b d detach "
	if p.controls.TabView {
		hint = "#[bold]Ctrl+] m#[default] controls · Ctrl+] d close tab "
	}
	return strings.Join([]string{
		// This private client wrapper owns scrollback. Without mouse reports,
		// Windows Terminal/xterm translate wheel motion in the alternate screen
		// into Up/Down keys and replace the agent's draft with old prompts.
		// Keep the inner attachment on the wrapper's normal screen so output
		// remains available to tmux copy-mode. Shared agent tmux is untouched.
		"set -g mouse on",
		"set -g history-limit 100000",
		"set -gw alternate-screen off",
		"set -g prefix C-]",
		"bind-key -T prefix C-] send-prefix",
		"bind-key -T prefix m display-popup -E -w 90% -h 85% -T 'Lectern controls' " + shellq.Quote(p.controlsScript),
		"bind-key -T prefix u display-popup -E -w 90% -h 85% -T 'Lectern upload' " + shellq.Quote(p.uploadScript),
		"set -g status on",
		"set -g status-position top",
		"set -g status-left-length 44",
		"set -g status-style fg=colour252,bg=colour236",
		// The primary shortcut leads the row so a narrow client clips trailing
		// text, never the hint itself. The window list is dropped: its text
		// otherwise crowds the hint out on mobile-width terminals.
		"set -g status-left " + shellq.Quote(hint),
		"set -g status-right ''",
		"set -g window-status-format ''",
		"set -g window-status-current-format ''",
		"set -g status-interval 0",
		"set -g escape-time 0",
		"",
	}, "\n")
}

func (p *nativeWrapPlan) write() error {
	files := []struct {
		path string
		body string
		mode os.FileMode
	}{
		{p.confPath, p.conf, 0o600},
		{p.innerScript, p.inner, 0o700},
		{p.controlsScript, p.controlsBody, 0o700},
		{p.uploadScript, p.uploadBody, 0o700},
	}
	for _, file := range files {
		if err := os.WriteFile(file.path, []byte(file.body), file.mode); err != nil {
			return err
		}
	}
	return nil
}

func (p *nativeWrapPlan) start(tmuxPath string) error {
	args := []string{"-S", p.socket, "-f", p.confPath, "new-session", "-d", "-s", p.session, "--", p.innerScript}
	cmd := exec.Command(tmuxPath, args...)
	cmd.Env = p.serverEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if message := strings.TrimSpace(string(out)); message != "" {
			return fmt.Errorf("start private tmux: %w: %s", err, message)
		}
		return fmt.Errorf("start private tmux: %w", err)
	}
	return nil
}

// serverEnv is the scoped child environment: only this attachment's tmux
// server and the popups it starts can see the API base and token.
func (p *nativeWrapPlan) serverEnv() []string {
	env := os.Environ()
	if p.controls.Base != "" {
		env = append(env, "LECTERN_API="+p.controls.Base)
	}
	if p.controls.Token != "" {
		env = append(env, "LECTERN_AUTH_TOKEN="+p.controls.Token)
	}
	return env
}

func (p *nativeWrapPlan) clientArgv() []string {
	return []string{"tmux", "-S", p.socket, "attach", "-t", p.session}
}

func (p *nativeWrapPlan) attach(tmuxPath string) error {
	if p.inWorkspace != "" {
		// The outer popup belongs to the workspace tmux the operator already
		// runs: it keeps that tmux's prefix from stealing Ctrl-b, while the
		// client inside it owns the Ctrl-] controls prefix.
		popup := workspacePopup(p.clientArgv(), p.inWorkspace, "Lectern · Ctrl-] m actions")
		cmd := exec.Command(popup[0], popup[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	cmd := exec.Command(tmuxPath, p.clientArgv()[1:]...)
	cmd.Env = portableTerm(withoutEnv(os.Environ(), "TMUX"), terminfoDirs())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// portableTerm keeps the caller's TERM when this machine can describe it and
// otherwise falls back to xterm-256color, the same type the SSH hop already
// uses. tmux refuses to attach a client whose terminal it has no terminfo
// for ("missing or unsuitable terminal"), which is what a kitty or WezTerm
// user meets on a machine without that terminal's entry installed. The
// fallback only changes how the private tmux draws, not what the agent sees.
func portableTerm(env []string, dirs []string) []string {
	name := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, "TERM=") {
			name = strings.TrimPrefix(entry, "TERM=")
		}
	}
	if name != "" && hasTerminfo(name, dirs) {
		return env
	}
	return append(withoutEnv(env, "TERM"), "TERM=xterm-256color")
}

// terminfoDirs lists where ncurses looks for a compiled entry, in its order.
func terminfoDirs() []string {
	var dirs []string
	if dir := os.Getenv("TERMINFO"); dir != "" {
		dirs = append(dirs, dir)
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".terminfo"))
	}
	if list, ok := os.LookupEnv("TERMINFO_DIRS"); ok {
		for _, dir := range strings.Split(list, ":") {
			if dir == "" {
				dir = "/usr/share/terminfo"
			}
			dirs = append(dirs, dir)
		}
	}
	return append(dirs, "/etc/terminfo", "/lib/terminfo", "/usr/share/terminfo", "/usr/lib/terminfo")
}

// hasTerminfo reports whether any directory holds an entry for name, under
// either the letter directory Linux uses or the hex one macOS uses.
func hasTerminfo(name string, dirs []string) bool {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return false
	}
	first := name[:1]
	hex := strconv.FormatInt(int64(name[0]), 16)
	for _, dir := range dirs {
		for _, sub := range []string{first, hex} {
			if info, err := os.Stat(filepath.Join(dir, sub, name)); err == nil && !info.IsDir() {
				return true
			}
		}
	}
	return false
}

func withoutEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

var controlsActions = map[string]bool{"upload": true, "send": true, "review": true, "rename": true, "history": true}

// The controls popups are started by the private tmux server, which knows the
// socket it bound and the session that hosts the attached terminal. Passing
// both here lets a finished upload type its returned path into the agent's
// pane without guessing at the ambient environment.
const (
	insertSocketEnv = "LECTERN_CONTROLS_INSERT_SOCKET"
	insertTargetEnv = "LECTERN_CONTROLS_INSERT_TARGET"
)

// controlsCommand is the surface behind `lectern controls` and the attached
// terminal's popup. It reuses the dashboard's actions, forms and API client;
// only native attach actions are disabled.
func controlsCommand(c *console.Client, args []string) error {
	kind, rid, action, popup, err := parseControlsArgs(args)
	if err != nil {
		return err
	}
	opts := console.DashboardOptions{Popup: popup, FocusKind: kind, FocusID: rid, Action: action}
	if os.Getenv(insertSocketEnv) != "" && os.Getenv(insertTargetEnv) != "" {
		opts.Insert = insertIntoAttachment
	}
	return console.RunControls(c, os.Stdin, os.Stdout, opts)
}

// insertIntoAttachment types text into the pane the controls popup was opened
// over, without pressing Enter. send-keys -l delivers the bytes verbatim, so a
// quoted path reaches a local tmux attach or an SSH client unchanged.
func insertIntoAttachment(text string) error {
	args, err := insertSendKeysArgs(os.Getenv(insertSocketEnv), os.Getenv(insertTargetEnv), text)
	if err != nil {
		return err
	}
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	return exec.Command(tmuxPath, args...).Run()
}

// insertSendKeysArgs validates an insert request and renders the tmux command
// that types text into the exact attached session on the private socket. A
// missing socket is refused rather than falling back to the operator's ambient
// tmux, and text carrying control bytes is refused before anything is sent:
// send-keys -l writes those bytes literally, so an embedded newline would
// submit the draft and ESC or a C1 control could drive the terminal even
// though shell quoting already neutralised them as shell syntax.
func insertSendKeysArgs(socket, target, text string) ([]string, error) {
	if socket == "" {
		return nil, errors.New("no private tmux socket for insertion")
	}
	if target == "" {
		return nil, errors.New("no attached terminal")
	}
	if r, ok := firstInsertControl(text); ok {
		return nil, fmt.Errorf("refusing to insert text containing control character %s", strconv.QuoteRune(r))
	}
	return []string{"-S", socket, "send-keys", "-l", "-t", exactSessionTarget(target), "--", text}, nil
}

// exactSessionTarget makes tmux match the session by its whole name instead of
// prefix-matching it, and names the session's current window so the target is a
// valid pane. A bare, prefix-compatible session name gets the "=" exact-match
// marker and a trailing colon; a target that already carries either part is
// left untouched.
func exactSessionTarget(target string) string {
	if strings.HasPrefix(target, "=") || strings.ContainsAny(target, ":.") {
		return target
	}
	return "=" + target + ":"
}

// firstInsertControl reports the first control code in text. It covers the C0
// range (including LF, CR and ESC), DEL, and the C1 range, plus any byte that
// is not valid UTF-8 so a raw control byte cannot slip past as a replacement
// rune.
func firstInsertControl(text string) (rune, bool) {
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size == 1 {
			return rune(text[i]), true
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return r, true
		}
		i += size
	}
	return 0, false
}

func parseControlsArgs(args []string) (kind, rid, action string, popup bool, err error) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--popup":
			popup = true
		case args[i] == "--action":
			if i+1 >= len(args) {
				return "", "", "", false, errors.New("usage: lectern controls [KIND ID] [--popup] [--action upload]")
			}
			i++
			action = args[i]
		case strings.HasPrefix(args[i], "-"):
			return "", "", "", false, fmt.Errorf("unknown controls option %s", args[i])
		default:
			if kind == "" {
				kind = args[i]
			} else if rid == "" {
				rid = args[i]
			} else {
				return "", "", "", false, errors.New("usage: lectern controls [KIND ID] [--popup] [--action upload]")
			}
		}
	}
	if kind != "" || rid != "" {
		if err := validateControlsTarget(kind, rid); err != nil {
			return "", "", "", false, err
		}
	}
	if action != "" && !controlsActions[action] {
		return "", "", "", false, fmt.Errorf("unknown controls action %q (try upload, send, review, rename or history)", action)
	}
	return kind, rid, action, popup, nil
}

func validateControlsTarget(kind, rid string) error {
	switch strings.TrimSuffix(kind, "-shell") {
	case "session", "task", "attempt", "project":
	default:
		return fmt.Errorf("controls kind %q is not supported (use session, task, attempt or project)", kind)
	}
	if n, err := strconv.ParseInt(rid, 10, 64); err != nil || n <= 0 {
		return errors.New("controls ID must be a positive integer")
	}
	return nil
}
