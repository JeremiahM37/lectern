package isolation

import (
	"strings"
	"testing"
)

func argvString(t *testing.T, argv []string) string {
	t.Helper()
	s := ""
	for i, a := range argv {
		if i > 0 {
			s += " "
		}
		s += a
	}
	return s
}

func TestBuildBwrapArgvRejectsWrongMode(t *testing.T) {
	if _, err := BuildBwrapArgv(Config{Mode: Docker}, WrapOpts{Workdir: "/tmp/x"}); err == nil {
		t.Fatal("expected an error building bwrap argv for a docker config")
	}
}

func TestBuildBwrapArgvRequiresWorkdir(t *testing.T) {
	if _, err := BuildBwrapArgv(Config{Mode: Bwrap}, WrapOpts{}); err == nil {
		t.Fatal("expected an error with no workdir")
	}
}

func TestBuildBwrapArgvAllowNetwork(t *testing.T) {
	argv, err := BuildBwrapArgv(Config{Mode: Bwrap}, WrapOpts{Agent: "claude", Workdir: "/srv/proj"})
	if err != nil {
		t.Fatal(err)
	}
	s := argvString(t, argv)
	for _, want := range []string{"bwrap", "--die-with-parent", "--ro-bind /usr /usr", "--proc /proc", "--bind /srv/proj /srv/proj", "--chdir /srv/proj"} {
		if !strings.Contains(s, want) {
			t.Errorf("argv missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "--unshare-net") {
		t.Errorf("network=allow must not unshare the network namespace:\n%s", s)
	}
	if !strings.Contains(s, `"$HOME/.claude"`) || !strings.Contains(s, `"$HOME/.claude.json"`) {
		t.Errorf("claude's own config paths must be bound:\n%s", s)
	}
}

// A workdir containing shell-meaningful characters must still be one word.
func TestBuildBwrapArgvQuotesAnUnsafeWorkdir(t *testing.T) {
	argv, err := BuildBwrapArgv(Config{Mode: Bwrap}, WrapOpts{Agent: "claude", Workdir: "/srv/my proj"})
	if err != nil {
		t.Fatal(err)
	}
	s := argvString(t, argv)
	if !strings.Contains(s, "--bind '/srv/my proj' '/srv/my proj'") || !strings.Contains(s, "--chdir '/srv/my proj'") {
		t.Errorf("expected the workdir quoted as one shell word:\n%s", s)
	}
}

func TestBuildBwrapArgvPerAgentHomePaths(t *testing.T) {
	cases := map[string][]string{
		"claude": {`"$HOME/.claude"`, `"$HOME/.claude.json"`},
		"codex":  {`"$HOME/.codex"`},
		"gemini": {`"$HOME/.gemini"`, `"$HOME/.config/gemini"`},
	}
	for agent, want := range cases {
		argv, err := BuildBwrapArgv(Config{Mode: Bwrap}, WrapOpts{Agent: agent, Workdir: "/w"})
		if err != nil {
			t.Fatal(err)
		}
		s := argvString(t, argv)
		for _, w := range want {
			if !strings.Contains(s, w) {
				t.Errorf("agent %q: argv missing %q:\n%s", agent, w, s)
			}
		}
	}
	// An unregistered custom agent gets no extra config bind, but still builds.
	argv, err := BuildBwrapArgv(Config{Mode: Bwrap}, WrapOpts{Agent: "my-custom-cli", Workdir: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(argvString(t, argv), "$HOME/.claude") {
		t.Errorf("a custom agent must not inherit claude's binds")
	}
}

func TestBuildBwrapArgvDenyNetworkRequiresSocket(t *testing.T) {
	if _, err := BuildBwrapArgv(Config{Mode: Bwrap, Network: NetworkDeny}, WrapOpts{Workdir: "/w"}); err == nil {
		t.Fatal("expected an error: network=deny with no proxy socket")
	}
}

func TestBuildBwrapArgvDenyNetwork(t *testing.T) {
	argv, err := BuildBwrapArgv(Config{Mode: Bwrap, Network: NetworkDeny},
		WrapOpts{Agent: "claude", Workdir: "/w", ProxySocket: "/tmp/lectern-isolation/session-9.sock"})
	if err != nil {
		t.Fatal(err)
	}
	s := argvString(t, argv)
	if !strings.Contains(s, "--unshare-net") {
		t.Errorf("network=deny must unshare the network namespace:\n%s", s)
	}
	if !strings.Contains(s, "--ro-bind /tmp/lectern-isolation/session-9.sock "+bwrapProxySocketPath) {
		t.Errorf("the proxy socket must be bind-mounted at the fixed sandbox path:\n%s", s)
	}
}

func TestWrapBwrapEmbedsBootstrapOnlyWhenDenied(t *testing.T) {
	allow, err := Wrap("claude -p hi", Config{Mode: Bwrap}, WrapOpts{Agent: "claude", Workdir: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(allow, "socat") {
		t.Errorf("network=allow must not start the socat bridge:\n%s", allow)
	}
	if !strings.Contains(allow, "bash -c") || !strings.Contains(allow, "claude -p hi") {
		t.Errorf("expected the agent invocation embedded verbatim:\n%s", allow)
	}

	deny, err := Wrap("claude -p hi", Config{Mode: Bwrap, Network: NetworkDeny},
		WrapOpts{Agent: "claude", Workdir: "/w", ProxySocket: "/tmp/s.sock"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"socat TCP-LISTEN:" + proxyBridgeTCPPort + ",bind=127.0.0.1,reuseaddr,fork UNIX-CONNECT:" + bwrapProxySocketPath, "HTTP_PROXY=http://127.0.0.1:" + proxyBridgeTCPPort, "claude -p hi"} {
		if !strings.Contains(deny, want) {
			t.Errorf("network=deny wrap missing %q:\n%s", want, deny)
		}
	}
}

func TestWrapNoneIsIdentity(t *testing.T) {
	out, err := Wrap("claude -p hi", Config{}, WrapOpts{Agent: "claude", Workdir: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "claude -p hi" {
		t.Errorf("mode=none must return the invocation unchanged, got %q", out)
	}
}
