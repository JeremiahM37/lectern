// Package mcp exposes lectern over the Model Context Protocol on stdio, so any
// MCP client — Claude Code, an mcpo bridge, a chat bot — can file and steer
// tasks.
//
// It is a client of lectern's own HTTP API rather than of the database, so it
// works against a control plane running anywhere and can never bypass a
// validation the API enforces.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// protocolVersion is the MCP revision this server implements.
const protocolVersion = "2024-11-05"

// Server speaks JSON-RPC over stdio and forwards to an lectern API.
type Server struct {
	API   string
	Token string
	HTTP  *http.Client
}

// New builds a server pointed at an lectern instance.
func New(api, token string) *Server {
	return &Server{
		API:   strings.TrimRight(api, "/"),
		Token: token,
		HTTP:  &http.Client{Timeout: 60 * time.Second},
	}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve runs the stdio loop until the client closes the connection.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	// MCP messages are newline-delimited JSON; agent prompts can be long, so the
	// default 64KB scanner limit is not enough
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			continue // a malformed frame is the client's problem, not fatal here
		}
		resp, send := s.handle(req)
		if !send {
			continue // a notification gets no reply
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s *Server) handle(req request) (response, bool) {
	resp := response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "lectern", "version": version.Version},
		}
	case "tools/list":
		resp.Result = map[string]any{"tools": toolSchemas()}
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: err.Error()}
			break
		}
		out, err := s.call(params.Name, params.Arguments)
		if err != nil {
			// a tool error is reported IN the result, so the model can read and
			// react to it rather than the whole call failing
			resp.Result = map[string]any{
				"content": []any{map[string]any{"type": "text", "text": err.Error()}},
				"isError": true,
			}
			break
		}
		raw, _ := json.MarshalIndent(out, "", "  ")
		resp.Result = map[string]any{
			"content": []any{map[string]any{"type": "text", "text": string(raw)}},
		}
	case "ping":
		resp.Result = map[string]any{}
	default:
		if strings.HasPrefix(req.Method, "notifications/") || len(req.ID) == 0 {
			return response{}, false
		}
		resp.Error = &rpcError{Code: -32601, Message: "unknown method " + req.Method}
	}
	return resp, true
}

// ---- the HTTP client --------------------------------------------------------

func (s *Server) api(method, path string, body any) (any, error) {
	return s.apiWithClient(s.HTTP, method, path, body)
}

// apiLong is for the one call that legitimately takes as long as a build: the
// wait. The default client's minute would turn every wait into an error.
func (s *Server) apiLong(method, path string, body any, timeout time.Duration) (any, error) {
	return s.apiWithClient(&http.Client{Timeout: timeout + 15*time.Second}, method, path, body)
}

func (s *Server) apiWithClient(client *http.Client, method, path string, body any) (any, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, s.API+"/api"+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lectern unreachable at %s: %w", s.API, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s %s: %s — %s", method, path, resp.Status,
			strings.TrimSpace(string(raw)))
	}
	var out any
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Server) list(path string) ([]map[string]any, error) {
	raw, err := s.api("GET", path, nil)
	if err != nil {
		return nil, err
	}
	items, _ := raw.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, i := range items {
		if m, ok := i.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *Server) object(path string) (map[string]any, error) {
	raw, err := s.api("GET", path, nil)
	if err != nil {
		return nil, err
	}
	m, _ := raw.(map[string]any)
	return m, nil
}
