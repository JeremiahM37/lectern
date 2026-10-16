package anthropic

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// A tool call whose streamed JSON is cut off must still end with a
// tool_call_end, carrying the raw text, so the agent can report it.
func TestTruncatedToolJSONKeepsRawText(t *testing.T) {
	body := frame("message_start", messageStart) +
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_09","name":"write_file","input":{}}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\": \"a.go\", \"content\": \"pack"}}`) +
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":9}}`) +
		frame("message_stop", `{"type":"message_stop"}`)

	evs := mustStream(t, newTestServer(t, sseHandler(body)), llm.Request{Model: "m"})
	var end *llm.Event
	for i := range evs {
		if evs[i].Type == llm.EvToolCallEnd {
			end = &evs[i]
		}
	}
	if end == nil {
		t.Fatal("no tool_call_end for a truncated call")
	}
	if end.ToolName != "write_file" || end.ToolCallID != "toolu_09" {
		t.Fatalf("end = %+v", end)
	}
	raw, bad := llm.UnparsedArgs(end.Args)
	if !bad {
		t.Fatalf("args %s parsed as an object; want the raw text kept", end.Args)
	}
	if raw != `{"path": "a.go", "content": "pack` {
		t.Fatalf("raw args = %q", raw)
	}
	if terminalOf(t, evs).FinishReason != llm.FinishLength {
		t.Fatalf("finish = %v, want length", terminalOf(t, evs).FinishReason)
	}
}

// A well-formed call is not flagged.
func TestWellFormedToolJSONNotFlagged(t *testing.T) {
	body := frame("message_start", messageStart) +
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_10","name":"read_file","input":{}}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"a.go\"}"}}`) +
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":3}}`) +
		frame("message_stop", `{"type":"message_stop"}`)

	evs := mustStream(t, newTestServer(t, sseHandler(body)), llm.Request{Model: "m"})
	for _, ev := range evs {
		if ev.Type == llm.EvToolCallEnd {
			if _, bad := llm.UnparsedArgs(ev.Args); bad || string(ev.Args) != `{"path":"a.go"}` {
				t.Fatalf("args = %s", ev.Args)
			}
		}
	}
}

// A refusal stop is a content-filter finish, not a normal answer.
func TestRefusalStopIsFilterFinish(t *testing.T) {
	body := frame("message_start", messageStart) +
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null},"usage":{"output_tokens":1}}`) +
		frame("message_stop", `{"type":"message_stop"}`)
	evs := mustStream(t, newTestServer(t, sseHandler(body)), llm.Request{Model: "m"})
	if got := terminalOf(t, evs).FinishReason; got != llm.FinishFilter {
		t.Fatalf("finish = %v, want content_filter", got)
	}
}
