package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// countRead stands in for the read tool and counts the reads that reach it.
type countRead struct{ runs *int }

func (countRead) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "read", Description: "read", Schema: tools.Schema(map[string]any{"path": tools.Str("p")}, "path")}
}
func (countRead) ReadOnly() bool { return true }
func (c countRead) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	*c.runs++
	return tools.Result{Output: "contents"}
}

// marks records which files the agent read, the way the edit guard does.
type marks struct{ read []string }

func (m *marks) BeforeWrite(string)         {}
func (m *marks) MarkRead(abs string)        { m.read = append(m.read, abs) }
func (m *marks) StaleSinceRead(string) bool { return false }
func (m *marks) WasRead(string) bool        { return false }

// The second read of an unchanged file in one dispatch is served from the
// bus, and it still marks the file read. A changed file is read again.
func TestSubagentReadCacheServesUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runs := 0
	reg := tools.NewRegistry()
	reg.Register(countRead{runs: &runs})
	cached := withReadCache(reg, agent.NewAgentBus())
	tool, _ := cached.Get("read")
	m := &marks{}
	env := &tools.Env{Workdir: dir, Files: m}
	args := json.RawMessage(`{"path":"a.go"}`)

	first := tool.Run(context.Background(), env, args)
	second := tool.Run(context.Background(), env, args)
	if runs != 1 || first.Output != second.Output {
		t.Fatalf("second read ran the tool (%d runs) or changed output", runs)
	}
	// The stub read tool does not mark files itself, so the only mark is the
	// one the cache hit must make.
	if len(m.read) != 1 {
		t.Fatalf("file marked read %d times, want 1 from the cache hit", len(m.read))
	}

	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte("package a\n\nvar X = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool.Run(context.Background(), env, args)
	if runs != 2 {
		t.Fatalf("a changed file was served from the cache (runs %d)", runs)
	}
}

// Without a bus there is no cache: every read reaches the tool.
func TestSubagentReadWithoutBusIsNotCached(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runs := 0
	reg := tools.NewRegistry()
	reg.Register(countRead{runs: &runs})
	tool, _ := reg.Get("read")
	env := &tools.Env{Workdir: dir}
	for i := 0; i < 2; i++ {
		tool.Run(context.Background(), env, json.RawMessage(`{"path":"a.go"}`))
	}
	if runs != 2 {
		t.Fatalf("runs = %d, want 2", runs)
	}
}
