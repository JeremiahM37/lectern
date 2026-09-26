package mcp

// Covers the "connection confirmation" half of the Settings connect-tools
// card: when a real MCP client (Claude Code, Codex, ...) says hello, this
// server records it with the real lectern API so GET /api/mcp-clients can
// show "connected · used N ago" instead of a plain button.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestInitializeReportsClientSeen(t *testing.T) {
	_, base := newTestApp(t)
	s := New(base, "")

	resp, send := s.handleRequest(request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"clientInfo":{"name":"claude-code","version":"9.9.9"}}`),
	})
	if !send {
		t.Fatal("initialize must get a reply")
	}
	if resp.Error != nil {
		t.Fatalf("initialize errored: %+v", resp.Error)
	}

	// reportClientSeen fires in the background — poll for the write to land
	// rather than assuming it beat this goroutine to the assertion.
	deadline := time.Now().Add(3 * time.Second)
	var rows []map[string]any
	for time.Now().Before(deadline) {
		httpResp, err := http.Get(base + "/api/mcp-clients")
		if err != nil {
			t.Fatal(err)
		}
		_ = json.NewDecoder(httpResp.Body).Decode(&rows)
		httpResp.Body.Close()
		for _, row := range rows {
			if row["id"] == "claude-code" && row["last_seen"] != nil {
				seen, _ := row["last_seen"].(map[string]any)
				if seen["version"] == "9.9.9" {
					return // recorded — test passes
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("claude-code's initialize was never recorded as seen: %+v", rows)
}

func TestInitializeIgnoresMissingClientInfo(t *testing.T) {
	_, base := newTestApp(t)
	s := New(base, "")
	// No clientInfo at all — must not panic, and must still answer normally.
	resp, send := s.handleRequest(request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize", Params: json.RawMessage(`{}`),
	})
	if !send || resp.Error != nil {
		t.Fatalf("initialize with no clientInfo must still succeed: %+v", resp)
	}
}
