package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/internal/config"
)

func TestMemoryOutageIsVisibleAndDoesNotBlockLaunch(t *testing.T) {
	memoryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
	defer memoryServer.Close()
	h := newHarness(t, func(c *config.Config) { c.GrimoireURL = memoryServer.URL })
	pid := h.seededProjectID()
	brief := h.get(fmt.Sprintf("/api/projects/%d/brief", pid))
	if !strings.Contains(brief.str("brief"), "Memory unavailable") {
		t.Fatalf("outage hidden: %+v", brief)
	}
	m, ok := brief["memory"].(map[string]any)
	if !ok || m["status"] != "unavailable" {
		t.Fatalf("missing machine-readable status: %+v", brief)
	}
	sess := h.session(obj{"project_id": pid, "brief": true, "name": "memory-outage"})
	h.waitSessionStatus(sess.id(), "idle", "waiting", "running")
	found := false
	for _, cmd := range h.mock().CmdLog() {
		if strings.HasPrefix(cmd, "tmux new-session") && strings.Contains(cmd, "Memory unavailable") {
			found = true
		}
	}
	if !found {
		t.Fatal("launched agent was not told that its memory lookup failed")
	}
}
