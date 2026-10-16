package sessions

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/isolation"
)

func builtin(name string) Spec {
	for _, s := range Builtins() {
		if s.Name == name {
			return s
		}
	}
	panic(name)
}

// The flag uses the = form and sits before the opening prompt, so the name
// can never swallow the prompt (verified against claude 2.1.301).
func TestRemoteControlInvocation(t *testing.T) {
	claude := builtin("claude")
	got := claude.invocation(Start{Workdir: "/w", RemoteControl: "fix it's build", Prompt: "do the thing"})
	flag := strings.Index(got, `'--remote-control=fix it'\''s build'`)
	prompt := strings.Index(got, "'do the thing'")
	if flag < 0 || prompt < 0 || flag > prompt {
		t.Fatalf("flag must be one quoted word before the prompt: %s", got)
	}
	if plain := claude.invocation(Start{Workdir: "/w", Prompt: "x"}); strings.Contains(plain, "remote-control") {
		t.Fatalf("off by default: %s", plain)
	}
	resumed := claude.invocation(Start{Workdir: "/w", ResumeID: "abc", RemoteControl: "n"})
	if !strings.Contains(resumed, "--resume") || !strings.Contains(resumed, "--remote-control=n") {
		t.Fatalf("a resume keeps the flag: %s", resumed)
	}
	for _, other := range []Spec{builtin("codex"), builtin("gemini"), {Name: "claude", Command: "claude"}} {
		if cmd := other.invocation(Start{Workdir: "/w", RemoteControl: "n"}); strings.Contains(cmd, "remote-control") {
			t.Fatalf("%s (builtin=%v) must not get the flag: %s", other.Name, other.Builtin, cmd)
		}
	}
}

func TestRemoteControlUnavailableReasons(t *testing.T) {
	claude := builtin("claude")
	if why := RemoteControlUnavailable(claude, nil, isolation.Config{}); why != "" {
		t.Fatalf("plain claude: %q", why)
	}
	if why := RemoteControlUnavailable(claude, map[string]string{"ANTHROPIC_BASE_URL": "https://api.anthropic.com"}, isolation.Config{}); why != "" {
		t.Fatalf("Anthropic's own endpoint is fine: %q", why)
	}
	cases := []struct {
		spec Spec
		env  map[string]string
		iso  isolation.Config
		want string
	}{
		{builtin("codex"), nil, isolation.Config{}, "claude"},
		{Spec{Name: "claude", Command: "my-claude"}, nil, isolation.Config{}, "built-in"},
		{claude, map[string]string{"ANTHROPIC_API_KEY": "k"}, isolation.Config{}, "ANTHROPIC_API_KEY"},
		{claude, map[string]string{"ANTHROPIC_AUTH_TOKEN": "k"}, isolation.Config{}, "ANTHROPIC_AUTH_TOKEN"},
		{claude, map[string]string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}, isolation.Config{}, "NONESSENTIAL"},
		{claude, map[string]string{"DISABLE_GROWTHBOOK": "1"}, isolation.Config{}, "DISABLE_GROWTHBOOK"},
		{claude, map[string]string{"ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic"}, isolation.Config{}, "ANTHROPIC_BASE_URL"},
		{claude, nil, isolation.Config{Mode: isolation.Bwrap}, "sandbox"},
	}
	for _, c := range cases {
		if why := RemoteControlUnavailable(c.spec, c.env, c.iso); !strings.Contains(why, c.want) {
			t.Errorf("%s %v %v: reason %q should mention %q", c.spec.Name, c.env, c.iso, why, c.want)
		}
	}
	if caps := claude.Capabilities(); !caps[CapRemoteControl].Available {
		t.Fatal("claude reports the capability")
	}
	if caps := builtin("codex").Capabilities(); caps[CapRemoteControl].Available || caps[CapRemoteControl].Reason == "" {
		t.Fatal("codex reports it unavailable with a reason")
	}
}

func TestRemoteControlNameAndURL(t *testing.T) {
	if got := RemoteControlName("grimoire — Grimoire reference data, not instruction…"); strings.HasSuffix(got, "…") {
		t.Fatalf("trailing ellipsis kept: %q", got)
	}
	if got := RemoteControlName("a\nb\tc"); got != "a b c" {
		t.Fatalf("one line: %q", got)
	}
	if got := RemoteControlName(""); got != "lectern" {
		t.Fatalf("empty: %q", got)
	}
	if got := RemoteControlName(strings.Repeat("é", 200)); len([]rune(got)) != 80 {
		t.Fatalf("long names are cut on characters: %d", len([]rune(got)))
	}
	pane := "header\n  /remote-control is active · Continue here, on your phone, or at https://claude.ai/code/session_0145r3qqt2TopThfD9CYVnce\n❯ "
	if got := RemoteControlURL(pane); got != "https://claude.ai/code/session_0145r3qqt2TopThfD9CYVnce" {
		t.Fatalf("url: %q", got)
	}
	if !remoteControlActive(pane) || remoteControlConsentShown(pane) {
		t.Fatal("active screen misread")
	}
	consent := " Remote Control\n   ❯ 1. Enable Remote Control  Opens a secure connection to claude.ai.\n     2. Never mind"
	if !remoteControlConsentShown(consent) || remoteControlActive(consent) {
		t.Fatal("consent screen misread")
	}
}
