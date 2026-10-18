package web

import (
	"context"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// reasonHost denies every approval and gives a reason, the way a phone
// reply with a note does.
type reasonHost struct{ reason string }

func (h reasonHost) Approve(ctx context.Context, a tools.Approval) tools.Decision {
	tools.SetApprovalReason(ctx, h.reason)
	return tools.Deny
}
func (reasonHost) Ask(context.Context, tools.Question) (string, error) { return "", nil }
func (reasonHost) Progress(string, string)                             {}

func TestUserDeclinedSaysDecisionFinalAndThisCallOnly(t *testing.T) {
	msg := UserDeclined("fetch a web page: https://example.com/", "")
	for _, want := range []string{
		"Denied: the user chose Deny when asked to fetch a web page: https://example.com/.",
		"the user's decision",
		"covers this call only",
		"Then continue",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "Reason:") {
		t.Errorf("no reason was given, but the message has one:\n%s", msg)
	}
}

func TestUserDeclinedEndsWithTheReason(t *testing.T) {
	msg := UserDeclined("use the ok tool echo", "not from that site")
	if !strings.HasSuffix(msg, " Reason: not from that site") {
		t.Fatalf("reason is not the tail:\n%s", msg)
	}
}

func TestWebDenialCarriesTheUsersReason(t *testing.T) {
	w := newWebTools(Options{AllowPrivate: true})
	env := &tools.Env{Mode: "default", Host: reasonHost{reason: "search later, not now"}}
	res := runTool(t, &searchTool{w: w}, env, map[string]any{"query": "q"})
	if !res.IsError {
		t.Fatalf("a denied search succeeded: %+v", res)
	}
	if !strings.HasSuffix(res.Output, " Reason: search later, not now") {
		t.Fatalf("the denial lost the reason: %s", res.Output)
	}
}
