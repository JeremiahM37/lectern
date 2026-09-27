package agents

import (
	"encoding/json"
	"fmt"
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

func adapterFixture() map[string]any {
	return map[string]any{"mcpServers": map[string]any{
		"ops":   map[string]any{"command": "python3", "args": []any{"-m", "ops"}, "env": map[string]any{"TOKEN": "t"}},
		"web":   map[string]any{"type": "http", "url": "https://mcp.example/mcp", "headers": map[string]any{"Authorization": "Bearer x"}},
		"feeds": map[string]any{"type": "sse", "url": "https://mcp.example/sse"},
	}}
}

func decodeJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOpenCodeMCPPayloadTranslatesLocalAndRemote(t *testing.T) {
	raw, err := OpenCodeMCPPayload(adapterFixture())
	if err != nil {
		t.Fatal(err)
	}
	mcp := decodeJSON(t, raw)["mcp"].(map[string]any)
	ops := mcp["ops"].(map[string]any)
	if ops["type"] != "local" || fmt.Sprint(ops["command"]) != "[python3 -m ops]" || ops["environment"].(map[string]any)["TOKEN"] != "t" {
		t.Fatalf("stdio server not translated to an OpenCode local server: %v", ops)
	}
	web := mcp["web"].(map[string]any)
	if web["type"] != "remote" || web["url"] != "https://mcp.example/mcp" || web["headers"].(map[string]any)["Authorization"] != "Bearer x" {
		t.Fatalf("http server not translated to an OpenCode remote server: %v", web)
	}
	if mcp["feeds"].(map[string]any)["type"] != "remote" {
		t.Fatalf("sse server should be remote: %v", mcp["feeds"])
	}
}

func TestQwenMCPPayloadUsesHTTPURLForStreamableHTTP(t *testing.T) {
	raw, err := QwenMCPPayload(adapterFixture())
	if err != nil {
		t.Fatal(err)
	}
	servers := decodeJSON(t, raw)["mcpServers"].(map[string]any)
	if servers["web"].(map[string]any)["httpUrl"] != "https://mcp.example/mcp" || servers["web"].(map[string]any)["url"] != nil {
		t.Fatalf("http server must use httpUrl for Qwen: %v", servers["web"])
	}
	if servers["feeds"].(map[string]any)["url"] != "https://mcp.example/sse" {
		t.Fatalf("sse server must use url: %v", servers["feeds"])
	}
	ops := servers["ops"].(map[string]any)
	if ops["command"] != "python3" || fmt.Sprint(ops["args"]) != "[-m ops]" || ops["env"].(map[string]any)["TOKEN"] != "t" {
		t.Fatalf("stdio server changed: %v", ops)
	}
}

func TestCopilotMCPPayloadKeepsClaudeDocument(t *testing.T) {
	raw, err := CopilotMCPPayload(map[string]any{"ops": map[string]any{"command": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"mcpServers":{"ops":{"command":"x"}}}` {
		t.Fatalf("copilot reads the Claude document as is, got %s", raw)
	}
}

// Amp's --mcp-config takes the bare server map; the mcpServers wrapper is
// rejected by amp 0.0.1790496040 ("mcpServers: Invalid input").
func TestAmpMCPPayloadIsTheBareServerMap(t *testing.T) {
	raw, err := AmpMCPPayload(map[string]any{"mcpServers": map[string]any{
		"ops": map[string]any{"command": "x", "args": []any{"-v"}},
		"web": map[string]any{"type": "http", "url": "https://mcp.example/mcp"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ops":{"args":["-v"],"command":"x"},"web":{"type":"http","url":"https://mcp.example/mcp"}}`
	if string(raw) != want {
		t.Fatalf("amp payload:\n got %s\nwant %s", raw, want)
	}
}

// Kilo and MiMo Code are OpenCode forks that read the same "mcp" document
// from their own environment variable; Amp reads a file named on its
// command line.
func TestForkAndAmpAdaptersNameTheirOwnFile(t *testing.T) {
	for agent, want := range map[string]string{"kilo": "KILO_CONFIG", "mimo": "MIMOCODE_CONFIG"} {
		a, ok := MCPAdapterFor(agent)
		if !ok || a.EnvVar != want || len(a.Args) != 0 {
			t.Errorf("%s adapter: %+v %v", agent, a, ok)
		}
	}
	a, ok := MCPAdapterFor("amp")
	if !ok || fmt.Sprint(a.Args) != "[--mcp-config {path}]" || a.EnvVar != "" {
		t.Errorf("amp adapter: %+v %v", a, ok)
	}
}

func TestACPMCPServersUsesNameValuePairsAndReportsTransports(t *testing.T) {
	servers, needHTTP, needSSE, err := ACPMCPServers(adapterFixture())
	if err != nil {
		t.Fatal(err)
	}
	if !needHTTP || !needSSE || len(servers) != 3 {
		t.Fatalf("want http+sse and three servers, got %v %v %v", needHTTP, needSSE, servers)
	}
	raw, _ := json.Marshal(servers)
	want := `[{"headers":[],"name":"feeds","type":"sse","url":"https://mcp.example/sse"},` +
		`{"args":["-m","ops"],"command":"python3","env":[{"name":"TOKEN","value":"t"}],"name":"ops"},` +
		`{"headers":[{"name":"Authorization","value":"Bearer x"}],"name":"web","type":"http","url":"https://mcp.example/mcp"}]`
	if string(raw) != want {
		t.Fatalf("ACP servers:\n got %s\nwant %s", raw, want)
	}
}

func TestMCPAdaptersRejectUntranslatableFields(t *testing.T) {
	bad := map[string]any{"ops": map[string]any{"command": "x", "cwd": "/srv"}}
	for name, fn := range map[string]func(map[string]any) ([]byte, error){
		"opencode": OpenCodeMCPPayload, "qwen": QwenMCPPayload, "copilot": CopilotMCPPayload,
		"amp": AmpMCPPayload,
	} {
		if _, err := fn(bad); err == nil || !strings.Contains(err.Error(), `"cwd"`) {
			t.Errorf("%s should refuse a field it cannot carry, got %v", name, err)
		}
	}
	if _, _, _, err := ACPMCPServers(bad); err == nil {
		t.Error("ACP should refuse a field it cannot carry")
	}
	if _, ok := MCPAdapterFor("gemini"); ok {
		t.Error("gemini has no per-session MCP file and must not claim an adapter")
	}
}
