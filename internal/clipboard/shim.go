package clipboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The shims are what an agent's wl-paste / xclip resolve to inside a Lectern
// session. They speak exactly the read invocations Claude Code and Codex use
// and nothing else; every other invocation is passed to the real tool when
// this machine has one with a display, or does the harmless thing.

// Env names a session carries (see internal/sessions and internal/agentevents).
const (
	EnvHookURL   = "LECTERN_HOOK_URL"
	EnvHookToken = "LECTERN_HOOK_TOKEN"
	// EnvShimDir names the directory of shims so the real tool can be found
	// behind it.
	EnvShimDir = "LECTERN_CLIPBOARD_SHIMS"
)

// Mode is what an invocation wants.
type Mode int

const (
	ModeOther Mode = iota // not understood: pass through
	ModeList              // list the types on the clipboard
	ModeRead              // read one type
	ModeWrite             // copy (xclip without -o)
)

// Command is a parsed shim invocation.
type Command struct {
	Tool      string // "wl-paste" or "xclip"
	Mode      Mode
	Type      string // MIME type for ModeRead
	Primary   bool   // the primary selection, which a client has no notion of
	NoNewline bool
}

// Parse understands the read invocations of wl-paste and xclip.
func Parse(tool string, args []string) Command {
	switch filepath.Base(tool) {
	case "wl-paste":
		return parseWlPaste(args)
	case "xclip":
		return parseXclip(args)
	}
	return Command{Tool: filepath.Base(tool), Mode: ModeOther}
}

func normalizeType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if t == "text" || t == "string" || t == "utf8_string" || t == "text/plain;charset=utf-8" {
		return "text/plain"
	}
	if t == "image/jpg" {
		return "image/jpeg"
	}
	return t
}

func parseWlPaste(args []string) Command {
	c := Command{Tool: "wl-paste", Mode: ModeRead}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-l" || a == "--list-types":
			c.Mode = ModeList
		case a == "-n" || a == "--no-newline":
			c.NoNewline = true
		case a == "-p" || a == "--primary":
			c.Primary = true
		case a == "-t" || a == "--type":
			if i+1 >= len(args) {
				return Command{Tool: "wl-paste", Mode: ModeOther}
			}
			i++
			c.Type = normalizeType(args[i])
		case strings.HasPrefix(a, "--type="):
			c.Type = normalizeType(strings.TrimPrefix(a, "--type="))
		case a == "-s" || a == "--seat":
			i++
		case strings.HasPrefix(a, "--seat="):
		case len(a) > 2 && a[0] == '-' && a[1] != '-' && strings.Trim(a[1:], "nlp") == "":
			// bundled -nl, -np, ...
			for _, r := range a[1:] {
				switch r {
				case 'l':
					c.Mode = ModeList
				case 'n':
					c.NoNewline = true
				case 'p':
					c.Primary = true
				}
			}
		default:
			// --watch, --version, --help, a bare command: not ours
			return Command{Tool: "wl-paste", Mode: ModeOther}
		}
	}
	if c.Mode == ModeRead && c.Type == "" {
		c.Type = "text/plain"
	}
	return c
}

func parseXclip(args []string) Command {
	c := Command{Tool: "xclip", Mode: ModeWrite}
	out := false
	for i := 0; i < len(args); i++ {
		a := strings.TrimPrefix(args[i], "-")
		switch a {
		case "o", "out":
			out = true
		case "i", "in":
			out = false
		case "selection", "sel":
			if i+1 >= len(args) {
				return Command{Tool: "xclip", Mode: ModeOther}
			}
			i++
			c.Primary = args[i] != "clipboard" && args[i] != "c"
		case "t", "target":
			if i+1 >= len(args) {
				return Command{Tool: "xclip", Mode: ModeOther}
			}
			i++
			c.Type = args[i]
		case "d", "display", "l", "loops", "f", "filter", "r", "rmlastnl", "quiet", "silent", "verbose", "noutf8":
			if a == "d" || a == "display" || a == "l" || a == "loops" {
				i++
			}
		default:
			return Command{Tool: "xclip", Mode: ModeOther}
		}
	}
	switch {
	case !out:
		c.Mode = ModeWrite
	case strings.EqualFold(c.Type, "TARGETS"):
		c.Mode, c.Type = ModeList, ""
	default:
		c.Mode = ModeRead
		c.Type = normalizeType(c.Type)
		if c.Type == "" {
			c.Type = "text/plain"
		}
	}
	return c
}

// Streams are the process's standard streams, injected for tests.
type Streams struct {
	In       io.Reader
	Out, Err io.Writer
}

// Run executes one shim invocation and returns the exit status. env is the
// process environment (KEY=VALUE).
func Run(ctx context.Context, tool string, args []string, env []string, s Streams, client *http.Client) int {
	c := Parse(tool, args)
	get := func(k string) string {
		for i := len(env) - 1; i >= 0; i-- {
			if strings.HasPrefix(env[i], k+"=") {
				return env[i][len(k)+1:]
			}
		}
		return ""
	}
	switch c.Mode {
	case ModeOther:
		return passthrough(c, args, env, s, get(EnvShimDir), false)
	case ModeWrite:
		return passthrough(c, args, env, s, get(EnvShimDir), true)
	}
	if c.Primary {
		return unavailable(c, s)
	}
	if c.Mode == ModeRead && !AllowedType(c.Type) {
		return unavailable(c, s)
	}
	types, data, ok := fetch(ctx, c, get(EnvHookURL), get(EnvHookToken), client)
	if !ok {
		// A machine with a real clipboard of its own (a desktop running its
		// own agent) is better served by it than by "nothing".
		if code, ran := tryReal(c, args, env, s, get(EnvShimDir)); ran {
			return code
		}
		return unavailable(c, s)
	}
	if c.Mode == ModeList {
		if len(types) == 0 {
			return unavailable(c, s)
		}
		fmt.Fprintln(s.Out, strings.Join(types, "\n"))
		return 0
	}
	if len(data) == 0 {
		return unavailable(c, s)
	}
	s.Out.Write(data)
	return 0
}

func unavailable(c Command, s Streams) int {
	if c.Tool == "xclip" {
		if c.Mode == ModeRead {
			fmt.Fprintf(s.Err, "Error: target %s not available\n", c.Type)
		}
		return 1
	}
	if c.Mode == ModeRead && c.Type != "" && c.Type != "text/plain" {
		fmt.Fprintf(s.Err, "Clipboard content is not available as requested type \"%s\"\n", c.Type)
		return 1
	}
	fmt.Fprintln(s.Err, "No selection")
	return 1
}

// fetch asks the server. ok is false for anything but a clean answer.
func fetch(ctx context.Context, c Command, hookURL, token string, client *http.Client) (types []string, data []byte, ok bool) {
	if hookURL == "" || token == "" {
		return nil, nil, false
	}
	if client == nil {
		client = &http.Client{Timeout: 6 * time.Second}
	}
	op := OpRead
	if c.Mode == ModeList {
		op = OpList
	}
	body, _ := json.Marshal(map[string]string{"op": op, "type": c.Type})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(hookURL, "/")+"/clipboard", bytes.NewReader(body))
	if err != nil {
		return nil, nil, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, false
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, DefaultMaxBytes+1))
	if err != nil || len(data) > DefaultMaxBytes {
		return nil, nil, false
	}
	if t := resp.Header.Get("X-Clipboard-Types"); t != "" {
		for _, one := range strings.Split(t, ",") {
			if one = strings.TrimSpace(one); one != "" {
				types = append(types, one)
			}
		}
	}
	if c.Mode == ModeList && len(types) == 0 {
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				types = append(types, line)
			}
		}
	}
	return types, data, true
}

// findReal returns the real tool behind the shim directory, if there is one.
func findReal(tool, shimDir string, env []string) string {
	path := ""
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			path = e[5:]
		}
	}
	for _, d := range filepath.SplitList(path) {
		if d == "" || (shimDir != "" && filepath.Clean(d) == filepath.Clean(shimDir)) {
			continue
		}
		p := filepath.Join(d, tool)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			if self, err := os.Executable(); err == nil {
				if a, _ := filepath.EvalSymlinks(p); a == self {
					continue
				}
			}
			return p
		}
	}
	return ""
}

func hasDisplay(tool string, env []string) bool {
	want := "DISPLAY="
	if tool == "wl-paste" {
		want = "WAYLAND_DISPLAY="
	}
	for _, e := range env {
		if strings.HasPrefix(e, want) && len(e) > len(want) {
			return true
		}
	}
	return false
}

func tryReal(c Command, args, env []string, s Streams, shimDir string) (int, bool) {
	if !hasDisplay(c.Tool, env) {
		return 0, false
	}
	real := findReal(c.Tool, shimDir, env)
	if real == "" {
		return 0, false
	}
	cmd := exec.Command(real, args...)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, s.In, s.Out, s.Err
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), true
		}
		return 0, false
	}
	return 0, true
}

// passthrough serves what the shim does not: the real tool when there is a
// display for it, else a no-op (drain stdin for a write) or a plain failure.
func passthrough(c Command, args, env []string, s Streams, shimDir string, write bool) int {
	if code, ran := tryReal(c, args, env, s, shimDir); ran {
		return code
	}
	if write {
		if s.In != nil {
			io.Copy(io.Discard, s.In)
		}
		return 0
	}
	fmt.Fprintf(s.Err, "%s: not supported in a Lectern session\n", c.Tool)
	return 1
}

// WriteShims installs wl-paste and xclip into dir as scripts that run
// `lectern clipboard shim`. bin is the lectern executable (a stable path
// such as the one on PATH, so a self-update is picked up). Idempotent.
func WriteShims(dir, bin string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, tool := range []string{"wl-paste", "xclip"} {
		body := "#!/bin/sh\n" +
			"# Written by Lectern: reads the clipboard of the device driving this session.\n" +
			"# docs/clipboard.md\n" +
			"export " + EnvShimDir + "=\"" + dir + "\"\n" +
			"exec \"${LECTERN_BIN:-" + bin + "}\" clipboard shim " + tool + " \"$@\"\n"
		path := filepath.Join(dir, tool)
		if cur, err := os.ReadFile(path); err == nil && string(cur) == body {
			continue
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(body), 0o755); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	return nil
}
