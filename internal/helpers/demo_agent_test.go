package helpers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestDemoApprovalBeforeFirstWrite(t *testing.T) {
	for _, decision := range []string{"allow", "deny", "unavailable"} {
		t.Run(decision, func(t *testing.T) {
			t.Chdir(t.TempDir())
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/PermissionRequest" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{}`))
					return
				}
				calls++
				if _, err := os.Stat("demo-notes.md"); !os.IsNotExist(err) {
					t.Error("demo wrote before approval")
				}
				if r.URL.Path != "/PermissionRequest" || r.Header.Get("Authorization") != "Bearer test-session-token" {
					t.Error("wrong approval request")
				}
				if decision == "unavailable" {
					w.WriteHeader(503)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"hookSpecificOutput":{"decision":{"behavior":"` + decision + `"}}}`))
			}))
			defer server.Close()
			t.Setenv("LECTERN_HOOK_URL", server.URL)
			t.Setenv("LECTERN_HOOK_TOKEN", "test-session-token")
			t.Setenv("LECTERN_DEMO_ASK", "1")
			var out bytes.Buffer
			if code := demoAgent(nil, strings.NewReader("first\nsecond\n"), &out, &out); code != 0 {
				t.Fatalf("exit %d: %s", code, out.String())
			}
			data, err := os.ReadFile("demo-notes.md")
			if decision == "allow" {
				if err != nil || string(data) != "- first\n- second\n" || calls != 1 {
					t.Fatalf("writes=%q err=%v approval calls=%d", data, err, calls)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("wrote without approval: %q, %v", data, err)
			}
		})
	}
}
