package pluginpkg

import (
	"strings"
	"testing"
)

func TestScaffoldModValidates(t *testing.T) {
	out, err := ScaffoldMod("acme.hello")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := Load(out, false)
	if err != nil {
		t.Fatal(err)
	}
	m := pkg.Manifest
	if strings.Join(m.Capabilities.Mods, ",") != "web,cli" || len(m.Contributes.Mods) != 1 || m.Contributes.Mods[0].Path != "mods/hello.js" {
		t.Fatalf("manifest = %+v", m)
	}
	for _, f := range out {
		if f.Path == "mods/hello.js" {
			script, err := ModScript(string(f.Data))
			if err != nil || !strings.Contains(script, "function register(") {
				t.Fatalf("scaffolded mod does not convert: %v", err)
			}
		}
	}
	if _, err := ScaffoldMod("lectern.mine"); err == nil {
		t.Fatal("scaffolded a reserved id")
	}
}
