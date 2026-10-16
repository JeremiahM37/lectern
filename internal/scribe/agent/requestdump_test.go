package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Every model call of a turn reaches the dump hook once, numbered by step,
// with its model and messages; headers and secrets do not reach the file.
func TestRequestDumpWritesEachStepAndRedacts(t *testing.T) {
	var buf bytes.Buffer
	f := newFake(t)
	key := "sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdef"
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "read", args: `{"path":"x"}`}}}),
		turn(sseTurn{text: []string{"done " + key}}),
	))
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	opts := baseOptions(f, reg, &eventLog{}, t.TempDir())
	opts.RequestDump = DumpRequests(&buf)
	opts.Headers = map[string]string{"X-Session": "secret-session-header"}
	a := New(opts)
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("dump lines = %d, want one per model call:\n%s", len(lines), buf.String())
	}
	for i, line := range lines {
		var got struct {
			Step    int         `json:"step"`
			Model   string      `json:"model"`
			Request llm.Request `json:"request"`
		}
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		if got.Step != i+1 || got.Model != "fake/model-a" || len(got.Request.Messages) == 0 {
			t.Fatalf("line %d = step %d model %q messages %d", i, got.Step, got.Model, len(got.Request.Messages))
		}
		if got.Request.Headers != nil {
			t.Errorf("line %d kept headers", i)
		}
	}
	if strings.Contains(buf.String(), "secret-session-header") {
		t.Error("a request header reached the dump")
	}
	if strings.Contains(buf.String(), key) {
		t.Error("a secret reached the dump unredacted")
	}
}
