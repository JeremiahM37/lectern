package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
)

// The test binary doubles as a minimal MCP server when SCRIBE_MCP_TEST_SERVER
// is set. Behaviour is chosen with env vars the test passes in the server
// config: SCRIBE_MCP_TEST_MODE (hang | exit | crash-once), SCRIBE_MCP_TEST_PAGE (tools
// per page).
func TestMain(m *testing.M) {
	if os.Getenv("SCRIBE_MCP_TEST_SERVER") == "1" {
		serveStdio()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testServerSpec(extra map[string]string) map[string]string {
	env := map[string]string{"SCRIBE_MCP_TEST_SERVER": "1"}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

// testTools is the catalogue the test server advertises.
func testTools(added bool) []map[string]any {
	ts := []map[string]any{
		{"name": "echo", "description": "echo text back", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
		{"name": "env", "description": "print an env var", "inputSchema": map[string]any{"type": "object"}},
		{"name": "fail", "description": "always reports isError", "inputSchema": map[string]any{"type": "object"}},
		{"name": "image", "description": "returns an image", "inputSchema": map[string]any{"type": "object"}},
		{"name": "read_only_info", "description": "read only", "inputSchema": map[string]any{"type": "object"},
			"annotations": map[string]any{"readOnlyHint": true}},
		{"name": "add_tool", "description": "adds a tool and announces it", "inputSchema": map[string]any{"type": "object"}},
	}
	if added {
		ts = append(ts, map[string]any{"name": "late", "description": "appears after add_tool", "inputSchema": map[string]any{"type": "object"}})
	}
	return ts
}

func serveStdio() {
	var (
		mu      sync.Mutex
		out     = bufio.NewWriter(os.Stdout)
		added   bool
		mode    = os.Getenv("SCRIBE_MCP_TEST_MODE")
		page, _ = strconv.Atoi(os.Getenv("SCRIBE_MCP_TEST_PAGE"))
	)
	write := func(v any) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	if mode == "exit" {
		fmt.Fprintln(os.Stderr, "fatal: missing API token")
		os.Exit(3)
	}
	// crash-once dies right after its first tool listing, the first time it
	// runs (the marker file is absent), and behaves normally afterwards.
	crashOnce := false
	if mode == "crash-once" {
		if marker := os.Getenv("SCRIBE_MCP_TEST_MARK"); marker != "" {
			if _, err := os.Stat(marker); err != nil {
				_ = os.WriteFile(marker, nil, 0o600)
				crashOnce = true
			}
		}
	}
	br := bufio.NewReader(os.Stdin)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(line, &req) == nil && len(req.ID) > 0 {
				if mode == "hang" {
					// Never answer: the client must time out.
					continue
				}
				reply := func(result any) { write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) }
				switch req.Method {
				case "initialize":
					reply(map[string]any{
						"protocolVersion": "2025-06-18",
						"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
						"serverInfo":      map[string]any{"name": "test-server", "version": "1"},
					})
				case "ping":
					reply(map[string]any{})
				case "tools/list":
					var p struct {
						Cursor string `json:"cursor"`
					}
					_ = json.Unmarshal(req.Params, &p)
					all := testTools(added)
					start, _ := strconv.Atoi(p.Cursor)
					end := len(all)
					res := map[string]any{}
					if page > 0 {
						if start+page < end {
							end = start + page
							res["nextCursor"] = strconv.Itoa(end)
						}
					}
					res["tools"] = all[start:end]
					reply(res)
					if crashOnce {
						out.Flush()
						os.Exit(1)
					}
				case "tools/call":
					var p struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					}
					_ = json.Unmarshal(req.Params, &p)
					var args map[string]string
					_ = json.Unmarshal(p.Arguments, &args)
					text := func(s string) map[string]any {
						return map[string]any{"content": []map[string]any{{"type": "text", "text": s}}}
					}
					switch p.Name {
					case "echo":
						reply(text(args["text"]))
					case "env":
						reply(text(os.Getenv(args["name"])))
					case "fail":
						reply(map[string]any{"content": []map[string]any{{"type": "text", "text": "boom"}}, "isError": true})
					case "image":
						reply(map[string]any{"content": []map[string]any{
							{"type": "image", "data": "AAAA", "mimeType": "image/png"},
							{"type": "text", "text": "caption"},
						}})
					case "read_only_info":
						reply(text("info"))
					case "add_tool":
						added = true
						reply(text("added"))
						write(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
					case "late":
						if !added {
							reply(map[string]any{"content": []map[string]any{{"type": "text", "text": "not yet"}}, "isError": true})
							break
						}
						reply(text("late ok"))
					default:
						write(map[string]any{"jsonrpc": "2.0", "id": req.ID,
							"error": map[string]any{"code": -32602, "message": "unknown tool: " + p.Name}})
					}
				default:
					write(map[string]any{"jsonrpc": "2.0", "id": req.ID,
						"error": map[string]any{"code": -32601, "message": "no such method"}})
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// newTestHTTPServer speaks Streamable HTTP. Responses are JSON for
// initialize and tools/call and SSE for tools/list, to exercise both
// reply paths. The GET stream announces list_changed once it is opened.
func newTestHTTPServer(t *testing.T, wantAuth string) (*httptest.Server, *httpState) {
	st := &httpState{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantAuth != "" && r.Header.Get("Authorization") != wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodGet {
			if r.Header.Get("Mcp-Session-Id") != "sess-42" {
				http.Error(w, "no session", http.StatusBadRequest)
				return
			}
			st.mu.Lock()
			st.added = true
			st.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if r.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
			http.Error(w, "missing protocol header", http.StatusBadRequest)
			return
		}
		if req.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "sess-42" {
			http.Error(w, "missing session", http.StatusBadRequest)
			return
		}
		if len(req.ID) == 0 { // notification
			w.WriteHeader(http.StatusAccepted)
			return
		}
		st.mu.Lock()
		added := st.added
		st.mu.Unlock()
		resp := func(result any) map[string]any {
			return map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", "sess-42")
			json.NewEncoder(w).Encode(resp(map[string]any{"protocolVersion": "2025-06-18",
				"capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "http-server"}}))
		case "tools/list":
			w.Header().Set("Content-Type", "text/event-stream")
			all := testTools(added)
			b, _ := json.Marshal(resp(map[string]any{"tools": all[:2]}))
			if added {
				b, _ = json.Marshal(resp(map[string]any{"tools": all}))
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
		case "tools/call":
			var p struct {
				Arguments map[string]string `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp(map[string]any{
				"content": []map[string]any{{"type": "text", "text": "http:" + p.Arguments["text"]}}}))
		default:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "no such method"}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, st
}

type httpState struct {
	mu    sync.Mutex
	added bool
}
