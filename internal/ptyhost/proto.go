// Package ptyhost is Lectern's own terminal keeper: one small process per user
// per machine that owns pseudo-terminals, so agents and shells outlive the
// Lectern server, its restarts and its upgrades, with nothing but the lectern
// binary installed (docs/ptyhost.md).
//
// The host keeps each session's screen in a terminal emulator, so a client can
// read it back (capture), and any number of clients can attach to the live
// stream. Clients reach it over a Unix socket (an AF_UNIX socket on Windows
// too) in a directory only the user can enter.
package ptyhost

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Protocol is the version this build speaks; MinProtocol the oldest it still
// accepts. New operations are additive and announced in the handshake, so a
// newer client can keep using an older host for everything that host knows.
const (
	Protocol    = 1
	MinProtocol = 1
	helloName   = "lectern-ptyhost"
)

// Frame kinds. A frame is one kind byte, a 4-byte big-endian length, and the
// payload.
const (
	frameJSON   byte = 'J' // a request or a response
	frameOutput byte = 'O' // host → attached client: terminal output
	frameInput  byte = 'I' // attached client → host: keyboard input
	frameResize byte = 'R' // attached client → host: {"cols","rows"}
	frameExit   byte = 'X' // host → attached client: the session ended
)

// maxFrame bounds a frame so a confused peer cannot make either side allocate
// without limit.
const maxFrame = 64 << 20

func writeFrame(w io.Writer, kind byte, payload []byte) error {
	if len(payload) > maxFrame {
		return fmt.Errorf("frame of %d bytes is too large", len(payload))
	}
	head := make([]byte, 5, 5+len(payload))
	head[0] = kind
	binary.BigEndian.PutUint32(head[1:], uint32(len(payload)))
	_, err := w.Write(append(head, payload...))
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n > maxFrame {
		return 0, nil, fmt.Errorf("frame of %d bytes is too large", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return head[0], payload, nil
}

func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFrame(w, frameJSON, b)
}

func readJSON(r io.Reader, v any) error {
	kind, payload, err := readFrame(r)
	if err != nil {
		return err
	}
	if kind != frameJSON {
		return fmt.Errorf("expected a control frame, got %q", kind)
	}
	return json.Unmarshal(payload, v)
}

// hello opens every connection, in both directions.
type hello struct {
	Hello    string   `json:"hello"`
	Protocol int      `json:"protocol"`
	Min      int      `json:"min"`
	Ops      []string `json:"ops,omitempty"`
	Build    string   `json:"build,omitempty"`
	PID      int      `json:"pid,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// Operations a host of this build supports.
var hostOps = []string{"new", "list", "info", "kill", "write", "capture", "resize",
	"set-option", "attach", "probe", "shutdown"}

// Request is one operation. Unused fields are omitted.
type Request struct {
	Op   string `json:"op"`
	Name string `json:"name,omitempty"`

	// new: Argv runs as it is; Shell, when Argv is empty, is one command
	// line for the host's shell.
	Argv       []string `json:"argv,omitempty"`
	Shell      string   `json:"shell,omitempty"`
	Dir        string   `json:"dir,omitempty"`
	Env        []string `json:"env,omitempty"`         // the program's whole environment
	SessionEnv []string `json:"session_env,omitempty"` // -e entries, read back by show-environment
	Cols       int      `json:"cols,omitempty"`
	Rows       int      `json:"rows,omitempty"`

	// write: Data is typed as it is; with Paste it is bracketed when the
	// program asked for bracketed paste, as tmux's paste-buffer -p does.
	Data  []byte `json:"data,omitempty"`
	Paste bool   `json:"paste,omitempty"`

	// capture
	Lines int `json:"lines,omitempty"`

	// set-option
	Key         string `json:"key,omitempty"`
	Value       string `json:"value,omitempty"`
	OnlyIfUnset bool   `json:"only_if_unset,omitempty"`

	// probe
	Names []string `json:"names,omitempty"`

	// shutdown
	Force bool `json:"force,omitempty"`
}

// Response answers a Request.
type Response struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Missing bool   `json:"missing,omitempty"` // no such session

	Info     *Info   `json:"info,omitempty"`
	Sessions []Info  `json:"sessions,omitempty"`
	Text     string  `json:"text,omitempty"`
	Set      bool    `json:"set,omitempty"`
	Probes   []Probe `json:"probes,omitempty"`
}

// Info describes one session.
type Info struct {
	Name     string            `json:"name"`
	ID       int               `json:"id"`
	Created  int64             `json:"created"`
	Activity int64             `json:"activity"`
	PID      int               `json:"pid"`
	TTY      string            `json:"tty,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	Current  string            `json:"current,omitempty"` // the foreground command
	StartDir string            `json:"start_dir,omitempty"`
	Cols     int               `json:"cols"`
	Rows     int               `json:"rows"`
	Clients  int               `json:"clients"`
	Options  map[string]string `json:"options,omitempty"`
	Env      []string          `json:"env,omitempty"`
}

// Probe is what the agent-exit probe needs about one session: the arguments
// of its root process and its foreground command, and the arguments of every
// process on its terminal.
type Probe struct {
	Name     string   `json:"name"`
	Found    bool     `json:"found"`
	RootArgs string   `json:"root_args,omitempty"`
	Current  string   `json:"current,omitempty"`
	TTYArgs  []string `json:"tty_args,omitempty"`
}

type resize struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}
