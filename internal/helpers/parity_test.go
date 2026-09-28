package helpers

// Parity tests run each replaced Python script and its Go port on the same
// fixtures and require byte-identical stdout and the same exit status.

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/nativeidentity"
)

func requirePython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
}

// runPython runs `python3 -c script args...` in the test's environment.
func runPython(t *testing.T, script string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("python3", append([]string{"-c", script}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("python3: %v", err)
	}
	if code != 0 && code != 1 && code != 2 {
		t.Fatalf("python3 exited %d: %s", code, errb.String())
	}
	return out.String(), code
}

// runGo runs a registered helper in-process.
func runGo(t *testing.T, name string, args ...string) (string, int) {
	t.Helper()
	fn, ok := Lookup(name)
	if !ok {
		t.Fatalf("helper %q is not registered", name)
	}
	var out, errb bytes.Buffer
	code := fn(args, strings.NewReader(""), &out, &errb)
	return out.String(), code
}

// same asserts the Python script and the Go helper agree exactly.
func same(t *testing.T, label, script, helper string, pyArgs, goArgs []string) string {
	t.Helper()
	pyOut, pyCode := runPython(t, script, pyArgs...)
	goOut, goCode := runGo(t, helper, goArgs...)
	if pyOut != goOut || pyCode != goCode {
		t.Errorf("%s: output differs\npython (exit %d):\n%s\ngo (exit %d):\n%s", label, pyCode, pyOut, goCode, goOut)
	}
	return goOut
}

func writeFile(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func lines(rows ...string) string { return strings.Join(rows, "\n") + "\n" }

// at is a fixed file time with a sub-second part, so float formatting of
// st_mtime is exercised.
func at(sec int64) time.Time { return time.Unix(1790000000+sec, 123456789) }

const (
	cidA    = "aaaaaaaa-1111-4111-8111-111111111111"
	cidB    = "bbbbbbbb-2222-4222-8222-222222222222"
	cidC    = "cccccccc-3333-4333-8333-333333333333"
	cidD    = "dddddddd-4444-4444-8444-444444444444"
	cidLong = "eeeeeeee-5555-4555-8555-555555555555"
	cidX    = "ffffffff-6666-4666-8666-666666666666"
	cidY    = "abcdef01-7777-4777-8777-777777777777"
	cidZ    = "abcdef02-8888-4888-8888-888888888888"
)

// nativeFixture is a HOME with Claude and Codex histories for one workspace
// (reached through a symlink), plus conversations that must be excluded.
type nativeFixture struct {
	root, ws, wsLink, claude, codex, claudeDir string
}

func newNativeFixture(t *testing.T) *nativeFixture {
	t.Helper()
	root := t.TempDir()
	f := &nativeFixture{root: root, ws: filepath.Join(root, "work space"), claude: filepath.Join(root, "claude"), codex: filepath.Join(root, "codex")}
	if err := os.MkdirAll(f.ws, 0o755); err != nil {
		t.Fatal(err)
	}
	f.wsLink = filepath.Join(root, "link")
	if err := os.Symlink(f.ws, f.wsLink); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CLAUDE_CONFIG_DIR", f.claude)
	t.Setenv("CODEX_HOME", f.codex)
	t.Setenv("LECTERN_NATIVE_SEARCH_CACHE", "")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	ws := jsonStr(f.ws)
	f.claudeDir = filepath.Join(f.claude, "projects", slugUnsafe.ReplaceAllString(f.ws, "-"))
	long := strings.Repeat("Ω long line with words ", 3200)
	writeFile(t, filepath.Join(f.claudeDir, cidA+".jsonl"), lines(
		`{"type":"summary","summary":"ignored"}`,
		`[1,2,3]`,
		`not json at all`,
		`{"type":"user","sessionId":"`+cidA+`","cwd":`+ws+`,"timestamp":"2026-09-01T10:00:00Z","message":{"role":"user","content":"  Hello — unicode ✓ and emoji 😀\nsecond line with \"quotes\" and \\ backslash  "}}`,
		`{"type":"assistant","timestamp":"2026-09-01T10:00:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private plan"},{"type":"text","text":"Sure, searching for needle."},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls -la","nested":{"f":1.5,"tiny":1e-7,"big":123456789012345678901234567890,"neg":-0.0,"e":1E+22,"u":"é ","list":[true,false,null]},"z":1}}]}}`,
		`{"type":"user","timestamp":12345,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"total 0\nneedle here"}],"is_error":true}]}}`,
		`{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":"sidechain needle"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"image","source":{}},{"type":"text","text":"what is this picture"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":{"odd":"name's"},"input":"raw string input"}]}}`,
		`{"type":"assistant","message":{"role":"system","content":"system text is hidden"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"`+long+`"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"plain result"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"trailing é needle"},"timestamp":{"nested":[1,2.50]}}`,
	)+`{"type":"assistant","message":{"role":"assistant","content":"partial write`, at(10))
	writeFile(t, filepath.Join(f.claudeDir, cidB+".jsonl"), lines(
		`{"type":"user","sessionId":"`+cidB+`","cwd":"/somewhere/else","message":{"role":"user","content":"other workspace needle"}}`,
	), at(20))
	writeFile(t, filepath.Join(f.claudeDir, cidC+".jsonl"), lines(
		`{"type":"user","cwd":`+ws+`,"message":{"role":"user","content":"no session id"}}`,
	), at(30))
	writeFile(t, filepath.Join(f.claudeDir, ".hidden-"+cidD+".jsonl"), lines(
		`{"type":"user","sessionId":"`+cidD+`","cwd":`+ws+`,"message":{"role":"user","content":"hidden file needle"}}`,
	), at(40))
	writeFile(t, filepath.Join(f.claudeDir, cidLong+".jsonl"), lines(
		`{"type":"user","sessionId":"`+cidLong+`","cwd":`+jsonStr(f.wsLink)+`,"message":{"role":"user","content":[{"type":"text","text":"`+long+` needle"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"short reply"}}`,
	), at(5))
	writeFile(t, filepath.Join(f.claude, "projects", "hashed-dir", cidY+".jsonl"), lines(
		`{"type":"user","sessionId":"`+cidY+`","cwd":`+ws+`,"message":{"role":"user","content":"stored under another name"}}`,
	), at(6))

	sessions := filepath.Join(f.codex, "sessions", "2026", "09", "28")
	writeFile(t, filepath.Join(sessions, "rollout-2026-09-28T10-00-00-"+cidX+".jsonl"), lines(
		`{"type":"session_meta","timestamp":"2026-09-28T10:00:00Z","payload":{"id":"`+cidX+`","cwd":`+ws+`,"source":"cli"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"developer rules"}]}}`,
		`{"type":"response_item","timestamp":"t1","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Find the needle, please"}]}}`,
		`{"type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking about needles"}]}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":[\"rg\",\"needle\"]}","call_id":"call_1"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"found needle in haystack"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","input":{"patch":"*** Begin"},"id":"ct_1"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"ct_1","output":{"ok":true},"is_error":0}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","channel":"analysis","content":[{"type":"output_text","text":"hidden analysis"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","channel":"final","content":[{"type":"output_text","text":"Done: needle found."}]}}`,
		`{"type":"session_meta","payload":{"id":"`+cidZ+`","cwd":"/elsewhere"}}`,
		`{"type":"event_msg","payload":{"type":"token_count"}}`,
	), at(50))
	writeFile(t, filepath.Join(sessions, "rollout-2026-09-28T11-00-00-"+cidZ+".jsonl"), lines(
		`{"type":"session_meta","payload":{"id":"`+cidZ+`","cwd":"/elsewhere","source":"cli"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"elsewhere needle"}]}}`,
	), at(60))
	writeFile(t, filepath.Join(f.codex, "session_index.jsonl"), lines(
		`{"id":"`+cidX+`","thread_name":"Needle hunt ✓"}`,
		`{"id":["unhashable"],"thread_name":"x"}`,
		`broken`,
		`{"id":"`+cidZ+`","thread_name":5}`,
	), time.Time{})
	return f
}

func jsonStr(s string) string { return jsonDumps(s, true) }

func conversationsScript() string {
	return nativeidentity.RecordsScript + "\n" + nativeidentity.IdentityScript + "\n" + nativeidentity.ConversationsScript
}

func TestConversationsParity(t *testing.T) {
	requirePython(t)
	f := newNativeFixture(t)
	script := conversationsScript()
	for _, agent := range []string{"claude", "codex", "gemini"} {
		for _, ws := range []string{f.ws, f.wsLink, filepath.Join(f.root, "missing")} {
			args := []string{agent, ws, "", "", "", ""}
			same(t, agent+" list "+ws, script, "native-conversations", args, args)
			args = []string{agent, ws, "", ""}
			same(t, agent+" list, no identity args "+ws, script, "native-conversations", args, args)
		}
	}
	cases := [][]string{
		{"claude", f.ws, cidA, ""},
		{"claude", f.ws, cidA, "500"},
		{"claude", f.ws, cidA, "0"},
		{"claude", f.ws, cidA, " 1_000 "},
		{"claude", f.ws, cidA, "-5"},
		{"claude", f.ws, cidA, "abc"},
		{"claude", f.ws, cidLong, ""},
		{"claude", f.ws, cidB, ""},
		{"claude", f.ws, cidD, ""},
		{"claude", f.ws, "not-a-uuid", ""},
		{"codex", f.ws, cidX, ""},
		{"codex", f.ws, cidX, "99999999"},
		{"codex", f.ws, cidZ, ""},
	}
	for _, args := range cases {
		same(t, strings.Join(args, " "), script, "native-conversations", args, args)
	}
	// An unset or empty profile variable.
	t.Setenv("CODEX_HOME", "")
	same(t, "empty CODEX_HOME", script, "native-conversations", []string{"codex", f.ws, "", ""}, []string{"codex", f.ws, "", ""})
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	same(t, "unset CLAUDE_CONFIG_DIR", script, "native-conversations", []string{"claude", f.ws, "", ""}, []string{"claude", f.ws, "", ""})
	// Too few arguments crashes both.
	same(t, "too few arguments", script, "native-conversations", []string{"claude"}, []string{"claude"})
}

func TestConversationLiveParity(t *testing.T) {
	requirePython(t)
	f := newNativeFixture(t)
	script := nativeidentity.RecordsScript + "\n" + nativeidentity.ConversationLiveScript
	cases := [][]string{
		{"claude", f.ws, cidA, ""},
		{"claude", f.wsLink, cidA, "0"},
		{"claude", f.ws, cidA, "700"},
		{"claude", f.ws, cidA, "99999999"},
		{"claude", f.ws, cidA, "x"},
		{"claude", f.ws, cidLong, ""},
		{"claude", f.ws, cidB, ""},
		{"claude", f.ws, "nope", ""},
		{"codex", f.ws, cidX, ""},
		{"codex", f.ws, cidX, "0"},
		{"codex", f.ws, cidZ, ""},
		{"gemini", f.ws, cidA, ""},
	}
	for _, args := range cases {
		same(t, strings.Join(args, " "), script, "native-conversation-live", args, args)
	}
}

func TestNativeIdentityWithoutPaneParity(t *testing.T) {
	requirePython(t)
	f := newNativeFixture(t)
	for _, c := range []struct{ name, expected string }{{"", ""}, {"", strings.Repeat("a", 32)}, {"lec-x", "not-hex"}, {"lec-x", strings.Repeat("A", 32)}} {
		for _, discovery := range []bool{false, true} {
			flag, goFlag := "False", ""
			if discovery {
				flag, goFlag = "True", "1"
			}
			script := identityCall(f.ws, "", c.name, c.expected, "claude", flag)
			same(t, "identity "+c.name+"/"+c.expected, script, "native-identity", nil, []string{"claude", f.ws, "", c.name, c.expected, goFlag})
		}
	}
}

// identityCall is the Python invocation CaptureNativeEvidence builds.
func identityCall(ws, home, name, expected, agent, flag string) string {
	args := []string{jsonStr(agent), jsonStr(ws), jsonStr(home), jsonStr(name), jsonStr(expected)}
	return nativeidentity.RecordsScript + "\n" + nativeidentity.IdentityScript + "\nimport json\nprint(json.dumps(native_identity(" + strings.Join(args, ",") + "," + flag + ")))"
}

func TestClaudeForkPathParity(t *testing.T) {
	requirePython(t)
	f := newNativeFixture(t)
	src, err := os.ReadFile("../sessions/claude_fork_path.py")
	if err != nil {
		t.Fatal(err)
	}
	script := string(src)
	// A copy of A in a second project directory makes it ambiguous.
	dup := filepath.Join(f.root, "claude2")
	for _, args := range [][]string{
		{f.ws, cidA}, {f.wsLink, cidA}, {f.ws, cidY}, {f.ws, cidB}, {f.ws, cidC},
		{f.ws, strings.ToUpper(cidA)}, {f.ws, "{" + cidA + "}"}, {f.ws, "short"}, {f.ws, strings.Repeat("z", 32)},
		{}, {f.ws}, {f.ws, cidA, "extra"},
	} {
		same(t, strings.Join(args, " "), script, "claude-fork-path", args, args)
	}
	if err := os.MkdirAll(filepath.Join(dup, "projects", "copy"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"one", "two"} {
		data, _ := os.ReadFile(filepath.Join(f.claudeDir, cidA+".jsonl"))
		writeFile(t, filepath.Join(dup, "projects", d, cidA+".jsonl"), string(data), time.Time{})
	}
	t.Setenv("CLAUDE_CONFIG_DIR", dup)
	same(t, "ambiguous", script, "claude-fork-path", []string{f.ws, cidA}, []string{f.ws, cidA})
}
