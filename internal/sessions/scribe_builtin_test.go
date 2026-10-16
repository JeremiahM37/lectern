package sessions

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestScribeIsABuiltinLaunchedAsLecternCode(t *testing.T) {
	var scribe Spec
	found := false
	for _, s := range Builtins() {
		if s.Name == ScribeAgent {
			scribe, found = s, true
		}
	}
	if !found {
		t.Fatal("Builtins() has no scribe agent")
	}
	if !scribe.Builtin || scribe.DisplayName != "Scribe (Lectern)" {
		t.Fatalf("scribe = %+v, want a builtin labelled Scribe (Lectern)", scribe)
	}
	if !scribe.Installed() {
		t.Fatal("scribe must always report installed: it is this binary")
	}

	exe := ScribeCommand()
	if scribe.Command != exe || !filepath.IsAbs(exe) {
		t.Fatalf("Command = %q, want the absolute path of the running binary %q", scribe.Command, exe)
	}
	if len(scribe.Args) != 1 || scribe.Args[0] != "code" {
		t.Fatalf("Args = %v, want [code]", scribe.Args)
	}

	inv := scribe.invocation(Start{Model: "anthropic/claude", Yolo: true, Prompt: "fix it"})
	wantPrefix := exe + " code"
	if !strings.HasPrefix(inv, wantPrefix) {
		t.Fatalf("invocation %q does not start with %q", inv, wantPrefix)
	}
	for _, part := range []string{"--mode auto", "--model anthropic/claude", "'fix it'"} {
		if !strings.Contains(inv, part) {
			t.Errorf("invocation %q lacks %q", inv, part)
		}
	}

	resume := scribe.invocation(Start{Resume: true})
	if !strings.Contains(resume, " code --continue") {
		t.Errorf("resume invocation %q should continue the newest Scribe session", resume)
	}

	caps := scribe.Capabilities()
	if caps[CapTask].Available {
		t.Error("scribe has no non-interactive task driver; CapTask must be unavailable")
	}
	if !caps[CapModel].Available || !caps[CapYolo].Available {
		t.Errorf("scribe should offer model and yolo: %+v", caps)
	}
}

func TestScribeAppearsInParsedRegistry(t *testing.T) {
	for _, s := range ParseSpecs("") {
		if s.Name == ScribeAgent {
			return
		}
	}
	t.Fatal("ParseSpecs does not include scribe")
}
