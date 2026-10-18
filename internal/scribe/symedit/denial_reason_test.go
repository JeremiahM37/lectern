package symedit

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// reasonHost denies every approval and gives a reason.
type reasonHost struct{ reason string }

func (h reasonHost) Approve(ctx context.Context, a tools.Approval) tools.Decision {
	tools.SetApprovalReason(ctx, h.reason)
	return tools.Deny
}
func (reasonHost) Ask(context.Context, tools.Question) (string, error) { return "", nil }
func (reasonHost) Progress(string, string)                             {}

// A symbol edit that the user refuses must tell the model why, in the shared
// denial wording, for both the sensitive-file and the outside-project gates.
func TestSymbolEditDenialCarriesTheUsersReason(t *testing.T) {
	dir := t.TempDir()
	env := &tools.Env{Workdir: dir, Mode: "default", Host: reasonHost{reason: "leave CI alone"}}
	cases := map[string]string{
		"sensitive": ".github/workflows/ci.yml",
		"outside":   filepath.Join(t.TempDir(), "elsewhere.go"),
	}
	for name, p := range cases {
		_, _, err := gate(context.Background(), env, p)
		if err == nil {
			t.Errorf("%s: a refused edit passed the gate", name)
			continue
		}
		msg := err.Error()
		if !strings.HasPrefix(msg, "Denied: the user chose Deny when asked to ") {
			t.Errorf("%s: wording is not the shared denial: %s", name, msg)
		}
		if !strings.HasSuffix(msg, " Reason: leave CI alone") {
			t.Errorf("%s: the reason is missing: %s", name, msg)
		}
	}
}

// With no one to ask, the gate refuses without claiming the user chose.
func TestSymbolEditWithNoHostIsARefusal(t *testing.T) {
	dir := t.TempDir()
	env := &tools.Env{Workdir: dir, Mode: "default"}
	_, _, err := gate(context.Background(), env, filepath.Join(t.TempDir(), "elsewhere.go"))
	if err == nil || strings.Contains(err.Error(), "chose Deny") || !strings.Contains(err.Error(), "No one is available") {
		t.Fatalf("no-host refusal is wrong: %v", err)
	}
}
