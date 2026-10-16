package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// TestToolUseIDInStdin checks that the id the agent puts in the context
// reaches the hook's stdin on the tool events, and that it is empty when the
// caller did not set one.
func TestToolUseIDInStdin(t *testing.T) {
	skipWindows(t)
	r, root := newTestRunner(t, []config.Hook{{
		Event: "PreToolUse", Matcher: "shell",
		Command: `cat > pre.json; exit 0`,
	}, {
		Event: "PostToolUse", Matcher: "shell",
		Command: `cat > post.json; exit 0`,
	}})
	ctx := WithToolUseID(context.Background(), "toolu_123")
	r.PreTool(ctx, "shell", bashArgs("go test ./..."))
	r.PostTool(ctx, "shell", bashArgs("go test ./..."), tools.Result{Output: "ok"})
	for _, name := range []string{"pre.json", "post.json"} {
		var in map[string]any
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &in); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if in["tool_use_id"] != "toolu_123" {
			t.Fatalf("%s tool_use_id = %v", name, in["tool_use_id"])
		}
	}
	// Without an id the field is present and empty.
	r.PreTool(context.Background(), "shell", bashArgs("ls"))
	b, _ := os.ReadFile(filepath.Join(root, "pre.json"))
	var in map[string]any
	_ = json.Unmarshal(b, &in)
	if v, ok := in["tool_use_id"]; !ok || v != "" {
		t.Fatalf("no-id call: tool_use_id = %v (present %v)", v, ok)
	}
}
