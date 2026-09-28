//go:build unix

package helpers

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestMCPInstallParity(t *testing.T) {
	script := pythonConst(t, "agents/mcp.go", "interactiveMCPInstallScript")
	payload := base64.StdEncoding.EncodeToString([]byte(`{"mcpServers":{"x":{}}}`))
	cases := []struct {
		name  string
		rel   string
		data  string
		env   func(root string) []string
		setup func(root string)
	}{
		{name: "fresh", rel: "lectern/mcp/s1/mcp.json", data: payload},
		{name: "home fallback", rel: "lectern/mcp/s1/mcp.json", data: payload,
			env: func(root string) []string { return []string{"XDG_STATE_HOME=", "HOME=" + root + "/home"} }},
		{name: "existing runtime", rel: "lectern/mcp/s1/mcp.json", data: payload,
			setup: func(root string) { os.MkdirAll(filepath.Join(root, "state/lectern/mcp/s1"), 0o700) }},
		{name: "symlinked state component", rel: "lectern/mcp/s1/mcp.json", data: payload,
			setup: func(root string) {
				os.MkdirAll(filepath.Join(root, "elsewhere"), 0o700)
				os.MkdirAll(filepath.Join(root, "state"), 0o700)
				os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(root, "state/lectern"))
			}},
		{name: "bad path", rel: "lectern/mcp/../mcp.json", data: payload},
		{name: "wrong shape", rel: "lectern/other/s1/mcp.json", data: payload},
		{name: "bad base64", rel: "lectern/mcp/s2/mcp.json", data: "not base64!"},
		{name: "relative state", rel: "lectern/mcp/s1/mcp.json", data: payload,
			env: func(string) []string { return []string{"XDG_STATE_HOME=relative"} }},
		{name: "no home", rel: "lectern/mcp/s1/mcp.json", data: payload,
			env: func(string) []string { return []string{"XDG_STATE_HOME=", "HOME="} }},
		{name: "trailing slash", rel: "lectern/mcp/s1/mcp.json", data: payload,
			env: func(root string) []string { return []string{"XDG_STATE_HOME=" + root + "/state/"} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, got := twin(t, script, "mcp-install", func(t *testing.T, root string) (runSpec, []string) {
				env := []string{"XDG_STATE_HOME=" + root + "/state"}
				if c.env != nil {
					env = c.env(root)
				}
				if c.setup != nil {
					c.setup(root)
				}
				return runSpec{dir: root, env: env}, []string{"--", c.rel, c.data}
			})
			if c.name == "fresh" && got.stdout != "$ROOT/state/lectern/mcp/s1/mcp.json\n" {
				t.Fatalf("install printed %q", got.stdout)
			}
		})
	}
}
