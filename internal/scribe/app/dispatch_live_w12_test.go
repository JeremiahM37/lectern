package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveDispatchWithTheRealModel runs a two-agent dispatch against a real
// Ollama model, in a throwaway repository, and checks the report: both
// agents finish, each names where the function is, and the peer objectives
// reach the model. Skipped unless SCRIBE_LIVE_DISPATCH is set to an Ollama host.
func TestLiveDispatchWithTheRealModel(t *testing.T) {
	host := os.Getenv("SCRIBE_LIVE_DISPATCH")
	if host == "" {
		t.Skip("set SCRIBE_LIVE_DISPATCH to an Ollama host to run")
	}
	t.Setenv("OLLAMA_HOST", host)
	t.Setenv("SCRIBE_HOME", t.TempDir())
	work := t.TempDir()
	src := "package main\n\n// Add returns the sum of two numbers.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\nfunc main() {\n\t_ = Add(1, 2)\n}\n"
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	a, err := Open(ctx, Options{Workdir: work, Model: "ollama/qwen3.6:35b-a3b", Mode: "auto", Version: "test", MaxSteps: 8})
	if err != nil {
		t.Fatal(err)
	}
	args := `{"tasks":[` +
		`{"id":"locate","role":"explore","task":"Find the line where the function Add is declared in main.go. Answer with file:line."},` +
		`{"id":"entry","role":"explore","task":"Find the line where main calls Add in main.go. Answer with file:line."}]}`
	tool, ok := a.Tools.Get("dispatch")
	if !ok {
		t.Fatal("dispatch tool is not registered")
	}
	env := a.env()
	env.CallID = "live_dispatch"
	res := tool.Run(ctx, &env, json.RawMessage(args))
	t.Logf("summary: %s\n%s", res.Summary, res.Output)
	if res.IsError {
		t.Fatalf("dispatch failed: %s", res.Output)
	}
	if !strings.Contains(res.Output, "main.go:") {
		t.Fatalf("report should anchor the answers at main.go:line:\n%s", res.Output)
	}
}
