package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/registry"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/exec"
)

func TestBraveKeyComesFromConfiguredVariable(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	t.Setenv("MY_BRAVE", "  brv-123 ")
	a := &App{Cfg: config.Config{Search: config.SearchSettings{BraveKeyEnv: "MY_BRAVE"}}}
	a.Registry = registry.New(a.Cfg)
	if got := a.braveKey(); got != "brv-123" {
		t.Fatalf("braveKey = %q, want the trimmed value of MY_BRAVE", got)
	}
	a.Cfg.Search.BraveKeyEnv = ""
	t.Setenv("BRAVE_SEARCH_API_KEY", "default-env")
	if got := a.braveKey(); got != "default-env" {
		t.Fatalf("default variable not used: %q", got)
	}
}

func TestToolTimeoutFromConfig(t *testing.T) {
	zero := 0.0
	three := 3.0
	if d := (&App{Cfg: config.Config{ToolTimeout: &zero}}).toolTimeout(); d != exec.NoTimeout {
		t.Fatalf("tool_timeout 0 should mean no limit, got %s", d)
	}
	if d := (&App{Cfg: config.Config{ToolTimeout: &three}}).toolTimeout(); d != 3*time.Minute {
		t.Fatalf("tool_timeout 3 = %s", d)
	}
	if d := (&App{}).toolTimeout(); d != 2*time.Minute {
		t.Fatalf("unset tool_timeout = %s, want the 2 minute default", d)
	}
}

func TestRenderMapUsesConversationBudget(t *testing.T) {
	a := &App{}
	if got := a.RenderMap(0, nil); got != "" {
		t.Fatalf("no code map should render empty, got %q", got)
	}
}

// capReadLines is the explore subagent's read: a missing or larger limit is
// lowered to 750, a smaller one is kept.
type fakeRead struct{ got json.RawMessage }

func (f *fakeRead) Spec() llm.ToolSpec { return llm.ToolSpec{Name: "read"} }
func (f *fakeRead) ReadOnly() bool     { return true }
func (f *fakeRead) Run(_ context.Context, _ *tools.Env, raw json.RawMessage) tools.Result {
	f.got = raw
	return tools.Result{}
}

func TestExploreReadIsCappedAt750Lines(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{`{"path":"a.go"}`, 750},
		{`{"path":"a.go","limit":2000}`, 750},
		{`{"path":"a.go","limit":"5000"}`, 750},
		{`{"path":"a.go","limit":100}`, 100},
	}
	for _, c := range cases {
		inner := &fakeRead{}
		capped := capReadLines{Tool: inner, max: exploreReadLines}
		capped.Run(context.Background(), nil, json.RawMessage(c.in))
		var got struct {
			Limit int `json:"limit"`
			Path  string
		}
		if err := json.Unmarshal(inner.got, &got); err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got.Limit != c.want || got.Path != "a.go" {
			t.Fatalf("%s -> %s, want limit %d", c.in, inner.got, c.want)
		}
	}
}

func TestEditedFilesAreTrackedForRecall(t *testing.T) {
	a := &App{}
	a.noteEdited("internal/x.go")
	a.noteEdited("a.go")
	got := a.editedFilesSnapshot()
	if strings.Join(got, ",") != "a.go,internal/x.go" {
		t.Fatalf("snapshot = %v", got)
	}
}

func TestDispatchFeaturesFollowConfigDefaults(t *testing.T) {
	f := (&App{}).dispatchFeatures()
	if f.Desloppify || f.VerifyEdits {
		t.Fatal("desloppify and verify_edits are off by default")
	}
	if !f.TierRouting || !f.DispatchCache || !f.TargetFileValidation {
		t.Fatalf("tier_routing, dispatch_cache and target_file_validation default on: %+v", f)
	}
	off := false
	a := &App{Cfg: config.Config{AgentFeatures: &config.AgentFeatures{Desloppify: &off}}}
	on := true
	a.Cfg.AgentFeatures.VerifyEdits = &on
	f = a.dispatchFeatures()
	if f.Desloppify || !f.VerifyEdits {
		t.Fatalf("config overrides not applied: %+v", f)
	}
}
