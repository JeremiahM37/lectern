package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// reasonHost denies every approval with a reason.
type reasonHost struct{ reason string }

func (h reasonHost) Approve(ctx context.Context, a tools.Approval) tools.Decision {
	tools.SetApprovalReason(ctx, h.reason)
	return tools.Deny
}
func (reasonHost) Ask(context.Context, tools.Question) (string, error) { return "", nil }
func (reasonHost) Progress(string, string)                             {}

func TestMCPDenialCarriesTheUsersReason(t *testing.T) {
	tool := &remoteTool{m: &Manager{}, server: "ok", remote: "echo", name: "mcp__ok__echo"}
	env := &tools.Env{Workdir: t.TempDir(), Mode: "default", Host: reasonHost{reason: "wrong account"}}
	res := tool.Run(context.Background(), env, json.RawMessage(`{"text":"x"}`))
	if !res.IsError {
		t.Fatalf("a denied call ran: %+v", res)
	}
	for _, want := range []string{"Denied: the user chose Deny when asked to use the ok tool echo.", "covers this call only"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("denial lacks %q: %s", want, res.Output)
		}
	}
	if !strings.HasSuffix(res.Output, " Reason: wrong account") {
		t.Fatalf("the denial lost the reason: %s", res.Output)
	}
}
