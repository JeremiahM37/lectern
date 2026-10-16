package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// The raw values below are fake. Each one must never reach a session file.
const (
	rawEnvSecret  = "envsecret-FAKE-7781234"
	rawStoredKey  = "sk-ant-api03-FAKESTOREDKEY9876543"
	rawShellKey   = "ghp_FAKE0123456789abcdefghijklmnopqrstuv"
	rawPromptPass = "password=hunter22FAKE"
)

func TestSessionFilesNeverHoldRawSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	t.Setenv("DEPLOY_API_TOKEN", rawEnvSecret)
	keys := `{"anthropic":"` + rawStoredKey + `"}`
	if err := os.WriteFile(filepath.Join(home, "keys.json"), []byte(keys), 0o600); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	st := NewStore(root)
	m := &Meta{Workdir: root}
	if err := st.Create(m); err != nil {
		t.Fatal(err)
	}
	id := m.ID

	// A prompt whose first line carries a secret: it becomes the title.
	prompt := "deploy with " + rawPromptPass + "\nthen check it"
	shell := "$ git push https://x:" + rawShellKey + "@github.com/o/r\nexit 0"
	if err := st.AppendMessages(id, []llm.Message{
		llm.UserText(prompt),
		{Role: llm.RoleTool, Parts: []llm.Part{{
			Type:       llm.PartToolResult,
			ToolCallID: "c1",
			ToolName:   "shell",
			Result:     shell + "\nusing " + rawEnvSecret + " and " + rawStoredKey,
		}}},
	}); err != nil {
		t.Fatal(err)
	}

	uiRec, err := json.Marshal(map[string]string{"kind": "tool_result", "output": shell + " " + rawStoredKey})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendUI(id, uiRec); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePlan(id, json.RawMessage(`{"steps":["rotate `+rawStoredKey+`"]}`)); err != nil {
		t.Fatal(err)
	}
	// A rewrite (compaction) takes the other write path.
	if err := st.ReplaceMessages(id, []llm.Message{llm.UserText(prompt)}); err != nil {
		t.Fatal(err)
	}

	dir := st.Dir(id)
	for _, name := range []string{"meta.json", "messages.jsonl", "ui.jsonl", "plan.json"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, raw := range []string{rawEnvSecret, rawStoredKey, rawShellKey, "hunter22FAKE"} {
			if strings.Contains(string(b), raw) {
				t.Errorf("%s holds raw secret %q", name, raw)
			}
		}
		if name != "meta.json" && !json.Valid(firstLineOrAll(b)) {
			t.Errorf("%s is no longer valid JSON after redaction", name)
		}
	}

	msgs, err := st.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 message after ReplaceMessages, got %d", len(msgs))
	}

	// The tool-result text the model sees in the transcript is masked too.
	raw, _ := os.ReadFile(filepath.Join(dir, "messages.jsonl"))
	if !strings.Contains(string(raw), "[redacted]") {
		t.Errorf("messages.jsonl has no mask; the secrets were not seen at all")
	}
}

func TestScrubLeavesCleanBytesAlone(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	in := []byte(`{"role":"user","parts":[{"type":"text","text":"fix the 3f1a9c0d7e8b6a5f4c3d2e1b0a9f8e7d6c5b4a39 commit"}]}`)
	if got := scrub(in); string(got) != string(in) {
		t.Errorf("clean message changed: %s", got)
	}
}

func TestPromptHistoryNeverHoldsRawSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	if err := AppendHistory("use "+rawStoredKey+" to deploy", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), rawStoredKey) || !strings.Contains(string(b), "[redacted]") {
		t.Errorf("history.jsonl did not mask the prompt: %s", b)
	}
}

func firstLineOrAll(b []byte) []byte {
	if i := strings.IndexByte(string(b), '\n'); i >= 0 {
		return b[:i]
	}
	return b
}
