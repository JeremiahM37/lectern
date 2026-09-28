package helpers

// Port of configuredHomeScript (internal/sessions/checkpoint_manifest.go):
// discover the agent's configuration directory from the agent process below
// the exact session pane. This deliberately does not use the exporting
// user's HOME.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func init() {
	Register("configured-home", configuredHomeMain)
}

// configuredHomeMain is `configured-home AGENT NAME`: it prints the resolved
// home and exits 0, or exits 1 with no output.
func configuredHomeMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		return crashed(stderr, fmt.Errorf("%w: expected AGENT NAME", errCrash))
	}
	if home, ok := configuredHome(args[0], args[1]); ok {
		fmt.Fprintln(stdout, home)
		return 0
	}
	return 1
}

func configuredHome(agent, name string) (string, bool) {
	// The script's check_output had no timeout; a minute stands in for
	// "until the caller gives up".
	out, err := muxOutput(time.Minute, "display-message", "-p", "-t", "="+name+":", "#{pane_pid}")
	if err != nil {
		return "", false
	}
	root, err := pyIntParse(out)
	if err != nil {
		return "", false
	}
	entries, err := scandir("/proc")
	if err != nil {
		return "", false
	}
	children := map[int64][]int64{}
	for _, e := range entries {
		if !asciiDigits(e.name) {
			continue
		}
		pid, err := strconv.ParseInt(e.name, 10, 64)
		if err != nil {
			continue
		}
		if parent, _, err := procStat(pid); err == nil {
			children[parent] = append(children[parent], pid)
		}
	}
	seen := map[int64]bool{}
	queue := []int64{root}
	for len(queue) > 0 {
		p := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[p] {
			continue
		}
		seen[p] = true
		queue = append(queue, children[p]...)
	}
	pids := make([]int64, 0, len(seen))
	for p := range seen {
		pids = append(pids, p)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	for _, p := range pids {
		if home, ok := homeOf(agent, p); ok {
			return home, true
		}
	}
	return "", false
}

func asciiDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

// homeOf checks one process: its executable (or, for Claude Code, whose
// versioned binary keeps the comm "claude", its comm) must be the agent.
// This only locates the configured home; native identity capture separately
// validates PID/starttime, transcript, workspace and tracking identity.
func homeOf(agent string, pid int64) (string, bool) {
	proc := "/proc/" + strconv.FormatInt(pid, 10)
	exe, err := os.Readlink(proc + "/exe")
	if err != nil {
		return "", false
	}
	comm, err := os.ReadFile(proc + "/comm")
	if err != nil || !utf8.Valid(comm) {
		return "", false
	}
	if basename(exe) != agent && !(agent == "claude" && pyStrip(string(comm)) == "claude") {
		return "", false
	}
	data, err := os.ReadFile(proc + "/environ")
	if err != nil {
		return "", false
	}
	env := map[string]string{}
	for _, line := range bytes.Split(data, []byte{0}) {
		if k, v, ok := bytes.Cut(line, []byte("=")); ok {
			env[dropInvalid(k)] = dropInvalid(v)
		}
	}
	key, dir := "CLAUDE_CONFIG_DIR", ".claude"
	if agent == "codex" {
		key, dir = "CODEX_HOME", ".codex"
	}
	value := env[key]
	if value == "" {
		value = pyJoin(env["HOME"], dir)
	}
	return realpathLoose(expanduser(value)), true
}

// dropInvalid is bytes.decode(errors='ignore').
func dropInvalid(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size <= 1 {
			i += invalidPrefix(b[i:])
			continue
		}
		out.WriteRune(r)
		i += size
	}
	return out.String()
}
