package helpers

// Port of internal/nativeidentity/native_identity.py: read-only native
// identity evidence from the selected session pane, on Linux. Everywhere
// else (no /proc) the answer is "unavailable", as it was in Python.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func init() {
	Register("native-identity", nativeIdentityMain)
}

// nativeIdentityMain is `native-identity AGENT WORKSPACE HOME NAME EXPECTED
// [discovery]`: print(json.dumps(native_identity(...))). A non-empty sixth
// argument is discovery=True.
func nativeIdentityMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 5 {
		fmt.Fprintln(stderr, "usage: native-identity AGENT WORKSPACE HOME NAME EXPECTED [discovery]")
		return 2
	}
	discovery := len(args) > 5 && args[5] != ""
	out, err := nativeIdentity(args[0], args[1], args[2], args[3], args[4], discovery)
	if err != nil {
		return crashed(stderr, err)
	}
	fmt.Fprintln(stdout, jsonDumps(out, true))
	return 0
}

// crashed reports an exception the Python script would not have caught.
func crashed(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "Traceback (most recent call last):\n  %v\n", err)
	return 1
}

var trackingHex = regexp.MustCompile(`^[a-f0-9]{32}$`)

// errUnknown is any failure the script turned into state=unavailable.
var errUnknown = errors.New("unavailable")

// nativeIdentity is native_identity(agent, workspace, home, name, expected,
// discovery). Its only error is errCrash-like: an exception Python let
// escape.
func nativeIdentity(agent, workspace, home, name, expected string, discovery bool) (*pyDict, error) {
	unknown := func() *pyDict { return dict("state", "unavailable") }
	if name == "" || (expected != "" && !trackingHex.MatchString(expected)) {
		return unknown(), nil
	}
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		return unknown(), nil
	}
	workspace = realpathLoose(workspace)
	if home == "" {
		key := "CLAUDE_CONFIG_DIR"
		if agent == "codex" {
			key = "CODEX_HOME"
		}
		home = os.Getenv(key)
	}
	if home == "" {
		if agent == "codex" {
			home = expanduser("~/.codex")
		} else {
			home = expanduser("~/.claude")
		}
	}
	out, err := probeIdentity(agent, workspace, home, name, expected, discovery)
	if errors.Is(err, errCrash) {
		return nil, err
	}
	if err != nil {
		return unknown(), nil
	}
	return out, nil
}

// procStat is process(pid): the parent pid and start time from
// /proc/PID/stat. comm may contain spaces or parentheses, so fields are
// read after its final ')'.
func procStat(pid int64) (int64, string, error) {
	data, err := os.ReadFile("/proc/" + strconv.FormatInt(pid, 10) + "/stat")
	if err != nil {
		return 0, "", err
	}
	if !utf8.Valid(data) {
		return 0, "", errUnknown
	}
	i := strings.LastIndex(string(data), ")")
	if i < 0 {
		return 0, "", errUnknown
	}
	fields := pySplit(string(data[i+1:]))
	if len(fields) < 20 {
		return 0, "", errUnknown
	}
	ppid, err := pyIntParse(fields[1])
	if err != nil {
		return 0, "", err
	}
	return ppid, fields[19], nil
}

// processEnv is one variable from /proc/PID/environ, or fallback.
func processEnv(pid int64, key, fallback string) string {
	data, err := os.ReadFile("/proc/" + strconv.FormatInt(pid, 10) + "/environ")
	if err != nil {
		return fallback
	}
	for _, item := range bytes.Split(data, []byte{0}) {
		if bytes.HasPrefix(item, []byte(key+"=")) {
			value := bytes.SplitN(item, []byte("="), 2)[1]
			if !utf8.Valid(value) {
				return fallback
			}
			return string(value)
		}
	}
	return fallback
}

// muxOutput runs a tmux-language command against this machine's session
// backend, as subprocess.check_output(..., timeout, text=True) did.
func muxOutput(timeout time.Duration, args ...string) (string, error) {
	cmd := Mux(args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return "", err
		}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return "", errors.New("timed out")
	}
	if !utf8.Valid(out.Bytes()) {
		return "", errUnknown
	}
	// Text mode translates any newline convention to '\n'.
	text := strings.ReplaceAll(strings.ReplaceAll(out.String(), "\r\n", "\n"), "\r", "\n")
	return text, nil
}

const paneFormat = "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}\t#{?#{@lectern-tracking-identity},#{@lectern-tracking-identity},#{@agentdeck-tracking-identity}}"

func readPane(name string) ([]string, error) {
	text, err := muxOutput(2*time.Second, "display-message", "-p", "-t", "="+name+":", paneFormat)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(text, "\n"), "\t"), nil
}

// readJSONDocument is document(path): a regular file of at most 2 MiB,
// parsed as text. nil with no error is None.
func readJSONDocument(path string) (any, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 2*1024*1024 {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, errJSON
	}
	return jsonLoadsStr(string(data))
}

func probeIdentity(agent, workspace, home, name, expected string, discovery bool) (*pyDict, error) {
	before, err := readPane(name)
	if err != nil {
		return nil, err
	}
	if len(before) != 5 || (expected != "" && before[4] != expected) {
		return dict("state", "changed"), nil
	}
	// Older active records have no persisted marker. Their current pane can
	// still be observed read-only; do not claim it is the original session
	// or retain a binding after this observation.
	root, err := pyIntParse(before[3])
	if err != nil {
		return nil, err
	}
	_, rootStart, err := procStat(root)
	if err != nil {
		return nil, err
	}
	bootData, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || !utf8.Valid(bootData) {
		return nil, errUnknown
	}
	bootID := pyStrip(string(bootData))
	// Snapshot ancestry once, and verify each evidence-bearing process again.
	type proc struct {
		parent int64
		start  string
	}
	census := map[int64]proc{}
	for _, e := range pathlibWildcard("/proc", func(n string) bool { return n != "" && n[0] >= '0' && n[0] <= '9' }, true) {
		if !lexists(e.path + "/stat") {
			continue
		}
		pid, err := pyIntParse(e.name)
		if err != nil {
			continue
		}
		if parent, start, err := procStat(pid); err == nil {
			census[pid] = proc{parent, start}
		}
	}
	members := map[int64]bool{root: true}
	closed := false
	for i := 0; i < 32; i++ {
		found := map[int64]bool{}
		for pid, p := range census {
			if members[p.parent] {
				found[pid] = true
			}
		}
		grew := false
		for pid := range found {
			if !members[pid] {
				grew = true
				members[pid] = true
			}
		}
		if !grew {
			closed = true
			break
		}
		if len(members) > 256 {
			return nil, errUnknown
		}
	}
	if !closed {
		return nil, errUnknown
	}
	// Python iterates a set here; ascending pid order is the closest
	// deterministic equivalent (it only matters when one conversation is
	// proven by two processes, and then only for which pid is reported).
	pids := make([]int64, 0, len(members))
	for pid := range members {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	candidates := map[string]bool{}
	var order []string
	evidence := map[string]*pyDict{}
	record := func(cid string, pid int64, start, actualCwd, nativeHome string) {
		if !candidates[cid] {
			order = append(order, cid)
		}
		candidates[cid] = true
		evidence[cid] = dict("pid", pid, "proc_start", start, "workspace", actualCwd, "native_home", nativeHome,
			"boot_id", bootID, "root_pid", root, "root_start", rootStart, "pane_id", before[2])
	}
	for _, pid := range pids {
		p, ok := census[pid]
		if !ok {
			continue
		}
		start := p.start
		proc := "/proc/" + strconv.FormatInt(pid, 10)
		switch agent {
		case "claude":
			configDir := processEnv(pid, "CLAUDE_CONFIG_DIR", home)
			v, err := readJSONDocument(pathStr(configDir, "sessions", strconv.FormatInt(pid, 10)+".json"))
			if err != nil {
				continue
			}
			row, ok := v.(*pyDict)
			if !ok {
				continue
			}
			if !pyEqualInt(row.getOr("pid", nil), pid) || pyStr(row.getOr("procStart", nil)) != start {
				continue
			}
			if !strIn(row.getOr("kind", nil), "interactive") || !strIn(row.getOr("entrypoint", nil), "cli") {
				continue
			}
			if domain := row.getOr("pidDomain", nil); truthy(domain) {
				machine, err := os.ReadFile("/etc/machine-id")
				if err != nil || !utf8.Valid(machine) {
					return nil, errUnknown
				}
				ns, err := os.Readlink(proc + "/ns/pid")
				if err != nil {
					return nil, err
				}
				if !strIn(domain, "linux:"+pyStrip(string(machine))+":"+ns) {
					continue
				}
			}
			link, err := os.Readlink(proc + "/cwd")
			if err != nil {
				continue
			}
			actualCwd := realpathLoose(link)
			if !discovery {
				cwd, ok := row.getOr("cwd", "").(string)
				if !ok {
					return nil, errUnknown // realpath(non-str) is a TypeError
				}
				if realpathLoose(cwd) != workspace {
					continue
				}
			}
			cid, ok := row.getOr("sessionId", "").(string)
			if !ok || !nativeUUID.MatchString(cid) {
				continue
			}
			_, now, err := procStat(pid)
			if err != nil {
				return nil, err
			}
			if now == start {
				record(cid, pid, start, actualCwd, realpathLoose(pathStr(configDir)))
			}
		case "codex":
			// A shell tool opening history is not the native agent.
			exe, err := realpath(proc+"/exe", true)
			if err != nil || basename(exe) != "codex" {
				continue
			}
			codexHome := processEnv(pid, "CODEX_HOME", home)
			base := realpathLoose(pathStr(codexHome, "sessions"))
			for _, fd := range pathlibWildcard(proc+"/fd", nil, false) {
				cid, actualCwd, err := codexTranscriptID(fd.path, base, proc, workspace, start, pid, discovery)
				if errors.Is(err, errCrash) {
					return nil, err
				}
				if err != nil || cid == "" {
					continue
				}
				record(cid, pid, start, actualCwd, realpathLoose(pathStr(codexHome)))
			}
		}
	}
	after, err := readPane(name)
	if err != nil {
		return nil, err
	}
	if strings.Join(after, "\t") != strings.Join(before, "\t") || len(after) != len(before) {
		return dict("state", "changed"), nil
	}
	if _, now, err := procStat(root); err != nil {
		return nil, err
	} else if now != rootStart {
		return dict("state", "changed"), nil
	}
	if len(candidates) > 1 || len(evidence) > 1 {
		return dict("state", "ambiguous"), nil
	}
	if len(candidates) == 0 {
		return nil, errUnknown
	}
	cid := order[0]
	if discovery {
		ev := evidence[cid]
		if ev == nil {
			ev = newDict()
		}
		return dict("state", "identified", "id", cid, "evidence", ev), nil
	}
	return dict("state", "identified", "id", cid), nil
}

// codexTranscriptID checks one open file descriptor of a codex process and
// returns the conversation id and the process's working directory when it
// is that process's interactive transcript; "" or an error means it is not.
func codexTranscriptID(fd, base, proc, workspace, start string, pid int64, discovery bool) (string, string, error) {
	path, err := realpath(fd, true)
	if err != nil {
		return "", "", err
	}
	if !isUnder(base, path) || suffix(path) != ".jsonl" {
		return "", "", nil
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return "", "", err
	}
	f, err := openPy(path)
	if err != nil {
		return "", "", err
	}
	line, err := f.readline(2*1024*1024 + 1)
	f.Close()
	if err != nil || len(line) > 2*1024*1024 {
		return "", "", err
	}
	v, err := jsonLoadsBytes(line)
	if err != nil {
		return "", "", err
	}
	row, ok := v.(*pyDict)
	if !ok {
		// row.get on a non-dict raised AttributeError, which nothing caught.
		return "", "", fmt.Errorf("%w: AttributeError reading %s", errCrash, path)
	}
	meta, ok := row.getOr("payload", newDict()).(*pyDict)
	if !strIn(row.getOr("type", nil), "session_meta") || !ok {
		return "", "", nil
	}
	// Codex launched by the VS Code extension writes the same interactive
	// JSONL transcript and keeps it open on the real codex process. Both
	// normal CLI and VS Code launches are valid; source alone is never
	// sufficient, and subagents that do not hold the transcript FD remain
	// excluded.
	link, err := os.Readlink(proc + "/cwd")
	if err != nil {
		return "", "", err
	}
	if !strIn(meta.getOr("source", nil), "cli", "vscode") {
		return "", "", nil
	}
	if !discovery {
		cwd, ok := meta.getOr("cwd", "").(string)
		if !ok || realpathLoose(cwd) != workspace {
			return "", "", nil
		}
	}
	cid, ok := meta.getOr("id", "").(string)
	if !ok || !nativeUUID.MatchString(cid) {
		return "", "", nil
	}
	again, err := realpath(fd, true)
	if err != nil || again != path {
		return "", "", err
	}
	_, now, err := procStat(pid)
	if err != nil || now != start {
		return "", "", err
	}
	return cid, realpathLoose(link), nil
}
