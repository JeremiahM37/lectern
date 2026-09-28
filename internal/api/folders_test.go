package api_test

import (
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The folder picker behind "Start an agent" runs its listing on the machine
// itself: real directories through the real local executor.
func TestFoldersListsSubfoldersOnTheMachine(t *testing.T) {
	h := newHarness(t, realLocal)
	tid := h.localTarget(t)
	root := projectTree(t) // with-git (a repo), just-go, research, screenshots
	if err := os.MkdirAll(filepath.Join(root, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := "/api/targets/" + strconv.FormatInt(tid, 10) + "/folders?path="
	out := h.get(base + url.QueryEscape(root))
	if out.str("path") != root || out.str("parent") != filepath.Dir(root) || out.str("home") == "" {
		t.Fatalf("listing header: %v", out)
	}
	byName := map[string]obj{}
	for _, f := range out.list("folders") {
		byName[f.str("name")] = f
	}
	if len(byName) != 4 || byName[".hidden"] != nil {
		t.Fatalf("expected the four visible folders, got %v", keysOf(byName))
	}
	if byName["with-git"]["git"] != true || byName["just-go"]["git"] != false {
		t.Fatalf("git flags: %v", byName)
	}
	if byName["research"].str("path") != filepath.Join(root, "research") {
		t.Fatalf("absolute paths: %v", byName["research"])
	}

	// A folder that is already a project says which one.
	proj := h.post("/api/projects/import", obj{"target_id": tid, "paths": []string{filepath.Join(root, "with-git")}}, 200)
	pid := proj.list("imported")[0].id()
	out = h.get(base + url.QueryEscape(root))
	for _, f := range out.list("folders") {
		if f.str("name") == "with-git" && int64(f.num("project_id")) != pid {
			t.Fatalf("registered folder not marked: %v", f)
		}
	}

	// Home is the default; "~/" paths are relative to it.
	if home := h.get(base); home.str("path") != home.str("home") {
		t.Fatalf("default is home: %v", home)
	}
	if code := h.status("GET", base+url.QueryEscape(filepath.Join(root, "nope")), nil); code != 404 {
		t.Fatalf("missing folder: %d", code)
	}
	if code := h.status("GET", base+"relative/path", nil); code != 400 {
		t.Fatalf("relative path: %d", code)
	}
}

func TestFoldersOnTheDemoMachine(t *testing.T) {
	h := newHarness(t)
	tid := h.getList("/api/targets")[0].id()
	out := h.get("/api/targets/" + strconv.FormatInt(tid, 10) + "/folders")
	if out.str("path") != "/mock/home" || len(out.list("folders")) != 3 {
		t.Fatalf("demo home: %v", out)
	}
	sub := h.get("/api/targets/" + strconv.FormatInt(tid, 10) + "/folders?path=" + url.QueryEscape("~/website"))
	if sub.str("path") != "/mock/home/website" || sub.str("parent") != "/mock/home" {
		t.Fatalf("demo subfolder: %v", sub)
	}
	if code := h.status("GET", "/api/targets/999/folders", nil); code != 404 {
		t.Fatalf("unknown machine: %d", code)
	}
}
