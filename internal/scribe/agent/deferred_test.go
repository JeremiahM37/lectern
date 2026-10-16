package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// namesOf lists the tool names in a recorded request, in order.
func namesOf(b wireBody) []string {
	var out []string
	for _, t := range b.Tools {
		out = append(out, t.Function.Name)
	}
	return out
}

// toolsJSON extracts the serialized "tools" array from a raw request body.
func toolsJSON(raw []byte) string {
	var body struct {
		Tools json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	return string(body.Tools)
}

// descTool is a deferred tool with a description and schema of a size close
// to a real MCP tool.
type descTool struct{ name, desc string }

func (t descTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: t.name, Description: t.desc, Schema: tools.Schema(map[string]any{"query": tools.Str("what to look for")}, "query")}
}
func (t descTool) ReadOnly() bool { return true }
func (t descTool) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	return tools.Result{Output: t.name + " ran"}
}

func TestDeferredOffersCoreThenRequestedAndStaysStable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	reg.Register(descTool{name: "extra_one", desc: "First extra."})
	reg.Register(descTool{name: "extra_two", desc: "Second extra."})
	d := NewDeferred([]string{"read"})
	RegisterDeferred(reg, d)

	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "read", args: `{"path":"a.txt"}`}}}),
		turn(sseTurn{calls: []sseCall{{id: "c2", name: "extra_two", args: `{}`}}}),
		turn(sseTurn{calls: []sseCall{{id: "c3", name: "request_tools", args: `{"tools":["extra_one","nope_tool"]}`}}}),
		turn(sseTurn{text: []string{"done"}}),
	))
	opts := baseOptions(f, reg, &eventLog{}, dir)
	opts.Deferred = d
	a := New(opts)
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}

	reqs := f.requests()
	if len(reqs) != 4 {
		t.Fatalf("requests = %d, want 4", len(reqs))
	}
	if got := strings.Join(namesOf(reqs[0].body), ","); got != "read,request_tools,release_tools" {
		t.Fatalf("step 1 offered %s", got)
	}
	// Step 1 and step 2 have the same set, so the tools block is byte-identical.
	if ta, tb := toolsJSON(reqs[0].raw), toolsJSON(reqs[1].raw); ta == "" || ta != tb {
		t.Fatalf("tool schemas changed between steps with no change in the set")
	}
	if tm := toolMsg(t, reqs[2], "c2"); !strings.Contains(tm.text(), "deferred and not loaded") {
		t.Fatalf("deferred call result = %q", tm.text())
	}
	if got := strings.Join(namesOf(reqs[2].body), ","); got != "read,request_tools,release_tools" {
		t.Fatalf("step 3 offered %s", got)
	}
	if got := strings.Join(namesOf(reqs[3].body), ","); got != "read,extra_one,request_tools,release_tools" {
		t.Fatalf("step 4 offered %s, want extra_one in registration order", got)
	}
	if tm := toolMsg(t, reqs[3], "c3"); !strings.Contains(tm.text(), "Activated: extra_one") || !strings.Contains(tm.text(), "Unknown: nope_tool") {
		t.Fatalf("request_tools result = %q", tm.text())
	}
}

func TestRequestAndReleaseToolsResults(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	reg.Register(descTool{name: "extra_one", desc: "First extra."})
	reg.Register(descTool{name: "extra_two", desc: "Second extra."})
	d := NewDeferred([]string{"read"})
	RegisterDeferred(reg, d)
	req, _ := reg.Get("request_tools")
	rel, _ := reg.Get("release_tools")
	env := &tools.Env{}
	ctx := context.Background()

	r := req.Run(ctx, env, json.RawMessage(`{"tools":["extra_one","extra_one","read","extra_tow"]}`))
	if r.IsError {
		t.Fatalf("request result error: %s", r.Output)
	}
	for _, want := range []string{"Activated: extra_one", "Already active: extra_one, read", "Unknown: extra_tow (did you mean extra_two?)"} {
		if !strings.Contains(r.Output, want) {
			t.Fatalf("request output %q missing %q", r.Output, want)
		}
	}
	if got := d.Active(); len(got) != 1 || got[0] != "extra_one" {
		t.Fatalf("active = %v", got)
	}
	if r = req.Run(ctx, env, json.RawMessage(`{"tools":["extra_one"]}`)); !strings.Contains(r.Output, "Already active: extra_one") {
		t.Fatalf("second request = %q", r.Output)
	}
	r = rel.Run(ctx, env, json.RawMessage(`{"tools":["read","extra_one","extra_two"]}`))
	for _, want := range []string{"Released: extra_one", "Not active: extra_two", "Core tools cannot be released: read (core)"} {
		if !strings.Contains(r.Output, want) {
			t.Fatalf("release output %q missing %q", r.Output, want)
		}
	}
	if len(d.Active()) != 0 {
		t.Fatalf("active after release = %v", d.Active())
	}
}

func TestDeferredTokenEstimateDrops(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	long := strings.Repeat("Performs a detailed operation on the project and returns structured results. ", 6)
	for i := 0; i < 40; i++ {
		reg.Register(descTool{name: "mcp_tool_" + string(rune('a'+i%26)) + string(rune('a'+i/26)), desc: long})
	}
	d := NewDeferred([]string{"read"})
	RegisterDeferred(reg, d)
	a := New(Options{Tools: reg, Deferred: d})

	before := estimateTokens(reg.Specs(nil))
	after := estimateTokens(reg.Specs(a.offeredNames()))
	t.Logf("tool schema tokens (est.): all=%d deferred=%d (%.0f%% of all)", before, after, 100*float64(after)/float64(before))
	if after*2 >= before {
		t.Fatalf("deferred schemas should be well under half: before=%d after=%d", before, after)
	}
}

// estimateTokens is the usual four-bytes-per-token rule over the JSON that
// is actually sent.
func estimateTokens(specs []llm.ToolSpec) int {
	b, _ := json.Marshal(specs)
	return (len(b) + 3) / 4
}

func TestDeferredCatalogAndPrompt(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	reg.Register(descTool{name: "web_search", desc: "Search the web for pages. Uses SearXNG when configured. Longer detail follows."})
	d := NewDeferred([]string{"read"})
	RegisterDeferred(reg, d)
	cat := DeferredCatalog(reg, d.IsCore)
	if cat != "- web_search — Search the web for pages." {
		t.Fatalf("catalog = %q", cat)
	}
	sys := BuildSystem(PromptInput{Workdir: "/w", Deferred: cat}, "default")
	if !strings.Contains(sys, "# Deferred tools") || !strings.Contains(sys, "- web_search — Search the web for pages.") {
		t.Fatalf("system prompt missing catalog")
	}
	if strings.Contains(BuildSystem(PromptInput{Workdir: "/w"}, "default"), "# Deferred tools") {
		t.Fatalf("empty catalog must not add a section")
	}
}

func TestRetryAfterZeroRetriesImmediately(t *testing.T) {
	if d := backoff(3, &llm.APIError{StatusCode: 429, RetryAfterSet: true}); d != 0 {
		t.Fatalf("Retry-After: 0 gave %v, want 0", d)
	}
	if d := backoff(0, &llm.APIError{StatusCode: 429, RetryAfter: 7 * time.Second, RetryAfterSet: true}); d != 7*time.Second {
		t.Fatalf("Retry-After: 7 gave %v", d)
	}
	if d := backoff(0, &llm.APIError{StatusCode: 529}); d <= 0 {
		t.Fatalf("no Retry-After must back off, got %v", d)
	}
}
