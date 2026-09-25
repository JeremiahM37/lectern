package isolation

import "testing"

func TestNormalized(t *testing.T) {
	cases := []struct {
		name string
		in   Config
		want Config
	}{
		{"none clears everything", Config{Network: NetworkDeny, DockerImage: "x", AllowHosts: []string{"a"}}, Config{}},
		{"bwrap defaults network to allow", Config{Mode: Bwrap}, Config{Mode: Bwrap, Network: NetworkAllow}},
		{"bwrap drops a docker image", Config{Mode: Bwrap, DockerImage: "x"}, Config{Mode: Bwrap, Network: NetworkAllow}},
		{"docker defaults its image", Config{Mode: Docker}, Config{Mode: Docker, Network: NetworkAllow, DockerImage: DefaultDockerImage}},
		{"docker keeps an explicit image", Config{Mode: Docker, DockerImage: "custom:tag"}, Config{Mode: Docker, Network: NetworkAllow, DockerImage: "custom:tag"}},
		{"deny is preserved", Config{Mode: Bwrap, Network: NetworkDeny, AllowHosts: []string{"x.com"}}, Config{Mode: Bwrap, Network: NetworkDeny, AllowHosts: []string{"x.com"}}},
		{"allow-hosts dropped without deny", Config{Mode: Bwrap, AllowHosts: []string{"x.com"}}, Config{Mode: Bwrap, Network: NetworkAllow}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.Normalized()
			if got.Mode != c.want.Mode || got.Network != c.want.Network || got.DockerImage != c.want.DockerImage || len(got.AllowHosts) != len(c.want.AllowHosts) {
				t.Fatalf("Normalized() = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	if err := (Config{Mode: "nonsense"}).Validate(); err == nil {
		t.Fatal("expected an error for an unknown mode")
	}
	if err := (Config{Mode: Bwrap, Network: "sideways"}).Validate(); err == nil {
		t.Fatal("expected an error for an unknown network policy")
	}
	if err := (Config{}).Validate(); err != nil {
		t.Fatalf("none should always validate: %v", err)
	}
	if err := (Config{Mode: Bwrap, Network: NetworkDeny}).Validate(); err != nil {
		t.Fatalf("bwrap+deny should validate on its own: %v", err)
	}
}

func TestValidateForTarget(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		target  string
		wantErr bool
	}{
		{"none anywhere", Config{}, "pct", false},
		{"none on sandbox is fine — nothing to redunda", Config{}, "sandbox", false},
		{"bwrap allow on local", Config{Mode: Bwrap}, "local", false},
		{"bwrap allow on ssh", Config{Mode: Bwrap}, "ssh", false},
		{"bwrap allow on pct rejected", Config{Mode: Bwrap}, "pct", true},
		{"bwrap deny on local", Config{Mode: Bwrap, Network: NetworkDeny}, "local", false},
		{"bwrap deny on ssh rejected", Config{Mode: Bwrap, Network: NetworkDeny}, "ssh", true},
		{"docker allow on pct", Config{Mode: Docker}, "pct", false},
		{"docker deny on ssh rejected", Config{Mode: Docker, Network: NetworkDeny}, "ssh", true},
		{"docker deny on local", Config{Mode: Docker, Network: NetworkDeny}, "local", false},
		{"any isolation on sandbox rejected", Config{Mode: Bwrap}, "sandbox", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateForTarget(c.cfg, c.target)
			if (err != nil) != c.wantErr {
				t.Fatalf("ValidateForTarget(%+v, %q) err=%v, wantErr=%v", c.cfg, c.target, err, c.wantErr)
			}
		})
	}
}

func TestDefaultAllowHosts(t *testing.T) {
	hosts := DefaultAllowHosts("claude", "http://127.0.0.1:9110/api/hook/session/5")
	has := func(want string) bool {
		for _, h := range hosts {
			if h == want {
				return true
			}
		}
		return false
	}
	if !has("api.anthropic.com") {
		t.Errorf("expected claude's model API host, got %v", hosts)
	}
	if !has("127.0.0.1") {
		t.Errorf("expected the hook host to be allowlisted, got %v", hosts)
	}
	if !has("registry.npmjs.org") {
		t.Errorf("expected a package-registry default, got %v", hosts)
	}
	codex := DefaultAllowHosts("codex", "")
	for _, h := range codex {
		if h == "api.anthropic.com" {
			t.Errorf("codex should not inherit claude's model API host: %v", codex)
		}
	}
}
