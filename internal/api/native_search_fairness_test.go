package api_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/sessions"
	"github.com/JeremiahM37/lectern/internal/store"
)

func TestNativeSearchGivesEveryProfileAPassBeforeRepeating(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	root := t.TempDir()
	// Deterministic worker replies model four large profiles and one warm one.
	// The real executor and HTTP scheduler run unchanged; no history size or
	// machine speed determines whether the small profile gets a turn.
	script := `#!/bin/sh
countfile="$PROOF/$PROFILE.count"
n=0
test ! -f "$countfile" || read -r n < "$countfile"
n=$((n+1))
printf '%s\n' "$n" > "$countfile"
printf '%s %s %s\n' "$PROFILE" "$n" "${3:-none}" >> "$PROOF/events"
complete=false
if test "$PROFILE" = 0 || test "$n" -ge 3; then complete=true; fi
printf '{"profile_key":"profile-%s","progress":{"complete":%s},"matches":[]}\n' "$PROFILE" "$complete"
`
	if err := os.WriteFile(filepath.Join(root, "python3"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	env := func(profile int) map[string]string {
		return map[string]string{"PATH": root + ":/usr/bin:/bin", "PROOF": root, "PROFILE": fmt.Sprint(profile)}
	}
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "fair search", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	h.decode("PUT", "/api/agents", []obj{{"name": "codex", "command": "codex", "env": env(0)}}, 200, nil)
	for i := 1; i <= 4; i++ {
		snapshot, _ := json.Marshal(sessions.LaunchConfiguration{Version: 1, Spec: sessions.Spec{Name: "codex", Command: "codex", Env: env(i)}})
		row, err := h.App.DB.InsertSession(&store.Session{TargetID: target.ID, Name: fmt.Sprintf("large-%d", i), Agent: "codex", Workdir: root, TmuxSession: fmt.Sprintf("not-running-%d", i), LaunchConfigJSON: string(snapshot)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.App.DB.Exec("UPDATE sessions SET launch_config_json=? WHERE id=?", string(snapshot), row.ID); err != nil {
			t.Fatal(err)
		}
	}
	var start obj
	h.decode("POST", "/api/conversation-search", obj{"query": "needle", "target_id": target.ID, "agent": "codex", "reset": true}, 202, &start)
	result := waitNativeSearch(t, h, start["id"].(string))
	if result["complete"] != true {
		t.Fatal(result)
	}
	raw, err := os.ReadFile(filepath.Join(root, "events"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("invalid event %q", line)
		}
		if fields[1] != "1" && len(seen) != 5 {
			t.Fatalf("profile repeated before every profile was searched: %s", raw)
		}
		if (fields[1] == "1") != (fields[2] == "--reset") {
			t.Fatalf("reset must run exactly once per profile: %s", raw)
		}
		seen[fields[0]] = true
	}
	if len(seen) != 5 {
		t.Fatalf("missing profiles: %s", raw)
	}
}
