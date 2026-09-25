package isolation

import (
	"strings"
	"testing"
)

func TestBuildDockerArgvRejectsWrongMode(t *testing.T) {
	if _, err := BuildDockerArgv(Config{Mode: Bwrap}, WrapOpts{Workdir: "/w"}); err == nil {
		t.Fatal("expected an error building docker argv for a bwrap config")
	}
}

func TestBuildDockerArgvDefaultImage(t *testing.T) {
	argv, err := BuildDockerArgv(Config{Mode: Docker}, WrapOpts{Agent: "codex", Workdir: "/srv/proj"})
	if err != nil {
		t.Fatal(err)
	}
	s := argvString(t, argv)
	for _, want := range []string{"docker run --rm -i -t", "-w /srv/proj", "-v /srv/proj:/srv/proj", `"$HOME/.codex":"$HOME/.codex"`, DefaultDockerImage} {
		if !strings.Contains(s, want) {
			t.Errorf("argv missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "--network") {
		t.Errorf("network=allow must not pass --network at all:\n%s", s)
	}
}

func TestBuildDockerArgvQuotesAnUnsafeWorkdir(t *testing.T) {
	argv, err := BuildDockerArgv(Config{Mode: Docker}, WrapOpts{Workdir: "/srv/my proj"})
	if err != nil {
		t.Fatal(err)
	}
	s := argvString(t, argv)
	if !strings.Contains(s, "-w '/srv/my proj'") || !strings.Contains(s, "-v '/srv/my proj':'/srv/my proj'") {
		t.Errorf("expected the workdir quoted as one shell word:\n%s", s)
	}
}

func TestBuildDockerArgvCustomImage(t *testing.T) {
	argv, err := BuildDockerArgv(Config{Mode: Docker, DockerImage: "myorg/agent-runner:latest"}, WrapOpts{Workdir: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	s := argvString(t, argv)
	if !strings.Contains(s, "myorg/agent-runner:latest") {
		t.Errorf("expected the configured image, got:\n%s", s)
	}
	if strings.Contains(s, DefaultDockerImage) {
		t.Errorf("must not fall back to the default image when one is set:\n%s", s)
	}
}

func TestBuildDockerArgvDenyNetworkNone(t *testing.T) {
	argv, err := BuildDockerArgv(Config{Mode: Docker, Network: NetworkDeny}, WrapOpts{Workdir: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	s := argvString(t, argv)
	if !strings.Contains(s, "--network none") {
		t.Errorf("network=deny must pass --network none:\n%s", s)
	}
}

func TestWrapDocker(t *testing.T) {
	out, err := Wrap("codex exec hi", Config{Mode: Docker}, WrapOpts{Agent: "codex", Workdir: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "docker run") {
		t.Errorf("expected a docker run command, got %q", out)
	}
	if !strings.Contains(out, "bash -c 'codex exec hi'") {
		t.Errorf("expected the invocation passed to bash -c, got %q", out)
	}
}
