package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

// With nothing chosen and Ollama the only provider, setup is needed when the
// default model is not installed on the server (the first message would fail
// with "model not found"), and not needed when it is.
func TestFirstRunNeedsSetupWhenDefaultOllamaModelIsMissing(t *testing.T) {
	for _, tc := range []struct {
		tags string
		want bool
	}{
		{`{"models":[{"name":"qwen3.6:35b-a3b"}]}`, true},
		{`{"models":[{"name":"qwen2.5-coder:7b"}]}`, false},
	} {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(tc.tags))
		}))
		t.Setenv("SCRIBE_HOME", t.TempDir())
		for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "OPENROUTER_API_KEY", "DEEPSEEK_API_KEY", "GROQ_API_KEY", "XAI_API_KEY", "MISTRAL_API_KEY"} {
			t.Setenv(k, "")
		}
		t.Setenv("OLLAMA_HOST", strings.TrimPrefix(up.URL, "http://"))
		repo := t.TempDir()
		if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		host := NewTUIHost()
		a, err := Open(context.Background(), Options{Workdir: repo, Host: host, Version: "test"})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		be := NewBackend(a, host)
		if !strings.HasPrefix(be.Model(), "ollama/") {
			be.Close()
			up.Close()
			t.Skipf("default model is %q here, not an Ollama one", be.Model())
		}
		if got := needsSetup(be); got != tc.want {
			t.Errorf("installed %s: needs setup = %v, want %v", tc.tags, got, tc.want)
		}
		be.Close()
		up.Close()
	}
}
