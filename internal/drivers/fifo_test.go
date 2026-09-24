package drivers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUserMessageLineShape(t *testing.T) {
	line := userMessageLine("do the thing")
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, line)
	}
	if msg.Type != "user" || msg.Message.Role != "user" {
		t.Fatalf("wrong envelope: %+v", msg)
	}
	if len(msg.Message.Content) != 1 || msg.Message.Content[0].Type != "text" ||
		msg.Message.Content[0].Text != "do the thing" {
		t.Fatalf("wrong content: %+v", msg.Message.Content)
	}
}

func TestEnsureFifoCommandShape(t *testing.T) {
	cmd := ensureFifoCommand("/wt/.lectern")
	for _, want := range []string{"mkdir -p", "mkfifo", "steer.fifo", "[ -p"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command missing %q\n%s", want, cmd)
		}
	}
}

// streamLaunchCommand deliberately does NOT create the fifo (see
// ensureFifoCommand's doc comment for the race that would reopen) — assert
// that split stays that way.
func TestStreamLaunchCommandShape(t *testing.T) {
	cmd := streamLaunchCommand("lec-1", "/wt/.lectern", "/wt", "FOO=bar claude --input-format stream-json")
	for _, want := range []string{
		"tmux new-session -d -s lec-1",
		"cd /wt &&",
		"steer.fifo",
		"pump.py",
		// the agent invocation must be GROUPED ({ ...; }) on the pipe's right
		// side, or "cd WT && agent" (which is what a naive, ungrouped
		// "pipe | cd WT && agent" actually parses as — see the function's own
		// doc comment) leaves the agent reading the pty instead of the fifo.
		"{ FOO=bar claude --input-format stream-json; }",
		"> /wt/.lectern/events.jsonl",
		"2> /wt/.lectern/stderr.log",
		"echo $? > /wt/.lectern/exit_code",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command missing %q\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "mkfifo") {
		t.Error("streamLaunchCommand must not create the fifo itself; that races the launch")
	}
}

func TestAppendAndCloseCommands(t *testing.T) {
	line := appendCommand("/wt/.lectern", `{"type":"user"}`)
	if !strings.Contains(line, ">> /wt/.lectern/steer.fifo") {
		t.Errorf("append does not target the fifo: %s", line)
	}
	if !strings.Contains(line, `{"type":"user"}`) {
		t.Errorf("append lost the payload: %s", line)
	}
	closeLine := closeCommand("/wt/.lectern")
	if !strings.Contains(closeLine, endSentinel) {
		t.Errorf("close does not send the end sentinel: %s", closeLine)
	}
}

func TestPumpScriptExitsOnEndSentinel(t *testing.T) {
	if !strings.Contains(pumpScript, "END") || !strings.Contains(pumpScript, "sys.exit(0)") {
		t.Fatalf("pump script no longer has a graceful exit path:\n%s", pumpScript)
	}
}
