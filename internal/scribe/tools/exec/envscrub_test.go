package exec

import (
	"sort"
	"strings"
	"testing"
)

func TestScrubEnvDropsCredentialsKeepsToolchain(t *testing.T) {
	in := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/dev",
		"LANG=en_US.UTF-8",
		"LC_ALL=C",
		"XDG_CONFIG_HOME=/home/dev/.config",
		"GOPATH=/home/dev/go",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		"GIT_AUTHOR_NAME=Dev",
		"FOO=bar",
		"ANTHROPIC_API_KEY=sk-ant-1",
		"OPENAI_API_KEY=sk-2",
		"GITHUB_TOKEN=ghp_3",
		"GH_TOKEN=gho_4",
		"NPM_TOKEN=npm_5",
		"MY_SERVICE_SECRET=s",
		"DB_PASSWORD=p",
		"AWS_ACCESS_KEY_ID=AKIA",
		"AWS_SESSION_TOKEN=t",
		"PRIVATE_KEY=-----BEGIN",
		"CUSTOM_CREDENTIAL=c",
		"STRIPE_KEY=sk_live",
		"NOEQUALSSIGN",
		"=weird",
	}
	got := ScrubEnv(in)

	kept := map[string]bool{}
	for _, kv := range got {
		name, _, _ := strings.Cut(kv, "=")
		kept[name] = true
	}
	for _, name := range []string{"PATH", "HOME", "LANG", "LC_ALL", "XDG_CONFIG_HOME", "GOPATH", "SSH_AUTH_SOCK", "GIT_AUTHOR_NAME", "FOO"} {
		if !kept[name] {
			t.Errorf("%s should pass scrubbing", name)
		}
	}
	for _, name := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GITHUB_TOKEN", "GH_TOKEN", "NPM_TOKEN", "MY_SERVICE_SECRET",
		"DB_PASSWORD", "AWS_ACCESS_KEY_ID", "AWS_SESSION_TOKEN", "PRIVATE_KEY", "CUSTOM_CREDENTIAL", "STRIPE_KEY",
	} {
		if kept[name] {
			t.Errorf("%s should be dropped", name)
		}
	}
	if kept["NOEQUALSSIGN"] || kept[""] {
		t.Error("entries without a name or '=' are not environment entries and must be dropped")
	}
	if !contains(got, "GIT_TERMINAL_PROMPT=0") {
		t.Error("GIT_TERMINAL_PROMPT=0 should be forced")
	}
}

func TestScrubEnvNameMatchingIsCaseInsensitive(t *testing.T) {
	got := ScrubEnv([]string{"anthropic_api_key=x", "Github_Token=y", "path=/bin"})
	names := []string{}
	for _, kv := range got {
		n, _, _ := strings.Cut(kv, "=")
		names = append(names, n)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "GIT_TERMINAL_PROMPT,path" {
		t.Fatalf("kept %v", names)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
