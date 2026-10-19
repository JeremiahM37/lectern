package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/memory"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func w40App(t *testing.T) (*App, string) {
	t.Helper()
	t.Setenv("SCRIBE_HOME", t.TempDir())
	root := t.TempDir()
	m, err := memory.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return &App{Memory: m, Workdir: root, services: map[string]any{}}, root
}

func TestPublishMemoryServiceReachesFileTools(t *testing.T) {
	a, _ := w40App(t)
	if _, _, err := a.Memory.Project.Add(memory.Record{Category: memory.CatBug, Title: "Watch the lock", Body: "x", Files: []string{"a.go"}}); err != nil {
		t.Fatal(err)
	}
	a.publishMemoryService()
	env := &tools.Env{Services: a.services}
	m, ok := tools.Service[interface{ FileHint(string) string }](env, "memory")
	if !ok {
		t.Fatal("Services[memory] not published")
	}
	if got := m.FileHint("a.go"); !strings.Contains(got, "Watch the lock") {
		t.Fatalf("FileHint through the service = %q", got)
	}
}

func TestPublishMemoryServiceSkipsNilStore(t *testing.T) {
	a := &App{services: map[string]any{}}
	a.publishMemoryService()
	if _, ok := a.services["memory"]; ok {
		t.Fatal("published a nil store; a nil *Stores would reach the tools")
	}
}

func TestSaveCompactionMemoryWritesInBackground(t *testing.T) {
	a, root := w40App(t)
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.saveCompactionMemory("Summary of work on b.go")
	deadline := time.Now().Add(5 * time.Second)
	for {
		recs, _ := a.Memory.Project.List(memory.ListOptions{Limit: 10})
		if len(recs) == 1 {
			if !strings.HasPrefix(recs[0].Title, "Session diary:") || len(recs[0].Files) != 1 {
				t.Fatalf("diary record = %+v", recs[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no diary entry written; have %d", len(recs))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
