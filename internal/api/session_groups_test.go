package api_test

import (
	"fmt"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"testing"
)

func TestSessionGroupsNormalizeClearAndPreserveProcess(t *testing.T) {
	h := newHarness(t)
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "groups", Kind: "local"})
	row, _ := h.App.DB.InsertSession(&store.Session{Name: "Keep attached", TargetID: target.ID, Workdir: "/repo", TmuxSession: "keep"})
	base := fmt.Sprintf("/api/sessions/%d", row.ID)
	var result obj
	h.decode("PATCH", base, obj{"group_path": " Work / Client "}, 200, &result)
	if result["group_path"] != "Work/Client" || result["tmux_session"] != "keep" || result["workdir"] != "/repo" {
		t.Fatal(result)
	}
	h.decode("PATCH", base, obj{"group_path": "Work//Client"}, 422, nil)
	h.decode("GET", base, nil, 200, &result)
	if result["group_path"] != "Work/Client" {
		t.Fatal("failed edit changed group")
	}
	h.decode("PATCH", base, obj{"group_path": ""}, 200, &result)
	if result["group_path"] != "" {
		t.Fatal("group did not clear")
	}
}
