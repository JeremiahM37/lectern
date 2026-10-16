package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// The agent masks every tool result through Options.Redact before it enters
// history or events. This checks that the app wires the process redactor
// and that it catches a key printed by a shell command.
func TestAgentOptionsRedactToolOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	const envKey = "fake-deploy-token-99887766"
	t.Setenv("RELEASE_TOKEN", envKey)
	const storedKey = "sk-ant-api03-FAKEAPPKEY0011223344"
	if err := os.WriteFile(filepath.Join(home, "keys.json"), []byte(`{"anthropic":"`+storedKey+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	a := &App{Workdir: t.TempDir(), Cfg: config.Config{}}
	opts := a.agentOptions(agent.Model{}, "auto")
	if opts.Redact == nil {
		t.Fatal("agentOptions left Redact nil; tool output would reach history unmasked")
	}

	shell := "$ env | grep KEY\nRELEASE_TOKEN=" + envKey + "\nANTHROPIC=" + storedKey + "\nexit 0"
	got := opts.Redact(shell)
	for _, raw := range []string{envKey, storedKey} {
		if strings.Contains(got, raw) {
			t.Fatalf("raw value survived in tool output: %q", got)
		}
	}
	if !strings.Contains(got, "RELEASE_TOKEN=[redacted]") || !strings.Contains(got, "ANTHROPIC=[redacted]") {
		t.Fatalf("unexpected masking: %q", got)
	}
}
