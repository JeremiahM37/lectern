package fs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// reasonHost denies every approval and gives a reason, as a phone reply with
// a note does.
type reasonHost struct{ reason string }

func (h reasonHost) Approve(ctx context.Context, a tools.Approval) tools.Decision {
	tools.SetApprovalReason(ctx, h.reason)
	return tools.Deny
}
func (reasonHost) Ask(context.Context, tools.Question) (string, error) { return "", nil }
func (reasonHost) Progress(string, string)                             {}

// Each file-tool denial must carry the user's reason to the model, in the
// same wording the other tool families use.
func TestFileDenialsCarryTheUsersReason(t *testing.T) {
	env, root, _, _ := newEnv(t)
	env.Host = reasonHost{reason: "not in CI"}
	write(t, root, ".env", "TOKEN=abc\n")

	cases := map[string]tools.Result{
		"sensitive write": call(t, NewWriteFile(nil), env, map[string]any{"path": ".github/workflows/ci.yml", "content": "x"}),
		"secret read":     call(t, NewRead(), env, map[string]any{"path": ".env"}),
		"outside write":   call(t, NewWriteFile(nil), env, map[string]any{"path": filepath.Join(t.TempDir(), "out.txt"), "content": "x"}),
	}
	for name, res := range cases {
		if !res.IsError {
			t.Errorf("%s: a denied call succeeded: %s", name, res.Output)
			continue
		}
		if !strings.HasPrefix(res.Output, "Denied: the user chose Deny when asked to ") {
			t.Errorf("%s: wording is not the shared denial: %s", name, res.Output)
		}
		if !strings.HasSuffix(res.Output, " Reason: not in CI") {
			t.Errorf("%s: the reason is missing: %s", name, res.Output)
		}
	}
}

func TestFileDenialWithoutReasonSaysNothingAboutOne(t *testing.T) {
	env, _, _, _ := newEnv(t)
	res := call(t, NewWriteFile(nil), env, map[string]any{"path": ".github/workflows/ci.yml", "content": "x"})
	if !res.IsError || strings.Contains(res.Output, "Reason:") {
		t.Fatalf("no reason was given, yet the result is %q", res.Output)
	}
}

func TestFileDenialWithNoHostIsARefusalNotAUserChoice(t *testing.T) {
	env, _, _, _ := newEnv(t)
	env.Host = nil
	res := call(t, NewWriteFile(nil), env, map[string]any{"path": ".github/workflows/ci.yml", "content": "x"})
	if !res.IsError || strings.Contains(res.Output, "chose Deny") {
		t.Fatalf("with no one to ask, the result claims a choice: %q", res.Output)
	}
	if !strings.Contains(res.Output, "No one is available to approve it") {
		t.Fatalf("no-host refusal lost its wording: %q", res.Output)
	}
}
