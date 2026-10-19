//go:build unix

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// scriptCodex is a sh script that stands in for the codex binary. It records its
// argv and stdin in its own directory and copies the output schema it was
// given, so tests can check what the client sent. The real codex is never run.
type scriptCodex struct {
	dir string
	bin string
}

func newScriptCodex(t *testing.T, stdout, stderr string, exit int) *scriptCodex {
	t.Helper()
	f := newScript(t, `cat "$d/out.jsonl"; cat "$d/err.txt" >&2; exit `+strconv.Itoa(exit))
	mustWrite(t, filepath.Join(f.dir, "out.jsonl"), stdout)
	mustWrite(t, filepath.Join(f.dir, "err.txt"), stderr)
	return f
}

// newScript writes the script with the recording prelude followed by body.
// body can use $d for the fake's directory.
func newScript(t *testing.T, body string) *scriptCodex {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"d=" + shQuote(dir) + "\n" +
		"printf '%s\\n' \"$@\" > \"$d/argv\"\n" +
		"cat > \"$d/stdin\"\n" +
		"prev=\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$prev\" = --output-schema ]; then cp \"$a\" \"$d/schema.json\"; fi\n" +
		"  prev=$a\n" +
		"done\n" +
		body + "\n"
	bin := filepath.Join(dir, "codex")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &scriptCodex{dir: dir, bin: bin}
}

// newHangingScript starts a child and a grandchild that never finish on their
// own. It writes their pids so the test can check both are gone after a kill.
func newHangingScript(t *testing.T) *scriptCodex {
	t.Helper()
	return newScript(t, `echo $$ > "$d/pid"; sleep 30 & echo $! > "$d/grandchild"; wait`)
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func mustWrite(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *scriptCodex) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *scriptCodex) readPid(t *testing.T, name string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(f.dir, name)); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fake codex never wrote %s", name)
	return 0
}

// waitForExit waits until the process no longer exists. A zombie still answers
// kill(0), so a reaped-by-init grandchild is polled for, not checked once.
func waitForExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d is still alive after cancel", pid)
}

func drain(t *testing.T, ch <-chan llm.Event) []llm.Event {
	t.Helper()
	var evs []llm.Event
	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
		case <-deadline:
			t.Fatal("stream did not close")
		}
	}
}

func streamOf(t *testing.T, p *Provider, req llm.Request) []llm.Event {
	t.Helper()
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return drain(t, ch)
}

// agentLine is one item.completed agent_message. The text is JSON-encoded here
// so tests can write the schema object without escaping it by hand.
func agentLine(t *testing.T, text string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type": "item.completed",
		"item": map[string]any{"id": "item_0", "type": "agent_message", "text": text},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func reply(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const turnDone = `{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7,"reasoning_output_tokens":3}}` + "\n"

func simpleRequest() llm.Request {
	return llm.Request{Model: "gpt-5.6-sol", System: "Be brief.", Messages: []llm.Message{llm.UserText("hi")}}
}

func TestStreamToolCallTurn(t *testing.T) {
	text := reply(t, map[string]any{
		"finishReason": "tool-calls",
		"text":         "",
		"toolCalls":    []map[string]string{{"toolName": "read_file", "input": `{"path":"go.mod"}`}},
	})
	f := newScriptCodex(t, `{"type":"thread.started","thread_id":"01a1"}`+"\n"+agentLine(t, text)+turnDone, "", 0)
	evs := streamOf(t, New(Options{Binary: f.bin, Cwd: func() string { return f.dir }}), simpleRequest())

	if len(evs) != 3 {
		t.Fatalf("want start, end, finish; got %d events: %+v", len(evs), evs)
	}
	start, end, finish := evs[0], evs[1], evs[2]
	if start.Type != llm.EvToolCallStart || start.ToolName != "read_file" || start.ToolCallID == "" {
		t.Fatalf("start: %+v", start)
	}
	if end.Type != llm.EvToolCallEnd || end.ToolCallID != start.ToolCallID || string(end.Args) != `{"path":"go.mod"}` {
		t.Fatalf("end: %+v args=%s", end, end.Args)
	}
	if finish.Type != llm.EvFinish || finish.FinishReason != llm.FinishToolCalls {
		t.Fatalf("finish: %+v", finish)
	}
	want := llm.Usage{InputTokens: 60, CacheReadTokens: 40, OutputTokens: 7, ReasoningTokens: 3}
	if finish.Usage != want {
		t.Fatalf("usage %+v want %+v", finish.Usage, want)
	}
}

func TestStreamToolCallIDsAreUniquePerCall(t *testing.T) {
	text := reply(t, map[string]any{
		"finishReason": "tool-calls",
		"text":         "",
		"toolCalls": []map[string]string{
			{"toolName": "a", "input": "{}"},
			{"toolName": "b", "input": "{}"},
		},
	})
	f := newScriptCodex(t, agentLine(t, text)+turnDone, "", 0)
	evs := streamOf(t, New(Options{Binary: f.bin}), simpleRequest())
	ids := map[string]bool{}
	for _, ev := range evs {
		if ev.Type == llm.EvToolCallStart {
			if ids[ev.ToolCallID] {
				t.Fatalf("duplicate id %s", ev.ToolCallID)
			}
			ids[ev.ToolCallID] = true
		}
	}
	if len(ids) != 2 {
		t.Fatalf("want 2 distinct call ids, got %v", ids)
	}
}

func TestStreamInvalidInputPassesThroughAsText(t *testing.T) {
	text := reply(t, map[string]any{
		"finishReason": "tool-calls",
		"text":         "",
		"toolCalls":    []map[string]string{{"toolName": "read_file", "input": "not json{"}},
	})
	f := newScriptCodex(t, agentLine(t, text)+turnDone, "", 0)
	evs := streamOf(t, New(Options{Binary: f.bin}), simpleRequest())
	end := evs[len(evs)-2]
	raw, ok := llm.UnparsedArgs(end.Args)
	if end.Type != llm.EvToolCallEnd || !ok || raw != "not json{" {
		t.Fatalf("bad input should reach the agent as text: %+v", end)
	}
}

func TestStreamTextTurn(t *testing.T) {
	f := newScriptCodex(t, agentLine(t, "Hello from codex")+turnDone, "", 0)
	evs := streamOf(t, New(Options{Binary: f.bin}), simpleRequest())
	if len(evs) != 2 || evs[0].Type != llm.EvText || evs[0].Text != "Hello from codex" {
		t.Fatalf("events: %+v", evs)
	}
	if evs[1].Type != llm.EvFinish || evs[1].FinishReason != llm.FinishStop {
		t.Fatalf("finish: %+v", evs[1])
	}
}

func TestStreamSchemaTextTurn(t *testing.T) {
	text := reply(t, map[string]any{"finishReason": "stop", "text": "Done.", "toolCalls": []any{}})
	f := newScriptCodex(t, agentLine(t, text)+turnDone, "", 0)
	evs := streamOf(t, New(Options{Binary: f.bin}), simpleRequest())
	if len(evs) != 2 || evs[0].Text != "Done." || evs[1].FinishReason != llm.FinishStop {
		t.Fatalf("events: %+v", evs)
	}
}

func TestStreamReasoningComesFirst(t *testing.T) {
	reasoning := `{"type":"item.completed","item":{"type":"reasoning","text":"Checking the file."}}` + "\n"
	f := newScriptCodex(t, reasoning+agentLine(t, "answer")+turnDone, "", 0)
	evs := streamOf(t, New(Options{Binary: f.bin}), simpleRequest())
	if len(evs) != 3 || evs[0].Type != llm.EvReasoning || evs[0].Text != "Checking the file." || evs[1].Type != llm.EvText {
		t.Fatalf("events: %+v", evs)
	}
}

func TestStreamErrorEventBeforeSuccessIsIgnored(t *testing.T) {
	f := newScriptCodex(t, `{"type":"error","message":"Reconnecting 1/5"}`+"\n"+agentLine(t, "ok")+turnDone, "", 0)
	evs := streamOf(t, New(Options{Binary: f.bin}), simpleRequest())
	for _, ev := range evs {
		if ev.Type == llm.EvError {
			t.Fatalf("a retried error must not fail a turn that completed: %v", ev.Err)
		}
	}
}

func TestStreamUsageLimitIsNotRetryable(t *testing.T) {
	f := newScriptCodex(t, `{"type":"turn.failed","error":{"message":"You've hit your usage limit."}}`+"\n", "", 1)
	evs := streamOf(t, New(Options{Binary: f.bin}), simpleRequest())
	last := evs[len(evs)-1]
	if last.Type != llm.EvError {
		t.Fatalf("want a terminal error, got %+v", last)
	}
	if msg := errorMessage(last.Err); msg != "Codex usage limit reached: You've hit your usage limit." {
		t.Fatalf("message: %q", msg)
	}
	if _, retry := llm.Classify(last.Err); retry {
		t.Fatal("usage limit retried")
	}
}

func TestStreamNotSignedInFromStderr(t *testing.T) {
	f := newScriptCodex(t, "", "Error: not logged in\n", 1)
	evs := streamOf(t, New(Options{Binary: f.bin}), simpleRequest())
	last := evs[len(evs)-1]
	if last.Type != llm.EvError || errorMessage(last.Err) != "Codex is not signed in. Run /codex login." {
		t.Fatalf("terminal: %+v", last)
	}
	if cat, _ := llm.Classify(last.Err); cat != llm.CatAuth {
		t.Fatalf("category %q", cat)
	}
}

func TestStreamNonZeroExitShowsStderrTail(t *testing.T) {
	f := newScriptCodex(t, "", "boom happened\n", 3)
	evs := streamOf(t, New(Options{Binary: f.bin}), simpleRequest())
	last := evs[len(evs)-1]
	if last.Type != llm.EvError || !strings.Contains(last.Err.Error(), "boom happened") {
		t.Fatalf("terminal: %+v", last)
	}
	var ae *llm.APIError
	if errors.As(last.Err, &ae) {
		t.Fatalf("an unknown failure should not be an APIError: %+v", ae)
	}
}

func TestStreamErrorEventOnlyIsReported(t *testing.T) {
	f := newScriptCodex(t, `{"type":"error","message":"model refused"}`+"\n", "", 0)
	evs := streamOf(t, New(Options{Binary: f.bin}), simpleRequest())
	last := evs[len(evs)-1]
	if last.Type != llm.EvError || last.Err.Error() != "model refused" {
		t.Fatalf("terminal: %+v", last)
	}
}

func TestStreamTransientFailureIsRetryable(t *testing.T) {
	f := newScriptCodex(t, `{"type":"turn.failed","error":{"message":"stream disconnected before completion"}}`+"\n", "", 1)
	evs := streamOf(t, New(Options{Binary: f.bin}), simpleRequest())
	last := evs[len(evs)-1]
	if _, retry := llm.Classify(last.Err); !retry {
		t.Fatalf("a stream drop must be retryable: %v", last.Err)
	}
}

func TestStreamMissingBinaryGivesInstallHint(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-codex")
	evs := streamOf(t, New(Options{Binary: missing}), simpleRequest())
	last := evs[len(evs)-1]
	if last.Type != llm.EvError || !strings.Contains(last.Err.Error(), "npm i -g @openai/codex") {
		t.Fatalf("terminal: %+v", last)
	}
}

func TestStreamInvocationAndStdin(t *testing.T) {
	f := newScriptCodex(t, agentLine(t, "ok")+turnDone, "", 0)
	req := simpleRequest()
	p := New(Options{Binary: f.bin, Cwd: func() string { return "/srv/work" }})
	streamOf(t, p, req)

	argv := strings.Split(strings.TrimRight(f.read(t, "argv"), "\n"), "\n")
	// The schema path is a temp file that is removed after the call, so it is
	// read from argv and checked by its content below.
	schemaPath := argv[indexOf(argv, "--output-schema")+1]
	want := []string{
		"exec", "--json", "--ephemeral", "--skip-git-repo-check",
		"--sandbox", "read-only",
		"-c", `approval_policy="never"`,
	}
	want = append(want, isolationArgs...)
	want = append(want,
		"--cd", "/srv/work",
		"--output-schema", schemaPath,
		"--model", "gpt-5.6-sol",
		"-",
	)

	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv\n got %q\nwant %q", argv, want)
	}
	if got := f.read(t, "schema.json"); got != outputSchema {
		t.Fatalf("schema file given to codex differs from outputSchema")
	}
	if _, err := os.Stat(schemaPath); !os.IsNotExist(err) {
		t.Fatalf("schema temp file outlived the call: %v", err)
	}
	if got := f.read(t, "stdin"); got != buildPrompt(req) {
		t.Fatalf("stdin is not the prompt")
	}
}

func TestStreamCancelKillsCodexAndItsChildren(t *testing.T) {
	f := newHangingScript(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := New(Options{Binary: f.bin}).Stream(ctx, simpleRequest())
	if err != nil {
		t.Fatal(err)
	}
	pid := f.readPid(t, "pid")
	grandchild := f.readPid(t, "grandchild")
	cancel()
	evs := drain(t, ch)

	last := evs[len(evs)-1]
	if last.Type != llm.EvError || !errors.Is(last.Err, context.Canceled) {
		t.Fatalf("terminal after cancel: %+v", last)
	}
	waitForExit(t, pid)
	waitForExit(t, grandchild)
}

func TestStreamTimeoutKillsCodex(t *testing.T) {
	f := newHangingScript(t)
	ch, err := New(Options{Binary: f.bin, Timeout: 300 * time.Millisecond}).Stream(context.Background(), simpleRequest())
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(t, ch)
	last := evs[len(evs)-1]
	if last.Type != llm.EvError || !strings.Contains(last.Err.Error(), "did not answer within 300ms") {
		t.Fatalf("terminal after timeout: %+v", last)
	}
	waitForExit(t, f.readPid(t, "pid"))
}

func TestModelsFallbackAndListOverride(t *testing.T) {
	models, err := New(Options{}).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 || models[0].ID != "gpt-5.6-sol" || !models[0].Tools || !models[0].CatalogPriced {
		t.Fatalf("fallback models: %+v", models)
	}
	listed := []llm.ModelInfo{{ID: "only-one"}}
	got, err := New(Options{ListModels: func(context.Context) ([]llm.ModelInfo, error) { return listed, nil }}).Models(context.Background())
	if err != nil || len(got) != 1 || got[0].ID != "only-one" {
		t.Fatalf("list override: %+v err=%v", got, err)
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
