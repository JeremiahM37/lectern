package mcp

// Integration tests for the Streamable-HTTP web-connector transport
// (http.go): the full OAuth lifecycle end to end against a real
// internal/app.App standing in for the Lectern control plane
// (newTestApp, from tools_test.go), then tools/list and tools/call over
// /mcp with the resulting bearer token. internal/oauth's own tests already
// cover the authorization server's protocol details in isolation (DCR,
// PKCE, refresh rotation, revocation, redirect_uri handling, the
// public/private listener split); this file is about what a bearer token
// actually unlocks once it reaches the MCP tool surface.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/oauth"
)

// fakeIdentity is a minimal stand-in for cmd/lectern's tailscale-backed
// oauth.Identity, keyed by the request's remote host.
type fakeIdentity map[string]string

func (f fakeIdentity) Identify(r *http.Request) (string, bool) {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	login, ok := f[host]
	return login, ok
}

const testOwnerIP = "100.64.0.9"

// newTestOAuthHandler builds an oauth.Handler wired to a fake owner
// identity, matching what cmd/lectern/mcp_http.go assembles from env vars.
func newTestOAuthHandler(t *testing.T) *oauth.Handler {
	t.Helper()
	store, err := oauth.Open(filepath.Join(t.TempDir(), "oauth.db"))
	if err != nil {
		t.Fatalf("oauth.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h, err := oauth.New(oauth.Config{
		Store:         store,
		PublicBase:    "https://mcp.example.com",
		AuthorizeBase: "https://consent.example.com",
		Identity:      fakeIdentity{testOwnerIP: "jam@github"},
		AllowedLogins: []string{"jam@github"},
		AdminToken:    "admin-tok",
	})
	if err != nil {
		t.Fatalf("oauth.New: %v", err)
	}
	return h
}

// issueOAuthToken walks DCR -> authorize (approved as the owner, with
// exactly `scopes` checked) -> token, and returns the resulting access
// token.
func issueOAuthToken(t *testing.T, h *oauth.Handler, scopes []string) string {
	t.Helper()
	pub, priv := h.PublicMux(), h.PrivateMux()

	regBody, _ := json.Marshal(map[string]any{
		"redirect_uris": []string{"http://localhost:1/callback"},
		"client_name":   "Test Connector",
	})
	regReq := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(regBody))
	regReq.Header.Set("Content-Type", "application/json")
	regRec := httptest.NewRecorder()
	pub.ServeHTTP(regRec, regReq)
	if regRec.Code != http.StatusCreated {
		t.Fatalf("register: %d: %s", regRec.Code, regRec.Body.String())
	}
	var reg map[string]any
	_ = json.Unmarshal(regRec.Body.Bytes(), &reg)
	clientID := reg["client_id"].(string)

	verifier := "test-code-verifier-0123456789-abcdefghijklmnop"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	authQ := url.Values{
		"response_type": {"code"}, "client_id": {clientID},
		"redirect_uri":   {"http://localhost:1/callback"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}.Encode()
	getReq := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+authQ, nil)
	getReq.RemoteAddr = testOwnerIP + ":1"
	getRec := httptest.NewRecorder()
	priv.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET authorize: %d: %s", getRec.Code, getRec.Body.String())
	}
	m := regexp.MustCompile(`name="req" value="([^"]+)"`).FindStringSubmatch(getRec.Body.String())
	if m == nil {
		t.Fatalf("no req token in consent page:\n%s", getRec.Body.String())
	}

	form := url.Values{"req": {m[1]}, "decision": {"approve"}}
	form["scope"] = scopes
	postReq := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.RemoteAddr = testOwnerIP + ":1"
	postRec := httptest.NewRecorder()
	priv.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusFound {
		t.Fatalf("POST authorize: %d: %s", postRec.Code, postRec.Body.String())
	}
	loc, _ := url.Parse(postRec.Header().Get("Location"))
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in redirect %s", loc)
	}

	tokForm := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {"http://localhost:1/callback"}, "code_verifier": {verifier},
		"resource": {h.Resource},
	}
	tokReq := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(tokForm.Encode()))
	tokReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokRec := httptest.NewRecorder()
	pub.ServeHTTP(tokRec, tokReq)
	if tokRec.Code != http.StatusOK {
		t.Fatalf("token: %d: %s", tokRec.Code, tokRec.Body.String())
	}
	var tok map[string]any
	_ = json.Unmarshal(tokRec.Body.Bytes(), &tok)
	access, _ := tok["access_token"].(string)
	if access == "" {
		t.Fatalf("no access_token in %v", tok)
	}
	return access
}

// rpc posts one JSON-RPC request to h and decodes the JSON-RPC envelope.
func rpc(t *testing.T, h http.Handler, bearer, method string, params any) (int, map[string]any) {
	t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		req["params"] = params
	}
	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		httpReq.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httpReq)
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden || rec.Code == http.StatusMethodNotAllowed {
		return rec.Code, nil
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON-RPC response (status %d): %s", rec.Code, rec.Body.String())
	}
	return rec.Code, resp
}

func toolNames(t *testing.T, resp map[string]any) map[string]bool {
	t.Helper()
	result, _ := resp["result"].(map[string]any)
	list, _ := result["tools"].([]any)
	out := map[string]bool{}
	for _, raw := range list {
		m, _ := raw.(map[string]any)
		if name, _ := m["name"].(string); name != "" {
			out[name] = true
		}
	}
	return out
}

// TestFullWebConnectorFlow: DCR -> authorize -> token -> tools/list ->
// tools/call, end to end, against a real Lectern API fake.
func TestFullWebConnectorFlow(t *testing.T) {
	_, baseURL := newTestApp(t)
	oh := newTestOAuthHandler(t)
	s := New(baseURL, "")
	s.Remote = true
	s.OAuth = oh

	access := issueOAuthToken(t, oh, []string{oauth.ScopeRead, oauth.ScopeWrite})
	handler := s.HTTPHandler()

	status, resp := rpc(t, handler, access, "tools/list", nil)
	if status != http.StatusOK {
		t.Fatalf("tools/list: want 200, got %d", status)
	}
	names := toolNames(t, resp)
	if !names["list_sessions"] || !names["start_session"] {
		t.Fatalf("expected both read and write tools listed, got %v", names)
	}
	if names["decide_approval"] {
		t.Fatal("decide_approval must never appear in the web connector's tools/list")
	}

	// tools/call: board_summary, a read tool.
	status, resp = rpc(t, handler, access, "tools/call", map[string]any{
		"name": "board_summary", "arguments": map[string]any{},
	})
	if status != http.StatusOK {
		t.Fatalf("tools/call board_summary: want 200, got %d: %v", status, resp)
	}
	if isErr, _ := resp["result"].(map[string]any)["isError"].(bool); isErr {
		t.Fatalf("board_summary returned an error result: %v", resp)
	}
}

// TestWebConnectorMissingOrBadBearerIsUnauthorized covers the plain 401 path.
func TestWebConnectorMissingOrBadBearerIsUnauthorized(t *testing.T) {
	_, baseURL := newTestApp(t)
	oh := newTestOAuthHandler(t)
	s := New(baseURL, "")
	s.Remote, s.OAuth = true, oh
	handler := s.HTTPHandler()

	for _, bearer := range []string{"", "not-a-real-token"} {
		status, _ := rpc(t, handler, bearer, "tools/list", nil)
		if status != http.StatusUnauthorized {
			t.Fatalf("bearer %q: want 401, got %d", bearer, status)
		}
	}
}

// TestWebConnectorScopeFiltering: a read-only token cannot see or call
// start_session; a write-scoped call it cannot cover 403s with
// insufficient_scope.
func TestWebConnectorScopeFiltering(t *testing.T) {
	_, baseURL := newTestApp(t)
	oh := newTestOAuthHandler(t)
	s := New(baseURL, "")
	s.Remote, s.OAuth = true, oh
	handler := s.HTTPHandler()

	readOnly := issueOAuthToken(t, oh, []string{oauth.ScopeRead})

	_, resp := rpc(t, handler, readOnly, "tools/list", nil)
	names := toolNames(t, resp)
	if names["start_session"] {
		t.Fatal("a read-scoped token must not see start_session in tools/list")
	}
	if !names["list_sessions"] {
		t.Fatal("a read-scoped token must still see list_sessions")
	}

	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "start_session", "arguments": map[string]any{"prompt": "x", "scratch": true}}})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+readOnly)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("calling start_session with a read-only token: want 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "insufficient_scope") {
		t.Fatalf("expected an insufficient_scope challenge, got %q", rec.Header().Get("WWW-Authenticate"))
	}
}

// TestWebConnectorDecideApprovalNeverReachable: refused on tools/call
// regardless of scope, and never listed — checked again here (beyond
// scope_test.go's unit coverage) because this is the property that actually
// protects an owner: end to end over the transport a remote LLM speaks.
func TestWebConnectorDecideApprovalNeverReachable(t *testing.T) {
	_, baseURL := newTestApp(t)
	oh := newTestOAuthHandler(t)
	s := New(baseURL, "")
	s.Remote, s.OAuth = true, oh
	handler := s.HTTPHandler()

	full := issueOAuthToken(t, oh, []string{oauth.ScopeRead, oauth.ScopeWrite})
	status, resp := rpc(t, handler, full, "tools/call", map[string]any{
		"name": "decide_approval", "arguments": map[string]any{"approval_id": 1, "decision": "approved"},
	})
	if status != http.StatusOK {
		t.Fatalf("want 200 (an in-band tool error, not a transport error), got %d", status)
	}
	result, _ := resp["result"].(map[string]any)
	isErr, _ := result["isError"].(bool)
	if !isErr {
		t.Fatalf("expected decide_approval to be refused as a tool error, got %v", resp)
	}
}

// TestWebConnectorFilesRejectedInlineFilesAccepted is the chat-friendly
// file-handover contract: a remote caller cannot point start_session at a
// path on the machine Lectern's MCP server runs on, but CAN hand over a
// document's content directly via inline_files, which lands as a normal
// multipart attachment.
func TestWebConnectorFilesRejectedInlineFilesAccepted(t *testing.T) {
	withFastPolling(t)
	var uploadedNames []string
	var uploadedBody map[string][]byte

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":9,"name":"sess-9","status":"waiting"}`)
	})
	mux.HandleFunc("GET /api/sessions/9", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":9,"name":"sess-9","status":"waiting"}`)
	})
	mux.HandleFunc("POST /api/sessions/9/attachments", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatal(err)
		}
		fh := r.MultipartForm.File["file"][0]
		f, _ := fh.Open()
		defer f.Close()
		buf := new(bytes.Buffer)
		buf.ReadFrom(f)
		if uploadedBody == nil {
			uploadedBody = map[string][]byte{}
		}
		uploadedNames = append(uploadedNames, fh.Filename)
		uploadedBody[fh.Filename] = buf.Bytes()
		fmt.Fprintf(w, `{"name":%q,"path":"/w/.lectern/context/%s"}`, fh.Filename, fh.Filename)
	})
	mux.HandleFunc("POST /api/sessions/9/send", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"sent":true}`)
	})
	fakeAPI := httptest.NewServer(mux)
	defer fakeAPI.Close()

	oh := newTestOAuthHandler(t)
	s := New(fakeAPI.URL, "")
	s.Remote, s.OAuth = true, oh
	handler := s.HTTPHandler()
	access := issueOAuthToken(t, oh, []string{oauth.ScopeRead, oauth.ScopeWrite})

	// `files` must be refused for the remote caller.
	status, resp := rpc(t, handler, access, "tools/call", map[string]any{
		"name": "start_session",
		"arguments": map[string]any{
			"workdir": "/w", "prompt": "build it",
			"files": []any{"/etc/passwd"},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("want 200 (in-band tool error), got %d", status)
	}
	result, _ := resp["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("expected files to be refused for a remote caller, got %v", resp)
	}
	if text := firstText(result); !strings.Contains(text, "web connector") {
		t.Fatalf("expected an explanation mentioning the web connector, got %q", text)
	}

	// inline_files, by contrast, must work — one text, one base64.
	status, resp = rpc(t, handler, access, "tools/call", map[string]any{
		"name": "start_session",
		"arguments": map[string]any{
			"workdir": "/w", "prompt": "build it",
			"inline_files": []any{
				map[string]any{"name": "spec.md", "content": "the spec"},
				map[string]any{"name": "data.bin", "content": base64.StdEncoding.EncodeToString([]byte("binary!")), "encoding": "base64"},
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("want 200, got %d: %v", status, resp)
	}
	result, _ = resp["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("inline_files should be accepted for a remote caller, got %v", resp)
	}
	if len(uploadedNames) != 2 {
		t.Fatalf("expected 2 uploads, got %v", uploadedNames)
	}
	if string(uploadedBody["spec.md"]) != "the spec" {
		t.Fatalf("text inline file mismatch: %q", uploadedBody["spec.md"])
	}
	if string(uploadedBody["data.bin"]) != "binary!" {
		t.Fatalf("base64 inline file did not decode correctly: %q", uploadedBody["data.bin"])
	}
}

// firstText pulls the first content block's text out of a tool result, for
// assertions on error messages.
func firstText(result map[string]any) string {
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	m, _ := content[0].(map[string]any)
	text, _ := m["text"].(string)
	return text
}

// TestWebConnectorStaticBearerIsUnrestricted checks the InboundToken path
// independent of OAuth: a static bearer sees every tool except
// decide_approval (still excluded — the web connector rule, not a scope
// rule) and is not scope-limited otherwise.
func TestWebConnectorStaticBearerIsUnrestricted(t *testing.T) {
	_, baseURL := newTestApp(t)
	s := New(baseURL, "")
	s.Remote = true
	s.InboundToken = "static-secret"
	handler := s.HTTPHandler()

	status, resp := rpc(t, handler, "static-secret", "tools/list", nil)
	if status != http.StatusOK {
		t.Fatalf("want 200, got %d", status)
	}
	names := toolNames(t, resp)
	if !names["start_session"] {
		t.Fatal("a static bearer must see write tools too — it is unrestricted")
	}
	if names["decide_approval"] {
		t.Fatal("decide_approval must be excluded even for an unrestricted static bearer")
	}

	status, _ = rpc(t, handler, "wrong-token", "tools/list", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong static token: want 401, got %d", status)
	}
}
