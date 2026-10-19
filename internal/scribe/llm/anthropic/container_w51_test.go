package anthropic

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// The response names the code-execution container in message_start. The
// adapter reports it once, so the agent can send it back on the next request.
func TestMessageStartReportsTheContainer(t *testing.T) {
	st := newStreamState("anthropic")
	evs, _, err := st.apply([]byte(`{"type":"message_start","message":{"id":"m1","container":{"id":"container_abc","expires_at":"2026-11-01T00:00:00Z"},"usage":{"input_tokens":3}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, ev := range evs {
		if ev.Type == llm.EvContainer {
			got = ev.Text
		}
	}
	if got != "container_abc" {
		t.Fatalf("container event = %q, want container_abc (events %+v)", got, evs)
	}
}

// The container goes back as a top-level string only with code execution on.
func TestContainerIsSentOnlyWithCodeExecution(t *testing.T) {
	isolate(t)
	on := bodyOf(t, llm.Request{Model: "claude-sonnet-5-5", CodeExecution: true, Container: "container_abc",
		Messages: []llm.Message{{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartText, Text: "go"}}}}})
	if on["container"] != "container_abc" {
		t.Fatalf("container = %#v, want the id", on["container"])
	}
	off := bodyOf(t, llm.Request{Model: "claude-sonnet-5-5", Container: "container_abc",
		Messages: []llm.Message{{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartText, Text: "go"}}}}})
	if _, ok := off["container"]; ok {
		t.Fatal("a container id must not be sent when code execution is off")
	}
}
