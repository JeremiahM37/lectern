package helpers

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// lec is internal/hooks/lec.py, the agent-side kit a running agent uses to
// file follow-up cards and leave durable project notes. lec.py reads its
// credentials from the env file beside itself, i.e. .lectern/env under the
// directory it was run from as `python3 .lectern/lec.py`; the port reads the
// same file relative to the working directory.
func init() { Register("lec", lecMain) }

// lecUsage is lec.py's docstring, naming this command instead of the script.
const lecUsage = `lectern agent-side kit — lets a running agent file follow-up task cards.
Usage (from inside the worktree):
    %[1]s add-task "title" "detailed prompt" [--dispatch]
    %[1]s add-note "durable fact future agents should know"
Auth comes from .lectern/env (per-attempt token). Stdlib only.
`

// lecCommand is how the usage text names the kit: this binary, as run.
func lecCommand() string { return shellq.Quote(os.Args[0]) + " helper lec" }

func lecMain(argv []string, _ io.Reader, stdout, stderr io.Writer) int {
	dispatch := false
	var args []string
	for _, a := range argv {
		if a == "--dispatch" {
			dispatch = true
			continue
		}
		args = append(args, a)
	}
	if len(args) < 2 || (args[0] != "add-task" && args[0] != "add-note") {
		fmt.Fprintf(stderr, lecUsage+"\n", lecCommand())
		return 2
	}
	env, ok := lecLoadEnv(filepath.Join(".lectern", "env"))
	if !ok {
		fmt.Fprintln(stderr, "lec: .lectern/env is not valid UTF-8")
		return 1
	}
	base, token := strings.TrimRight(env["ADK_URL"], "/"), env["ADK_TOKEN"]
	if base == "" || token == "" {
		fmt.Fprintln(stderr, "lec: missing .lectern/env")
		return 2
	}
	payload := newPyDict()
	var path string
	payload.set("token", token)
	if args[0] == "add-note" {
		path = "/api/hook/notes"
		payload.set("note", args[1])
	} else {
		path = "/api/hook/tasks"
		payload.set("title", args[1])
		prompt := args[1]
		if len(args) > 2 {
			prompt = args[2]
		}
		payload.set("prompt", prompt)
		payload.set("dispatch", dispatch)
	}
	body, err := pyJSONDumps(payload, -1, true)
	if err != nil {
		fmt.Fprintf(stderr, "lec: %v\n", err)
		return 1
	}
	raw, err := urlopen(urllibClient(15*time.Second), "POST", base+path, []byte(body))
	if err != nil {
		if err == errNotHTTP {
			fmt.Fprintf(stderr, "lec: request failed: unknown url type: %s\n", pyStrRepr(base+path))
		} else {
			fmt.Fprintf(stderr, "lec: request failed: %s\n", urllibErrorText(err))
		}
		return 1
	}
	out, err := pyJSONLoadBytes(raw)
	if err != nil {
		fmt.Fprintf(stderr, "lec: request failed: %v\n", err)
		return 1
	}
	key := "task_id"
	if args[0] == "add-note" {
		key = "note_id"
	}
	d, ok := out.(*pyDict)
	var id any
	if ok {
		id, ok = d.get(key)
	}
	if !ok {
		fmt.Fprintf(stderr, "lec: the reply has no %s\n", key)
		return 1
	}
	if args[0] == "add-note" {
		fmt.Fprintf(stdout, "lec: noted (#%s)\n", pyStr(id))
	} else {
		state := "backlog"
		if dispatch {
			state = "dispatched"
		}
		fmt.Fprintf(stdout, "lec: filed task #%s (%s)\n", pyStr(id), state)
	}
	return 0
}

// lecLoadEnv is lec.py's load_env: KEY=VALUE lines, a missing file is empty.
// ok is false when the file is not UTF-8 (an uncaught decode error there).
func lecLoadEnv(path string) (map[string]string, bool) {
	env := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return env, true
	}
	if !utf8.Valid(data) {
		return nil, false
	}
	for _, line := range strings.SplitAfter(pyUniversalNewlines(string(data)), "\n") {
		if !strings.Contains(line, "=") {
			continue
		}
		k, v, _ := strings.Cut(pyStrip(line), "=")
		env[k] = v
	}
	return env, true
}
