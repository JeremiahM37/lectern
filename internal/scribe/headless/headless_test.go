package headless

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// fakeServer speaks the OpenAI streaming wire format. reply is called for
// each request with its 1-based number and the raw request body.
type fakeServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newFakeServer(t *testing.T, reply func(n int, w http.ResponseWriter, r *http.Request, body string)) *fakeServer {
	t.Helper()
	f := &fakeServer{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, buf.String())
		n := len(f.bodies)
		f.mu.Unlock()
		reply(n, w, r, buf.String())
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

// sse writes the data chunks as a streamed completion.
func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func textReply(text string) []string {
	b, _ := json.Marshal(text)
	return []string{
		`{"choices":[{"delta":{"content":` + string(b) + `}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
	}
}

// toolReply asks for one tool call; list_dir is read-only, so it needs no approval.
func toolReply(name, args string) []string {
	a, _ := json.Marshal(args)
	return []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"` + name + `","arguments":` + string(a) + `}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
	}
}

// setup points SCRIBE_HOME at a config naming the fake provider and makes a
// git repository to work in. It returns the repository path.
func setup(t *testing.T, base string) string {
	t.Helper()
	home := t.TempDir()
	repo := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	t.Setenv("SCRIBE_NO_REPOMAP", "")
	cfg := fmt.Sprintf(`{"providers":[{"id":"fake","base_url":%q}],"model":"fake/m"}`, base+"/v1")
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("git"); err == nil {
		if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
	} else {
		t.Skip("git is not installed")
	}
	return repo
}

type result struct {
	code           int
	stdout, stderr string
}

func invoke(t *testing.T, args []string, stdin string) result {
	t.Helper()
	var out, errb bytes.Buffer
	sigs := make(chan os.Signal, 2)
	code := run(t.Context(), args, strings.NewReader(stdin), &out, &errb, sigs)
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

func parseLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not JSON: %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func types(evs []map[string]any) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e["type"].(string))
	}
	return out
}

func TestTextMode(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {
		sse(w, textReply("Hello from the fake model.")...)
	})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--cwd", repo, "say hello"}, "")
	if res.code != 0 {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}
	if res.stdout != "Hello from the fake model.\n" {
		t.Errorf("stdout = %q", res.stdout)
	}
	if !strings.Contains(res.stderr, "model: fake/m") || !strings.Contains(res.stderr, "1 steps") {
		t.Errorf("stderr missing progress: %q", res.stderr)
	}
	if !strings.Contains(srv.bodies[0], "say hello") {
		t.Errorf("prompt not sent: %s", srv.bodies[0])
	}
}

func TestQuietSuppressesProgress(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {
		sse(w, textReply("quiet")...)
	})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--cwd", repo, "-q", "hi"}, "")
	if res.code != 0 || res.stdout != "quiet\n" || res.stderr != "" {
		t.Errorf("code %d stdout %q stderr %q", res.code, res.stdout, res.stderr)
	}
}

func TestJSONRun(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {
		if n == 1 {
			sse(w, toolReply("list_dir", `{"path":"."}`)...)
			return
		}
		sse(w, textReply("done listing")...)
	})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--json", "--cwd", repo, "list it"}, "")
	if res.code != 0 {
		t.Fatalf("exit %d stderr %q", res.code, res.stderr)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &obj); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, res.stdout)
	}
	if obj["model"] != "fake/m" || obj["mode"] != "default" || obj["prompt"] != "list it" || obj["output"] != "done listing" {
		t.Errorf("fields: %v", obj)
	}
	if obj["steps"] != float64(2) {
		t.Errorf("steps = %v", obj["steps"])
	}
	calls, _ := obj["toolCalls"].([]any)
	if len(calls) != 1 || calls[0] != "list_dir" {
		t.Errorf("toolCalls = %v", obj["toolCalls"])
	}
	tokens, _ := obj["tokens"].(map[string]any)
	if tokens["input"] != float64(20) || tokens["output"] != float64(4) {
		t.Errorf("tokens = %v", tokens)
	}
	for _, k := range []string{"costUSD", "filesEdited", "duration"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("missing %s in %v", k, obj)
		}
	}
	if _, ok := obj["sessionId"]; ok {
		t.Errorf("ephemeral run reported a session id")
	}
	if _, ok := obj["error"]; ok {
		t.Errorf("error present on success: %v", obj["error"])
	}
}

func TestEventsRun(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {
		if n == 1 {
			sse(w, toolReply("list_dir", `{"path":"."}`)...)
			return
		}
		sse(w, textReply("ok")...)
	})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--events", "--cwd", repo, "go"}, "")
	if res.code != 0 {
		t.Fatalf("exit %d stderr %q", res.code, res.stderr)
	}
	if res.stderr != "" {
		t.Errorf("events mode wrote progress to stderr: %q", res.stderr)
	}
	evs := parseLines(t, res.stdout)
	got := types(evs)
	// One step per model call. The first step ends in a tool call, the
	// second streams the answer. done comes last.
	want := []string{"start", "step", "tool-call", "tool-result", "text", "step", "done"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	if evs[0]["model"] != "fake/m" || evs[0]["session"] != nil {
		t.Errorf("start = %v", evs[0])
	}
	// The code map is on unless --no-repomap says otherwise; workers is
	// per dispatch call, so it stays null at start.
	if evs[0]["repoMap"] != true || evs[0]["workers"] != nil {
		t.Errorf("start repoMap/workers = %v / %v", evs[0]["repoMap"], evs[0]["workers"])
	}
	if evs[2]["tool"] != "list_dir" || evs[2]["input"] == nil {
		t.Errorf("tool-call = %v", evs[2])
	}
	last := evs[len(evs)-1]
	if last["output"] != "ok" || last["error"] != nil {
		t.Errorf("done = %v", last)
	}
}

func TestEventsStartReportsNoRepoMap(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {
		sse(w, textReply("ok")...)
	})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--events", "--no-repomap", "--cwd", repo, "go"}, "")
	if res.code != 0 {
		t.Fatalf("exit %d stderr %q", res.code, res.stderr)
	}
	evs := parseLines(t, res.stdout)
	if evs[0]["repoMap"] != false {
		t.Fatalf("start = %v, want repoMap false", evs[0])
	}
}

func TestMaxStepsExitsOne(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {
		sse(w, toolReply("list_dir", `{"path":"."}`)...)
	})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--events", "--max-steps", "2", "--cwd", repo, "loop"}, "")
	if res.code != 1 {
		t.Fatalf("exit %d, want 1; stderr %q", res.code, res.stderr)
	}
	evs := parseLines(t, res.stdout)
	last := evs[len(evs)-1]
	if last["type"] != "done" || !strings.Contains(fmt.Sprint(last["error"]), "max steps") {
		t.Errorf("done = %v", last)
	}
	var sawError bool
	for _, e := range evs {
		if e["type"] == "error" && strings.Contains(fmt.Sprint(e["error"]), "max steps") {
			sawError = true
		}
	}
	if !sawError {
		t.Errorf("no error event for max steps: %v", types(evs))
	}
}

func TestTimeoutExitsTwo(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {
		// Hang until the client gives up.
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--timeout", "300", "--cwd", repo, "hang"}, "")
	if res.code != 2 {
		t.Fatalf("exit %d, want 2; stderr %q", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "timed out after 300ms") {
		t.Errorf("stderr = %q", res.stderr)
	}
}

func TestInterruptAbortsTurnWithExit130(t *testing.T) {
	seen := make(chan struct{}, 1)
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {
		select {
		case seen <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	repo := setup(t, srv.srv.URL)
	sigs := make(chan os.Signal, 2)
	go func() {
		<-seen
		sigs <- os.Interrupt
	}()
	var out, errb bytes.Buffer
	code := run(t.Context(), []string{"--cwd", repo, "hang"}, strings.NewReader(""), &out, &errb, sigs)
	if code != 130 {
		t.Fatalf("exit %d, want 130; stderr %q", code, errb.String())
	}
}

func TestListProviders(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--list-providers", "--cwd", repo}, "")
	if res.code != 0 {
		t.Fatalf("exit %d stderr %q", res.code, res.stderr)
	}
	for _, want := range []string{"PROVIDER", "anthropic", "fake", "✗ no key", "claude-sonnet-5-5"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("table missing %q:\n%s", want, res.stdout)
		}
	}
}

func TestSetKeyWritesKey(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--set-key", "anthropic", "sk-test-123", "--cwd", repo}, "")
	if res.code != 0 {
		t.Fatalf("exit %d stderr %q", res.code, res.stderr)
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("SCRIBE_HOME"), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"anthropic": "sk-test-123"`) && !strings.Contains(string(raw), `"anthropic":"sk-test-123"`) {
		t.Errorf("keys.json = %s", raw)
	}
	list := invoke(t, []string{"--list-providers", "--cwd", repo}, "")
	if !strings.Contains(list.stdout, "✓ store") {
		t.Errorf("stored key not reported:\n%s", list.stdout)
	}
	if bad := invoke(t, []string{"--set-key", "anthropic"}, ""); bad.code != 1 || !strings.Contains(bad.stderr, "PROVIDER KEY") {
		t.Errorf("missing args: exit %d stderr %q", bad.code, bad.stderr)
	}
}

func TestIncludeAndMissingInclude(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {
		sse(w, textReply("ok")...)
	})
	repo := setup(t, srv.srv.URL)
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("alpha beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := invoke(t, []string{"--include", "notes.txt", "--include", "missing.txt", "--cwd", repo, "summarise"}, "")
	if res.code != 0 {
		t.Fatalf("exit %d stderr %q", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "skipping --include missing.txt") {
		t.Errorf("no warning for the missing file: %q", res.stderr)
	}
	if !strings.Contains(srv.bodies[0], `[notes.txt]\nalpha beta`) || !strings.Contains(srv.bodies[0], "summarise") {
		t.Errorf("include not prepended: %s", srv.bodies[0])
	}
}

func TestStdinPrompt(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {
		sse(w, textReply("from stdin")...)
	})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--cwd", repo}, "  piped prompt\n")
	if res.code != 0 || res.stdout != "from stdin\n" {
		t.Fatalf("exit %d stdout %q stderr %q", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(srv.bodies[0], "piped prompt") {
		t.Errorf("stdin prompt not sent: %s", srv.bodies[0])
	}
	if empty := invoke(t, []string{"--cwd", repo}, "   \n"); empty.code != 1 {
		t.Errorf("empty stdin: exit %d, want 1", empty.code)
	}
}

func TestChatTwoTurnsJSON(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {
		if n == 1 {
			sse(w, textReply("reply one")...)
			return
		}
		sse(w, textReply("reply two")...)
	})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--chat", "--json", "--cwd", repo}, "first question\nsecond \\\nquestion\n")
	if res.code != 0 {
		t.Fatalf("exit %d stderr %q", res.code, res.stderr)
	}
	turns := parseLines(t, res.stdout)
	if len(turns) != 2 {
		t.Fatalf("got %d turn objects, want 2:\n%s", len(turns), res.stdout)
	}
	if turns[0]["turn"] != float64(1) || turns[0]["output"] != "reply one" || turns[1]["output"] != "reply two" {
		t.Errorf("turns = %v", turns)
	}
	if !strings.Contains(srv.bodies[1], "second \\\\nquestion") && !strings.Contains(srv.bodies[1], `second \nquestion`) {
		t.Errorf("continuation line not joined: %s", srv.bodies[1])
	}
}

func TestChatEventsOrder(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {
		sse(w, textReply(fmt.Sprintf("turn %d", n))...)
	})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--chat", "--events", "--cwd", repo}, "a\nb\n")
	if res.code != 0 {
		t.Fatalf("exit %d stderr %q", res.code, res.stderr)
	}
	got := strings.Join(types(parseLines(t, res.stdout)), ",")
	want := "start,ready,text,step,turn-done,ready,text,step,turn-done,ready,chat-done"
	if got != want {
		t.Errorf("event types = %s\nwant %s", got, want)
	}
	if !strings.Contains(res.stdout, `"sessionId"`) {
		t.Errorf("chat is saved, so chat-done should carry a session id:\n%s", res.stdout)
	}
}

func TestUnknownModeAndFlag(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {})
	repo := setup(t, srv.srv.URL)
	if res := invoke(t, []string{"--mode", "wizard", "--cwd", repo, "x"}, ""); res.code != 1 || !strings.Contains(res.stderr, "unknown mode") {
		t.Errorf("bad mode: exit %d stderr %q", res.code, res.stderr)
	}
	if res := invoke(t, []string{"--bogus", "x"}, ""); res.code != 1 {
		t.Errorf("bad flag: exit %d", res.code)
	}
	if res := invoke(t, []string{"--session", "deadbeef", "--cwd", repo, "x"}, ""); res.code != 1 {
		t.Errorf("unknown session: exit %d", res.code)
	}
}

func TestParseTimeout(t *testing.T) {
	for in, want := range map[string]time.Duration{"1500": 1500 * time.Millisecond, "90s": 90 * time.Second, "2m": 2 * time.Minute} {
		got, err := parseTimeout(in)
		if err != nil || got != want {
			t.Errorf("parseTimeout(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseTimeout("soon"); err == nil {
		t.Errorf("parseTimeout accepted nonsense")
	}
}

func TestPresetFlagIsAccepted(t *testing.T) {
	o, err := parseArgs([]string{"--preset", "fast", "-p", "hi"})
	if err != nil || o.preset != "fast" {
		t.Fatalf("--preset NAME: preset=%q err=%v", o.preset, err)
	}
	o, err = parseArgs([]string{"--preset=slow", "hi"})
	if err != nil || o.preset != "slow" {
		t.Fatalf("--preset=NAME: preset=%q err=%v", o.preset, err)
	}
}

func TestUnknownPresetExitsOne(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--preset", "nope", "--cwd", repo, "x"}, "")
	if res.code != 1 || !strings.Contains(res.stderr, "no preset") {
		t.Fatalf("unknown preset: exit %d stderr %q", res.code, res.stderr)
	}
}

// A key that the model streams in pieces must not reach stdout or the events
// stream, whole or in part. Text is held back and redacted as a block.
func TestStreamedSecretIsMasked(t *testing.T) {
	key := "sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdef"
	chunks := []string{strings.Repeat("filler ", 120), "token is " + key[:20], key[20:] + " end.\n"}
	for _, format := range []options{{}, {events: true}} {
		var stdout, stderr bytes.Buffer
		o := newOutput(&stdout, &stderr, format)
		o.beginTurn()
		for _, c := range chunks {
			o.handle(agent.Event{Kind: agent.EvText, Text: c})
		}
		o.flushStream()
		if strings.Contains(stdout.String(), key[:20]) || strings.Contains(stdout.String(), key[20:]) {
			t.Fatalf("streamed key reached output (events=%v): %q", format.events, stdout.String())
		}
		if !strings.Contains(stdout.String(), "[redacted]") {
			t.Fatalf("nothing masked (events=%v): %q", format.events, stdout.String())
		}
	}
}

// A turn whose model reply has no text must not exit 0: a script would read
// nothing and treat it as a success.
func TestEmptyAnswerExitsOneWithMessage(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {
		sse(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
	})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--cwd", repo, "say nothing"}, "")
	if res.code != 1 {
		t.Fatalf("exit %d, want 1; stdout %q stderr %q", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "empty answer") {
		t.Errorf("stderr does not say why: %q", res.stderr)
	}
}

// The same turn in --json reports the error in its object, and the object is
// the only thing on stdout.
func TestEmptyAnswerJSONCarriesError(t *testing.T) {
	srv := newFakeServer(t, func(n int, w http.ResponseWriter, r *http.Request, body string) {
		sse(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
	})
	repo := setup(t, srv.srv.URL)
	res := invoke(t, []string{"--json", "--cwd", repo, "say nothing"}, "")
	if res.code != 1 {
		t.Fatalf("exit %d, want 1", res.code)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &obj); err != nil {
		t.Fatalf("stdout is not one JSON object: %q (%v)", res.stdout, err)
	}
	if !strings.Contains(fmt.Sprint(obj["error"]), "empty answer") {
		t.Errorf("error = %v", obj["error"])
	}
}
