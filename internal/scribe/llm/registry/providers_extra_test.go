package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/openai"
)

// extraSSE writes Chat Completions chunks as an event stream.
func extraSSE(w http.ResponseWriter, payloads ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	for _, p := range payloads {
		fmt.Fprintf(w, "data: %s\n\n", p)
		if fl != nil {
			fl.Flush()
		}
	}
}

// extraRecord is what the fake server saw on its last chat request.
type extraRecord struct {
	mu   sync.Mutex
	path string
	auth string
	body map[string]any
}

func (e *extraRecord) snapshot() (string, string, map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.path, e.auth, e.body
}

// extraRegistry builds a registry with an isolated home and the given
// provider's base pointed at base. The built-in definitions are copied by New,
// so the redirect only affects this registry.
func extraRegistry(t *testing.T, id, base string) *Registry {
	t.Helper()
	isolate(t)
	r := New(customCfg("unused", "http://127.0.0.1:1", ""))
	i, ok := r.byID[id]
	if !ok {
		t.Fatalf("provider %q not in registry", id)
	}
	if base != "" {
		r.defs[i].BaseURL = base
	}
	return r
}

// TestExtraProvidersResolve checks every added provider resolves through
// Resolve("provider/model"), keeping slashes in the model part, and fails
// cleanly when its key is missing.
func TestExtraProvidersResolve(t *testing.T) {
	cases := []struct {
		id, env, base string
	}{
		{"bedrock", "AWS_BEARER_TOKEN_BEDROCK", "bedrock-runtime."},
		{"github-models", "GITHUB_TOKEN", "https://models.github.ai/inference"},
		{"vercel_gateway", "AI_GATEWAY_API_KEY", "https://ai-gateway.vercel.sh/v1"},
		{"opencode-zen", "OPENCODE_ZEN_API_KEY", "https://opencode.ai/zen/v1"},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			r := extraRegistry(t, c.id, "")

			// No key: the error names the variable to set.
			if _, _, err := r.Resolve(c.id + "/vendor/model-x"); err == nil || !strings.Contains(err.Error(), c.env) {
				t.Fatalf("missing-key error = %v, want mention of %s", err, c.env)
			}

			t.Setenv(c.env, "tok-env")
			p, model, err := r.Resolve(c.id + "/vendor/model-x")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if p.ID() != c.id || model != "vendor/model-x" {
				t.Fatalf("Resolve = %s, %q", p.ID(), model)
			}
			if _, ok := p.(*openai.Provider); !ok {
				t.Fatalf("provider type = %T, want openai adapter", p)
			}
			info, ok := r.Lookup(c.id)
			if !ok || !strings.Contains(info.BaseURL, c.base) {
				t.Fatalf("base = %q, want it to contain %q", info.BaseURL, c.base)
			}
		})
	}
}

// TestGitHubModelsAcceptsLegacyEnv checks the spec's variable works too, and
// that GITHUB_TOKEN wins when both are set.
func TestGitHubModelsAcceptsLegacyEnv(t *testing.T) {
	r := extraRegistry(t, "github-models", "")
	t.Setenv("GITHUB_MODELS_API_KEY", "legacy")
	if _, _, err := r.Resolve("github-models/openai/gpt-4.1"); err != nil {
		t.Fatalf("GITHUB_MODELS_API_KEY not accepted: %v", err)
	}
	t.Setenv("GITHUB_TOKEN", "primary")
	if info, _ := r.Lookup("github-models"); info.KeySource != "env:GITHUB_TOKEN" {
		t.Fatalf("KeySource = %q, want env:GITHUB_TOKEN", info.KeySource)
	}
}

// TestBedrockBaseURL covers the region handling used when the built-in
// definition is created at process start from AWS_REGION.
func TestBedrockBaseURL(t *testing.T) {
	if got := bedrockBaseURL("eu-west-1"); got != "https://bedrock-runtime.eu-west-1.amazonaws.com/openai/v1" {
		t.Fatalf("eu-west-1 = %s", got)
	}
	if got := bedrockBaseURL(""); got != "https://bedrock-runtime.us-east-1.amazonaws.com/openai/v1" {
		t.Fatalf("empty region = %s", got)
	}
	t.Setenv("AWS_REGION", "  ")
	if got := awsRegionFromEnv(); got != "us-east-1" {
		t.Fatalf("blank AWS_REGION = %q, want us-east-1", got)
	}
	t.Setenv("AWS_REGION", "ap-southeast-2")
	if got := awsRegionFromEnv(); got != "ap-southeast-2" {
		t.Fatalf("AWS_REGION = %q", got)
	}
}

// TestProxyIsKeyOptionalAndHonoursEnv checks the proxy resolves with no key,
// takes its base from SCRIBE_PROXY_URL (appending /v1 to a bare host), and
// sends PROXY_API_KEY when it is set.
func TestProxyIsKeyOptionalAndHonoursEnv(t *testing.T) {
	r := extraRegistry(t, "proxy", "")
	t.Setenv("SCRIBE_PROXY_URL", "127.0.0.1:9999")
	p, model, err := r.Resolve("proxy/claude-sonnet-5-5")
	if err != nil {
		t.Fatalf("Resolve without key: %v", err)
	}
	if p.ID() != "proxy" || model != "claude-sonnet-5-5" {
		t.Fatalf("Resolve = %s, %q", p.ID(), model)
	}
	if info, _ := r.Lookup("proxy"); info.BaseURL != "http://127.0.0.1:9999/v1" {
		t.Fatalf("base = %q", info.BaseURL)
	}

	t.Setenv("SCRIBE_PROXY_URL", "")
	if info, _ := r.Lookup("proxy"); info.BaseURL != proxyDefaultBase {
		t.Fatalf("default base = %q, want %q", info.BaseURL, proxyDefaultBase)
	}
	t.Setenv("PROXY_API_KEY", "local-key")
	if info, _ := r.Lookup("proxy"); !info.HasKey || info.KeySource != "env:PROXY_API_KEY" {
		t.Fatalf("proxy key state = %+v", info)
	}
}

// extraTextAndToolStream is a stream with text, then one tool call split
// across two chunks, then usage.
func extraTextAndToolStream(w http.ResponseWriter, _ *http.Request) {
	extraSSE(w,
		`{"choices":[{"delta":{"role":"assistant","content":"Looking. "}}]}`,
		`{"choices":[{"delta":{"content":"Done."}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"main.go\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":4}}`,
		`[DONE]`,
	)
}

// TestExtraProvidersRoundTrip sends a streamed request through Resolve for
// each provider against an httptest server and checks the text, the tool
// call, the finish reason, the bearer header and the path.
func TestExtraProvidersRoundTrip(t *testing.T) {
	cases := []struct {
		id, env, model string
	}{
		{"bedrock", "AWS_BEARER_TOKEN_BEDROCK", "openai.gpt-oss-120b-1:0"},
		{"github-models", "GITHUB_TOKEN", "openai/gpt-4.1"},
		{"vercel_gateway", "AI_GATEWAY_API_KEY", "openai/gpt-5"},
		{"opencode-zen", "OPENCODE_ZEN_API_KEY", "big-pickle"},
		{"proxy", "", "claude-sonnet-5-5"},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			rec := &extraRecord{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				var m map[string]any
				_ = json.Unmarshal(b, &m)
				rec.mu.Lock()
				rec.path, rec.auth, rec.body = r.URL.Path, r.Header.Get("Authorization"), m
				rec.mu.Unlock()
				extraTextAndToolStream(w, r)
			}))
			t.Cleanup(srv.Close)

			var r *Registry
			if c.id == "proxy" {
				// The proxy takes its base from the environment.
				t.Setenv("SCRIBE_PROXY_URL", srv.URL)
				r = extraRegistry(t, c.id, "")
			} else {
				r = extraRegistry(t, c.id, srv.URL)
				t.Setenv(c.env, "tok-test")
			}

			p, model, err := r.Resolve(c.id + "/" + c.model)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if model != c.model {
				t.Fatalf("model = %q, want %q", model, c.model)
			}
			ch, err := p.Stream(context.Background(), llm.Request{
				Model:    model,
				Messages: []llm.Message{llm.UserText("read main.go")},
				Tools: []llm.ToolSpec{{
					Name:        "read_file",
					Description: "read a file",
					Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
				}},
			})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}

			var text strings.Builder
			var toolStart, toolEnd llm.Event
			var finish llm.Event
			timeout := time.After(5 * time.Second)
		loop:
			for {
				select {
				case ev, ok := <-ch:
					if !ok {
						break loop
					}
					switch ev.Type {
					case llm.EvText:
						text.WriteString(ev.Text)
					case llm.EvToolCallStart:
						toolStart = ev
					case llm.EvToolCallEnd:
						toolEnd = ev
					case llm.EvFinish:
						finish = ev
					case llm.EvError:
						t.Fatalf("stream error: %v", ev.Err)
					}
				case <-timeout:
					t.Fatal("stream did not close")
				}
			}

			if text.String() != "Looking. Done." {
				t.Fatalf("text = %q", text.String())
			}
			if toolStart.ToolName != "read_file" || toolStart.ToolCallID != "call_1" {
				t.Fatalf("tool start = %+v", toolStart)
			}
			if string(toolEnd.Args) != `{"path":"main.go"}` {
				t.Fatalf("tool args = %s", toolEnd.Args)
			}
			if finish.FinishReason != llm.FinishToolCalls || finish.Usage.InputTokens != 7 {
				t.Fatalf("finish = %+v", finish)
			}

			path, auth, body := rec.snapshot()
			// The proxy base is a bare host from SCRIBE_PROXY_URL, so /v1 is
			// appended to it; the other bases already end in their API root.
			wantPath := "/chat/completions"
			if c.id == "proxy" {
				wantPath = "/v1/chat/completions"
			}
			if path != wantPath {
				t.Fatalf("path = %q, want %q", path, wantPath)
			}
			if c.env != "" && auth != "Bearer tok-test" {
				t.Fatalf("Authorization = %q", auth)
			}
			if body["model"] != c.model {
				t.Fatalf("request model = %v, want %q", body["model"], c.model)
			}
		})
	}
}
