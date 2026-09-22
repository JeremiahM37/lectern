package agents

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexMCPArgsParseWithInstalledCLI(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex is not installed")
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("HOME", home)
	args, err := CodexMCPArgs(map[string]any{"ops_tools": map[string]any{
		"command": "python3", "args": []any{"-m", "ops"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cmdArgs := append(args, "mcp", "list")
	out, err := exec.Command("codex", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("codex rejected generated overrides: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ops_tools") || !strings.Contains(string(out), "python3") {
		t.Fatalf("Codex did not report configured server: %s", out)
	}
}

func TestMCPPayloadUsesStandardShape(t *testing.T) {
	raw, err := MCPPayload(map[string]any{"demo": map[string]any{"command": "srv"}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["mcpServers"]; !ok {
		t.Fatalf("payload: %s", raw)
	}
}

func TestCodexMCPArgsAreSortedAndAdditive(t *testing.T) {
	args, err := CodexMCPArgs(map[string]any{
		"z_server": map[string]any{"args": []any{"--x", "a b"}, "command": "srv", "env": map[string]any{"TOKEN": "secret"}},
		"alpha":    map[string]any{"url": "https://example.test/mcp", "headers": map[string]any{"Authorization": "Bearer x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `mcp_servers.alpha.url="https://example.test/mcp"`) ||
		!strings.Contains(joined, `mcp_servers.z_server.args=["--x","a b"]`) {
		t.Fatalf("args: %v", args)
	}
	if strings.Contains(joined, "CODEX_HOME") {
		t.Fatalf("must not replace Codex home: %v", args)
	}
}

func TestCodexMCPArgsRejectsMalformedServer(t *testing.T) {
	if _, err := CodexMCPArgs(map[string]any{"bad": "not-an-object"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCodexMCPArgsRejectsNamesTheCLICannotParse(t *testing.T) {
	if _, err := CodexMCPArgs(map[string]any{"ops.tools": map[string]any{"command": "python3"}}); err == nil {
		t.Fatal("dotted server names must fail explicitly")
	}
}

func TestInteractiveMCPInstallUsesPrivateExclusiveRuntime(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required on agent targets")
	}
	payload := []byte(`{"mcpServers":{"ops":{"command":"python3"}}}`)
	root := t.TempDir()
	state := filepath.Join(root, "state")
	foreign := t.TempDir()
	os.MkdirAll(state, 0700)
	os.Symlink(foreign, filepath.Join(state, "lectern"))
	rel := InteractiveMCPRel(7, "nonce")
	cmd := exec.Command("bash", "-c", MCPInstallCommand(rel, payload))
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state)
	if err := cmd.Run(); err == nil {
		t.Fatal("symlinked state parent must be rejected")
	}
	root = t.TempDir()
	state = filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(state, "lectern"), 0700); err != nil {
		t.Fatal(err)
	}
	interactive := filepath.Join(state, "lectern", "mcp")
	if err := os.Symlink(foreign, interactive); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("bash", "-c", MCPInstallCommand(rel, payload))
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state)
	if err := cmd.Run(); err == nil {
		t.Fatal("symlinked MCP parent must be rejected")
	}
	root = t.TempDir()
	state = filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(state, "lectern", "mcp"), 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, filepath.FromSlash(strings.TrimSuffix(rel, "/mcp.json")))
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	foreignDest := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(foreignDest, []byte("foreign"), 0640); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("bash", "-c", MCPInstallCommand(rel, payload))
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state)
	if err := cmd.Run(); err == nil {
		t.Fatal("existing runtime leaf must make installation fail")
	}
	got, _ := os.ReadFile(foreignDest)
	info, _ := os.Stat(foreignDest)
	if string(got) != "foreign" || info.Mode().Perm() != 0640 {
		t.Fatalf("foreign config changed: body=%q mode=%o", got, info.Mode().Perm())
	}
	root = t.TempDir()
	state = filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(state, "lectern"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("bash", "-c", MCPInstallCommand(rel, payload))
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	dest := filepath.Join(state, filepath.FromSlash(rel))
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("published MCP payload: %q (%v)", got, err)
	}
	info, err = os.Stat(dest)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("published MCP mode: %o (%v)", info.Mode().Perm(), err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(dest), ".mcp.tmp")); !os.IsNotExist(err) {
		t.Fatalf("temporary file was not removed: %v", err)
	}
	if info, err := os.Stat(filepath.Dir(dest)); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("runtime leaf mode: %o (%v)", info.Mode().Perm(), err)
	}

	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	root = t.TempDir()
	state = filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(state, "lectern"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(state, "lectern", "mcp")); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("bash", "-c", MCPInstallCommand(rel, payload))
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state)
	if err := cmd.Run(); err == nil {
		t.Fatal("symlinked interactive parent must not be traversed")
	}
	got, _ = os.ReadFile(outside)
	if string(got) != "outside" {
		t.Fatalf("symlink target changed: %q", got)
	}
}

func TestInteractiveMCPInstallRejectsParentReplacementBeforeTraversal(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required on agent targets")
	}
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(state, "lectern"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	bin := t.TempDir()
	python := filepath.Join(bin, "python3")
	body := "#!/bin/sh\nset -eu\nmv -- \"$ADK_STATE/lectern\" \"$ADK_STATE/original-lectern\"\nln -s -- \"$ADK_OUTSIDE\" \"$ADK_STATE/lectern\"\nexec \"$ADK_REAL_PYTHON\" \"$@\"\n"
	if err := os.WriteFile(python, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	realPython, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	rel := InteractiveMCPRel(8, "replacement")
	cmd := exec.Command("bash", "-c", MCPInstallCommand(rel, []byte(`{"mcpServers":{}}`)))
	cmd.Env = append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"), "XDG_STATE_HOME="+state,
		"ADK_STATE="+state,
		"ADK_OUTSIDE="+outside, "ADK_REAL_PYTHON="+realPython)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatal("parent replacement must be rejected")
	} else if len(out) == 0 {
		t.Fatal("replacement rejection did not report an error")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("foreign replacement target changed: %v", err)
	}
}
