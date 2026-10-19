package codex

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestExecArgsMatchContract(t *testing.T) {
	got := execArgs("gpt-5.6-sol", "/work", "/tmp/s.json")
	want := []string{
		"exec", "--json", "--ephemeral", "--skip-git-repo-check",
		"--sandbox", "read-only",
		"-c", `approval_policy="never"`,
	}
	want = append(want, isolationArgs...)
	want = append(want,
		"--cd", "/work",
		"--output-schema", "/tmp/s.json",
		"--model", "gpt-5.6-sol",
		"-",
	)

	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
}

func TestExecArgsOmitsEmptyModel(t *testing.T) {
	for _, a := range execArgs("", "/w", "/s") {
		if a == "--model" {
			t.Fatal("--model sent with no model")
		}
	}
}

func TestParseExecOutputContractSample(t *testing.T) {
	out := []byte(`{"type":"thread.started","thread_id":"01a1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"{\"finishReason\":\"tool-calls\",\"text\":\"\",\"toolCalls\":[{\"toolName\":\"read_file\",\"input\":\"{\\\"path\\\":\\\"go.mod\\\"}\"}]}"}}
{"type":"turn.completed","usage":{"input_tokens":86975,"cached_input_tokens":43264,"cache_write_input_tokens":0,"output_tokens":308,"reasoning_output_tokens":224}}
`)
	res := parseExecOutput(out)
	if !res.hasFinal || res.turnFailed != "" {
		t.Fatalf("final message not read: %+v", res)
	}
	reply, ok := parseReply(res.finalText)
	if !ok || len(reply.ToolCalls) != 1 || reply.ToolCalls[0].ToolName != "read_file" {
		t.Fatalf("reply not decoded: %+v ok=%v", reply, ok)
	}
	if reply.ToolCalls[0].Input != `{"path":"go.mod"}` {
		t.Fatalf("input string not kept: %q", reply.ToolCalls[0].Input)
	}
	u := res.usage.toLLM()
	if u.InputTokens != 86975-43264 || u.CacheReadTokens != 43264 || u.OutputTokens != 308 || u.ReasoningTokens != 224 {
		t.Fatalf("usage: %+v", u)
	}
}

func TestParseExecOutputSkipsNoiseAndUnknownItems(t *testing.T) {
	out := []byte("warning: something\n" +
		"not json\n" +
		`{"type":"item.completed","item":{"type":"command_execution","text":"ls"}}` + "\n" +
		`{"type":"item.completed","item":{"type":"reasoning","text":"thinking it over"}}` + "\n" +
		`{"type":"item.completed","item":{"type":"agent_message","text":"first"}}` + "\n" +
		`{"type":"item.completed","item":{"type":"agent_message","text":"last"}}` + "\n")
	res := parseExecOutput(out)
	if res.finalText != "last" {
		t.Fatalf("only the last agent message is the answer, got %q", res.finalText)
	}
	if len(res.reasoning) != 1 || res.reasoning[0] != "thinking it over" {
		t.Fatalf("reasoning: %q", res.reasoning)
	}
}

func TestParseExecOutputFailureShapes(t *testing.T) {
	res := parseExecOutput([]byte(`{"type":"turn.failed","error":{"message":"the model is gone"}}` + "\n"))
	if res.turnFailed != "the model is gone" {
		t.Fatalf("turn.failed message: %q", res.turnFailed)
	}
	res = parseExecOutput([]byte(`{"type":"turn.failed"}` + "\n"))
	if res.turnFailed == "" {
		t.Fatal("a turn.failed with no message must still count as a failure")
	}
	res = parseExecOutput([]byte(`{"type":"error","message":"Reconnecting 1/5"}` + "\n"))
	if res.lastError != "Reconnecting 1/5" {
		t.Fatalf("error event: %q", res.lastError)
	}
}

func TestParseReply(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		structured bool
	}{
		{"schema stop", `{"finishReason":"stop","text":"hi","toolCalls":[]}`, true},
		{"schema tools", `{"finishReason":"tool-calls","text":"","toolCalls":[]}`, true},
		{"plain text", "Hello there", false},
		{"json without finish", `{"text":"hi"}`, false},
		{"bad finish", `{"finishReason":"length","text":"x","toolCalls":[]}`, false},
		{"broken json", `{"finishReason":"stop"`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := parseReply(c.text); ok != c.structured {
				t.Fatalf("structured=%v want %v", ok, c.structured)
			}
		})
	}
}

func TestFailureMapping(t *testing.T) {
	t.Run("usage limit is not retryable", func(t *testing.T) {
		err := failure("You've hit your usage limit. Try again later.")
		var ae *llm.APIError
		if !errors.As(err, &ae) {
			t.Fatalf("want *llm.APIError, got %T", err)
		}
		if ae.Message != "Codex usage limit reached: You've hit your usage limit. Try again later." {
			t.Fatalf("message: %q", ae.Message)
		}
		if ae.StatusCode != http.StatusPaymentRequired {
			t.Fatalf("status: %d", ae.StatusCode)
		}
		if _, retry := llm.Classify(err); retry {
			t.Fatal("a usage limit must not be retried")
		}
	})
	t.Run("rate limit text is a usage limit", func(t *testing.T) {
		if msg := errorMessage(failure("HTTP 429 rate limit")); !strings.HasPrefix(msg, "Codex usage limit reached:") {
			t.Fatalf("message: %q", msg)
		}
	})
	t.Run("sign-in problems say how to fix it", func(t *testing.T) {
		for _, text := range []string{"Error: not logged in", "401 Unauthorized", "please run login"} {
			err := failure(text)
			if msg := errorMessage(err); msg != "Codex is not signed in. Run /codex login." {
				t.Fatalf("%q -> %q", text, msg)
			}
			if cat, _ := llm.Classify(err); cat != llm.CatAuth {
				t.Fatalf("%q classified %q, want auth", text, cat)
			}
		}
	})
	t.Run("stream text is retryable", func(t *testing.T) {
		err := failure("stream disconnected before completion")
		var ae *llm.APIError
		if !errors.As(err, &ae) || ae.Type != llm.StreamErrorType {
			t.Fatalf("want a stream error, got %v", err)
		}
		if _, retry := llm.Classify(err); !retry {
			t.Fatal("a stream error must be retried")
		}
	})
	t.Run("other text is the server's own message", func(t *testing.T) {
		err := failure("model refused the request")
		if err.Error() != "model refused the request" {
			t.Fatalf("message: %q", err.Error())
		}
	})
}

func TestTailBufferKeepsEnd(t *testing.T) {
	tb := &execStderrTail{max: 5}
	tb.Write([]byte("abc"))
	tb.Write([]byte("defgh"))
	if got := tb.String(); got != "defgh" {
		t.Fatalf("tail = %q", got)
	}
}

func TestEventsForNoAnswer(t *testing.T) {
	_, err := eventsFor(execResult{}, nil, "")
	if err == nil || !strings.Contains(err.Error(), "finished without an answer") {
		t.Fatalf("err = %v", err)
	}
}

func TestToolArgsPassThroughForBadInput(t *testing.T) {
	evs, err := eventsFor(execResult{hasFinal: true, finalText: `{"finishReason":"tool-calls","text":"","toolCalls":[{"toolName":"x","input":"not json{"}]}`}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var end llm.Event
	for _, ev := range evs {
		if ev.Type == llm.EvToolCallEnd {
			end = ev
		}
	}
	raw, ok := llm.UnparsedArgs(end.Args)
	if !ok || raw != "not json{" {
		t.Fatalf("bad input not kept as raw text: %s", end.Args)
	}
	var s string
	if json.Unmarshal(end.Args, &s) != nil {
		t.Fatalf("args should be a JSON string here: %s", end.Args)
	}
}

func errorMessage(err error) string {
	var ae *llm.APIError
	if errors.As(err, &ae) {
		return ae.Message
	}
	return err.Error()
}
