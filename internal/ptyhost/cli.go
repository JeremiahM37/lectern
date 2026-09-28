package ptyhost

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// PollEnd ends a batched poll or probe; it is the framing of
// internal/sessions/backend, and a test there keeps the two equal.
const PollEnd = "ADK-POLL-END-v2"

// CLI is `lectern pty …`: the subset of tmux's command language Lectern's
// session code uses, answered by the PTY host, with tmux's output and error
// texts (docs/ptyhost.md §3).
type CLI struct {
	Socket string
	// Bin is the lectern binary that starts a host when a session is made.
	Bin    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Attach runs an interactive attachment; the command layer supplies it
	// because it needs the real terminal.
	Attach func(c *Client, name string) error
}

// exitError carries an exit status and the message tmux would print.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func failf(format string, args ...any) error {
	return &exitError{code: 1, msg: fmt.Sprintf(format, args...)}
}

// Run executes one command and returns its exit status.
func (c *CLI) Run(args []string) int {
	err := c.run(args)
	if err == nil {
		return 0
	}
	code := 1
	var ee *exitError
	if errors.As(err, &ee) {
		code = ee.code
	}
	if msg := err.Error(); msg != "" {
		fmt.Fprintln(c.Stderr, msg)
	}
	return code
}

// opts is a parsed tmux command line: flags with and without values, then
// positional arguments.
type opts struct {
	flags map[byte]bool
	vals  map[byte][]string
	args  []string
}

func (o opts) val(f byte) string {
	if v := o.vals[f]; len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}

// parse reads getopt-style flags; withValue lists the flags that take one.
func parse(args []string, withValue string) (opts, error) {
	o := opts{flags: map[byte]bool{}, vals: map[byte][]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			o.args = append(o.args, args[i+1:]...)
			return o, nil
		}
		if len(a) < 2 || a[0] != '-' {
			o.args = append(o.args, args[i:]...)
			return o, nil
		}
		for j := 1; j < len(a); j++ {
			f := a[j]
			if strings.IndexByte(withValue, f) >= 0 {
				v := a[j+1:]
				if v == "" {
					if i+1 >= len(args) {
						return o, failf("-%c expects an argument", f)
					}
					i++
					v = args[i]
				}
				o.vals[f] = append(o.vals[f], v)
				break
			}
			o.flags[f] = true
		}
	}
	return o, nil
}

// target reads tmux's target syntax: "=name" is exactly name, a trailing
// ":…" names a window or pane of it (a session has one), and a bare name
// may be a unique prefix.
func target(t string) (name string, exact bool) {
	if i := strings.IndexByte(t, ':'); i >= 0 {
		t = t[:i]
	}
	if strings.HasPrefix(t, "=") {
		return t[1:], true
	}
	return t, false
}

func (c *CLI) dial() (*Client, error) {
	cl, err := Dial(c.Socket)
	if IsNotRunning(err) {
		return nil, &exitError{code: 1, msg: "no server running on " + c.Socket}
	}
	return cl, err
}

// resolve finds the session a target names.
func (c *CLI) resolve(cl *Client, t string) (Info, error) {
	name, exact := target(t)
	res, err := cl.Do(Request{Op: "info", Name: name})
	if err != nil {
		return Info{}, err
	}
	if res.OK && res.Info != nil {
		return *res.Info, nil
	}
	if !res.Missing {
		return Info{}, errors.New(res.Error)
	}
	if !exact && name != "" {
		list, err := cl.Do(Request{Op: "list"})
		if err != nil {
			return Info{}, err
		}
		var match []Info
		for _, s := range list.Sessions {
			if strings.HasPrefix(s.Name, name) {
				match = append(match, s)
			}
		}
		if len(match) == 1 {
			return match[0], nil
		}
	}
	return Info{}, failf("can't find session: %s", t)
}

func (c *CLI) run(args []string) error {
	if len(args) == 0 {
		return failf("usage: lectern pty COMMAND [ARGS]")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "new-session", "new":
		return c.newSession(rest)
	case "has-session", "has":
		o, err := parse(rest, "t")
		if err != nil {
			return err
		}
		cl, err := c.dial()
		if err != nil {
			return err
		}
		defer cl.Close()
		_, err = c.resolve(cl, o.val('t'))
		return err
	case "kill-session":
		o, err := parse(rest, "t")
		if err != nil {
			return err
		}
		cl, err := c.dial()
		if err != nil {
			return err
		}
		defer cl.Close()
		in, err := c.resolve(cl, o.val('t'))
		if err != nil {
			return err
		}
		return c.do(cl, Request{Op: "kill", Name: in.Name})
	case "capture-pane", "capturep":
		o, err := parse(rest, "tSEb")
		if err != nil {
			return err
		}
		cl, err := c.dial()
		if err != nil {
			return err
		}
		defer cl.Close()
		in, err := c.resolve(cl, o.val('t'))
		if err != nil {
			return err
		}
		lines := 0
		if s := o.val('S'); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n < 0 {
				lines = -n
			}
		}
		res, err := cl.Do(Request{Op: "capture", Name: in.Name, Lines: lines})
		if err != nil {
			return err
		}
		if !res.OK {
			return responseError(res, o.val('t'))
		}
		_, err = io.WriteString(c.Stdout, res.Text)
		return err
	case "send-keys", "send":
		o, err := parse(rest, "tN")
		if err != nil {
			return err
		}
		cl, err := c.dial()
		if err != nil {
			return err
		}
		defer cl.Close()
		in, err := c.resolve(cl, o.val('t'))
		if err != nil {
			return err
		}
		if o.flags['l'] {
			return c.do(cl, Request{Op: "write", Name: in.Name, Data: []byte(strings.Join(o.args, " "))})
		}
		return c.do(cl, Request{Op: "write", Name: in.Name, Names: o.args})
	case "paste-file":
		// Not a tmux command: tmux's load-buffer + paste-buffer in one, so
		// the text never has to be held between two invocations.
		o, err := parse(rest, "t")
		if err != nil {
			return err
		}
		if len(o.args) != 1 {
			return failf("usage: paste-file -t TARGET FILE")
		}
		data, err := os.ReadFile(o.args[0])
		if err != nil {
			return failf("%v", err)
		}
		cl, err := c.dial()
		if err != nil {
			return err
		}
		defer cl.Close()
		in, err := c.resolve(cl, o.val('t'))
		if err != nil {
			return err
		}
		return c.do(cl, Request{Op: "write", Name: in.Name, Data: data, Paste: true})
	case "display-message", "display":
		o, err := parse(rest, "tcF")
		if err != nil {
			return err
		}
		cl, err := c.dial()
		if err != nil {
			return err
		}
		defer cl.Close()
		in, err := c.resolve(cl, o.val('t'))
		if err != nil {
			return err
		}
		format := strings.Join(o.args, " ")
		if f := o.val('F'); f != "" {
			format = f
		}
		_, err = fmt.Fprintln(c.Stdout, expandFormat(format, formatVars(in)))
		return err
	case "list-sessions", "ls", "list-panes", "lsp":
		o, err := parse(rest, "tFf")
		if err != nil {
			return err
		}
		cl, err := c.dial()
		if err != nil {
			return err
		}
		defer cl.Close()
		res, err := cl.Do(Request{Op: "list"})
		if err != nil {
			return err
		}
		format := o.val('F')
		if format == "" {
			format = "#{session_name}: 1 windows (created #{session_created})"
		}
		for _, in := range res.Sessions {
			fmt.Fprintln(c.Stdout, expandFormat(format, formatVars(in)))
		}
		return nil
	case "set-option", "set":
		o, err := parse(rest, "t")
		if err != nil {
			return err
		}
		if o.flags['w'] || o.flags['s'] || o.flags['g'] || len(o.args) == 0 || !strings.HasPrefix(o.args[0], "@") {
			// Window, server and global options configure tmux itself;
			// the PTY host has nothing they would change.
			return nil
		}
		value := ""
		if len(o.args) > 1 {
			value = o.args[1]
		}
		cl, err := c.dial()
		if err != nil {
			return err
		}
		defer cl.Close()
		in, err := c.resolve(cl, o.val('t'))
		if err != nil {
			return err
		}
		res, err := cl.Do(Request{Op: "set-option", Name: in.Name, Key: o.args[0], Value: value, OnlyIfUnset: o.flags['o']})
		if err != nil {
			return err
		}
		if !res.OK {
			return failf("%s", res.Error)
		}
		return nil
	case "show-options", "show":
		o, err := parse(rest, "t")
		if err != nil {
			return err
		}
		cl, err := c.dial()
		if err != nil {
			return err
		}
		defer cl.Close()
		in, err := c.resolve(cl, o.val('t'))
		if err != nil {
			return err
		}
		for _, key := range o.args {
			if v, ok := in.Options[key]; ok {
				if o.flags['v'] {
					fmt.Fprintln(c.Stdout, v)
				} else {
					fmt.Fprintf(c.Stdout, "%s %s\n", key, v)
				}
			} else if !o.flags['q'] {
				return failf("invalid option: %s", key)
			}
		}
		return nil
	case "show-environment", "showenv":
		o, err := parse(rest, "t")
		if err != nil {
			return err
		}
		cl, err := c.dial()
		if err != nil {
			return err
		}
		defer cl.Close()
		in, err := c.resolve(cl, o.val('t'))
		if err != nil {
			return err
		}
		for _, kv := range in.Env {
			k, _, _ := strings.Cut(kv, "=")
			if len(o.args) == 0 || o.args[0] == k {
				fmt.Fprintln(c.Stdout, kv)
				if len(o.args) > 0 {
					return nil
				}
			}
		}
		if len(o.args) > 0 {
			return failf("unknown variable: %s", o.args[0])
		}
		return nil
	case "if-shell", "if":
		o, err := parse(rest, "t")
		if err != nil {
			return err
		}
		if !o.flags['F'] || len(o.args) < 2 {
			return failf("if-shell: only -F conditions are supported")
		}
		cl, err := c.dial()
		if err != nil {
			return err
		}
		in, err := c.resolve(cl, o.val('t'))
		cl.Close()
		if err != nil {
			return err
		}
		branch := ""
		if truthy(expandFormat(o.args[0], formatVars(in))) {
			branch = o.args[1]
		} else if len(o.args) > 2 {
			branch = o.args[2]
		}
		if branch == "" {
			return nil
		}
		words, err := splitCommand(branch)
		if err != nil {
			return failf("%v", err)
		}
		return c.run(words)
	case "resize-window", "resizew", "resize-pane", "resizep":
		o, err := parse(rest, "txy")
		if err != nil {
			return err
		}
		cols, _ := strconv.Atoi(o.val('x'))
		rows, _ := strconv.Atoi(o.val('y'))
		cl, err := c.dial()
		if err != nil {
			return err
		}
		defer cl.Close()
		in, err := c.resolve(cl, o.val('t'))
		if err != nil {
			return err
		}
		if cols <= 0 {
			cols = in.Cols
		}
		if rows <= 0 {
			rows = in.Rows
		}
		return c.do(cl, Request{Op: "resize", Name: in.Name, Cols: cols, Rows: rows})
	case "attach-session", "attach", "a":
		o, err := parse(rest, "t")
		if err != nil {
			return err
		}
		cl, err := c.dial()
		if err != nil {
			return err
		}
		defer cl.Close()
		in, err := c.resolve(cl, o.val('t'))
		if err != nil {
			return err
		}
		return c.attach(cl, in.Name)
	case "poll":
		return c.poll(rest)
	case "probe":
		return c.probe(rest)
	}
	return failf("unknown command: %s", cmd)
}

func (c *CLI) do(cl *Client, req Request) error {
	res, err := cl.Do(req)
	if err != nil {
		return err
	}
	if !res.OK {
		return responseError(res, req.Name)
	}
	return nil
}

func (c *CLI) attach(cl *Client, name string) error {
	if c.Attach == nil {
		return failf("attach needs a terminal")
	}
	return c.Attach(cl, name)
}

// newSession is tmux's new-session: -d leaves it detached, -A attaches to an
// existing session of that name instead of failing, and a single command
// word is a command line for the shell.
func (c *CLI) newSession(rest []string) error {
	o, err := parse(rest, "scexyF")
	if err != nil {
		return err
	}
	name := o.val('s')
	if name == "" {
		return failf("a session name (-s) is required")
	}
	cl, err := Ensure(c.Socket, c.Bin)
	if err != nil {
		return failf("%v", err)
	}
	defer cl.Close()
	if o.flags['A'] {
		res, err := cl.Do(Request{Op: "info", Name: name})
		if err != nil {
			return err
		}
		if res.OK {
			if o.flags['d'] {
				return nil
			}
			return c.attach(cl, name)
		}
	}
	req := Request{Op: "new", Name: name, Dir: o.val('c'), Env: os.Environ(), SessionEnv: o.vals['e']}
	req.Cols, _ = strconv.Atoi(o.val('x'))
	req.Rows, _ = strconv.Atoi(o.val('y'))
	switch len(o.args) {
	case 0:
		req.Argv = []string{loginShell()}
	case 1:
		req.Shell = o.args[0]
	default:
		req.Argv = o.args
	}
	res, err := cl.Do(req)
	if err != nil {
		return err
	}
	if !res.OK {
		return failf("%s", res.Error)
	}
	if o.flags['d'] {
		return nil
	}
	return c.attach(cl, name)
}

func loginShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return defaultShell()[0]
}

// poll is the batched status capture, in the framing
// sessions.ParsePollSnapshot reads: per session its base64 name, ok/missing,
// and the base64 capture.
func (c *CLI) poll(rest []string) error {
	o, err := parse(rest, "S")
	if err != nil {
		return err
	}
	lines, _ := strconv.Atoi(strings.TrimPrefix(o.val('S'), "-"))
	var cl *Client
	if cl, err = Dial(c.Socket); err != nil && !IsNotRunning(err) {
		return err
	}
	if cl != nil {
		defer cl.Close()
	}
	seen := map[string]bool{}
	for _, name := range o.args {
		if seen[name] {
			continue
		}
		seen[name] = true
		state, text := "missing", ""
		if cl != nil {
			res, err := cl.Do(Request{Op: "capture", Name: name, Lines: lines})
			switch {
			case err != nil:
				state, text = "error", err.Error()
			case res.OK:
				state, text = "ok", res.Text
			case !res.Missing:
				state, text = "error", res.Error
			}
		}
		fmt.Fprintf(c.Stdout, "%s\t%s\t%s\n", b64(name), state, b64(text))
	}
	fmt.Fprintln(c.Stdout, PollEnd)
	return nil
}

// probe is the agent-exit probe, framed like the poll: root arguments,
// foreground command and every process on the terminal, base64 each.
func (c *CLI) probe(names []string) error {
	var probes map[string]Probe
	if cl, err := Dial(c.Socket); err == nil {
		defer cl.Close()
		res, err := cl.Do(Request{Op: "probe", Names: names})
		if err != nil {
			return err
		}
		probes = map[string]Probe{}
		for _, p := range res.Probes {
			probes[p.Name] = p
		}
	} else if !IsNotRunning(err) {
		return err
	}
	for _, name := range names {
		p, ok := probes[name]
		if !ok || !p.Found {
			fmt.Fprintf(c.Stdout, "%s\tmissing\t\t\t\n", b64(name))
			continue
		}
		fmt.Fprintf(c.Stdout, "%s\tok\t%s\t%s\t%s\n", b64(name), b64(p.RootArgs), b64(p.Current), b64(strings.Join(p.TTYArgs, "\n")))
	}
	fmt.Fprintln(c.Stdout, PollEnd)
	return nil
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// splitCommand splits a tmux command line into words, honouring single and
// double quotes and backslashes, as tmux's parser does for the simple
// commands if-shell runs.
func splitCommand(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord, quote := false, byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case quote == '\'':
			if ch == '\'' {
				quote = 0
			} else {
				cur.WriteByte(ch)
			}
		case quote == '"':
			if ch == '"' {
				quote = 0
			} else if ch == '\\' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
			} else {
				cur.WriteByte(ch)
			}
		case ch == '\'' || ch == '"':
			quote, inWord = ch, true
		case ch == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			inWord = true
		case unicode.IsSpace(rune(ch)):
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(ch)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}
