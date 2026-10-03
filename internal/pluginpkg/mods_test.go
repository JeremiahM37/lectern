package pluginpkg

import (
	"strings"
	"testing"
)

const modManifest = `
id: acme.blast
name: Blast radius
version: 0.1.0
capabilities: {mods: [web, cli], api: read}
contributes:
  mods:
    - {id: blast, path: mods/blast.js}
`

func modFile(src string) File { return File{Path: "mods/blast.js", Mode: 0o644, Data: []byte(src)} }

func TestAModLoadsAndItsCodeIsWhatAPersonConsentsTo(t *testing.T) {
	pkg, err := Load(files(modManifest, modFile("export function register(on) { on('app.start', ($, e, next) => next(e)) }")), false)
	if err != nil {
		t.Fatal(err)
	}
	keys := strings.Join(CapabilityKeys(pkg.Manifest.CapabilityList()), "\n")
	for _, want := range []string{"mods: blast (mods/blast.js) in the web and cli", "api: read"} {
		if !strings.Contains(keys, want) {
			t.Fatalf("consent list lacks %q:\n%s", want, keys)
		}
	}
}

func TestModScriptDeclaresRegisterFromEveryExportForm(t *testing.T) {
	for name, src := range map[string]string{
		"named":         "export function register(on, options) { }",
		"async named":   "export async function register(on) { }",
		"default":       "export default function (on) { }",
		"default arrow": "export default (on) => { on('x', ($, e, n) => n(e)) }",
		"plain":         "function register(on) { }",
		"other exports": "export const tone = 'ok';\nexport function helper() {}\nexport function register(on) { helper() }",
	} {
		t.Run(name, func(t *testing.T) {
			out, err := ModScript(src)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out, "export ") {
				t.Fatalf("an export is left:\n%s", out)
			}
		})
	}
}

func TestModValidationRefusesWhatItShould(t *testing.T) {
	head := "id: a\nname: A\nversion: 1\n"
	js := modFile("export function register(on) {}")
	cases := map[string]struct {
		manifest, want string
		file           File
	}{
		"no capability":     {head + "capabilities: {}\ncontributes: {mods: [{id: m, path: mods/blast.js}]}", "capabilities.mods", js},
		"surface not given": {head + "capabilities: {mods: [web]}\ncontributes: {mods: [{id: m, path: mods/blast.js}]}", "terminal console", js},
		"bad surface":       {head + "capabilities: {mods: [web]}\ncontributes: {mods: [{id: m, path: mods/blast.js, surfaces: [desktop]}]}", "web or cli", js},
		"not js":            {head + "capabilities: {mods: [web, cli]}\ncontributes: {mods: [{id: m, path: mods/blast.ts}]}", ".js or .mjs", js},
		"missing file":      {head + "capabilities: {mods: [web, cli]}\ncontributes: {mods: [{id: m, path: mods/other.js}]}", "not in the plugin", js},
		"imports":           {head + "capabilities: {mods: [web, cli]}\ncontributes: {mods: [{id: m, path: mods/blast.js}]}", "cannot import", modFile("import x from 'y'\nexport function register(on) {}")},
		"no register":       {head + "capabilities: {mods: [web, cli]}\ncontributes: {mods: [{id: m, path: mods/blast.js}]}", "must define register", modFile("export function setup(on) {}")},
		"syntax":            {head + "capabilities: {mods: [web, cli]}\ncontributes: {mods: [{id: m, path: mods/blast.js}]}", "does not parse", modFile("export function register(on) { on( }")},
		"too large":         {head + "capabilities: {mods: [web, cli]}\ncontributes: {mods: [{id: m, path: mods/blast.js}]}", "256 KB", modFile("function register(on) {}\n//" + strings.Repeat("x", MaxModBytes))},
		"bad api":           {head + "capabilities: {mods: [web, cli], api: admin}\ncontributes: {mods: [{id: m, path: mods/blast.js}]}", "read or write", js},
		"api without mods":  {head + "capabilities: {api: read}\ncontributes: {}", "only for mods", js},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(files(c.manifest, c.file), false)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error mentioning %q, got %v", c.want, err)
			}
		})
	}
}
