package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

func call(t *testing.T, c *Client, name, args string) callResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := c.callTool(ctx, name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

func toolNamesOf(ts []ToolInfo) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

// stdioSpec runs this test binary as the MCP server.
func stdioSpec(t *testing.T, env map[string]string) config.MCPServer {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return config.MCPServer{Name: "test", Command: exe, Env: testServerSpec(env)}
}

func TestStdioInitializeListCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, tools, err := connect(ctx, "test", stdioSpec(t, nil), "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	want := []string{"echo", "env", "fail", "image", "read_only_info", "add_tool"}
	if got := toolNamesOf(tools); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v", got)
	}
	if !tools[4].ReadOnly || tools[0].ReadOnly {
		t.Fatalf("readOnlyHint not read: %+v", tools)
	}
	if got := call(t, c, "echo", `{"text":"hello"}`); got.IsError || formatContent(got.Content) != "hello" {
		t.Fatalf("echo = %+v", got)
	}
	if got := call(t, c, "fail", `{}`); !got.IsError || formatContent(got.Content) != "boom" {
		t.Fatalf("fail should be an error result: %+v", got)
	}
	img := formatContent(call(t, c, "image", `{}`).Content)
	if img != "[image: image/png, about 3 bytes]\ncaption" {
		t.Fatalf("image summary = %q", img)
	}
}

func TestStdioEnvAllowlist(t *testing.T) {
	// The parent holds a secret; the server must not see it.
	t.Setenv("SCRIBE_TEST_LEAK", "parent-secret")
	t.Setenv("SCRIBE_TEST_ALLOWED_VIA_CONFIG", "from-config")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	spec := stdioSpec(t, map[string]string{"SCRIBE_TEST_CONFIG_VAR": "${SCRIBE_TEST_ALLOWED_VIA_CONFIG}"})
	c, _, err := connect(ctx, "test", spec, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := formatContent(call(t, c, "env", `{"name":"SCRIBE_TEST_LEAK"}`).Content); got != "" {
		t.Fatalf("server saw parent secret: %q", got)
	}
	if got := formatContent(call(t, c, "env", `{"name":"SCRIBE_TEST_CONFIG_VAR"}`).Content); got != "from-config" {
		t.Fatalf("configured env not passed (or not expanded): %q", got)
	}
	if got := formatContent(call(t, c, "env", `{"name":"PATH"}`).Content); got == "" {
		t.Fatal("PATH missing from the allowlist")
	}
}

func TestPagination(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	spec := stdioSpec(t, map[string]string{"SCRIBE_MCP_TEST_PAGE": "2"})
	c, tools, err := connect(ctx, "test", spec, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if len(tools) != 6 {
		t.Fatalf("paged listing returned %d tools, want 6", len(tools))
	}
}

func TestJSONRPCErrorBecomesErrorResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, _, err := connect(ctx, "test", stdioSpec(t, nil), "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.callTool(ctx, "nope", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("expected JSON-RPC error, got %v", err)
	}
}

func TestStartupFailureReportsStderr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err := connect(ctx, "test", stdioSpec(t, map[string]string{"SCRIBE_MCP_TEST_MODE": "exit"}), "t", nil)
	if err == nil || !strings.Contains(err.Error(), "missing API token") {
		t.Fatalf("err = %v, want the server's stderr", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("a dead server was not noticed promptly")
	}
}

func TestHangingServerHitsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _, err := connect(ctx, "test", stdioSpec(t, map[string]string{"SCRIBE_MCP_TEST_MODE": "hang"}), "t", nil)
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("err = %v, want a deadline error", err)
	}
}

func TestHTTPTransportJSONAndSSE(t *testing.T) {
	srv, _ := newTestHTTPServer(t, "Bearer tok")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	spec := config.MCPServer{Name: "web", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer tok"}}
	c, tools, err := connect(ctx, "web", spec, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// tools/list arrives as SSE, initialize and tools/call as JSON; the
	// session id from initialize must be sent on every later request.
	if got := toolNamesOf(tools); len(got) != 2 || got[0] != "echo" {
		t.Fatalf("tools via SSE = %v", got)
	}
	if got := formatContent(call(t, c, "echo", `{"text":"hi"}`).Content); got != "http:hi" {
		t.Fatalf("http call = %q", got)
	}
}

func TestHTTPRejectsMissingAuth(t *testing.T) {
	srv, _ := newTestHTTPServer(t, "Bearer tok")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, err := connect(ctx, "web", config.MCPServer{Name: "web", URL: srv.URL}, "t", nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("err = %v, want HTTP 401", err)
	}
}

func TestHTTPGetStreamDeliversListChanged(t *testing.T) {
	srv, _ := newTestHTTPServer(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	changed := make(chan string, 4)
	c, tools, err := connect(ctx, "web", config.MCPServer{Name: "web", URL: srv.URL}, "t", func(m string) { changed <- m })
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if len(tools) != 2 {
		t.Fatalf("initial tools = %d", len(tools))
	}
	select {
	case m := <-changed:
		if m != "notifications/tools/list_changed" {
			t.Fatalf("notification = %q", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("list_changed never arrived over the GET stream")
	}
}

func TestStdioListChangedNotification(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	changed := make(chan string, 4)
	c, _, err := connect(ctx, "test", stdioSpec(t, nil), "t", func(m string) { changed <- m })
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	call(t, c, "add_tool", `{}`)
	select {
	case m := <-changed:
		if m != "notifications/tools/list_changed" {
			t.Fatalf("notification = %q", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no list_changed notification")
	}
	list, err := c.listTools(ctx)
	if err != nil || len(list) != 7 {
		t.Fatalf("after add_tool: %d tools, err %v", len(list), err)
	}
}

func TestCloseFailsPendingCalls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, _, err := connect(ctx, "test", stdioSpec(t, nil), "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	c.Close() // idempotent
	if _, err := c.callTool(ctx, "echo", json.RawMessage(`{}`)); err == nil {
		t.Fatal("call on a closed client succeeded")
	}
}

func TestToolNameSanitising(t *testing.T) {
	if got := toolName("my server.v2", "get/thing"); got != "mcp__my_server_v2__get_thing" {
		t.Fatalf("got %q", got)
	}
	long := toolName("s", strings.Repeat("x", 100))
	if len(long) != 64 {
		t.Fatalf("length %d, want 64", len(long))
	}
}
