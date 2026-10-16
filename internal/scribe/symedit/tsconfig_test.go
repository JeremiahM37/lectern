package symedit

import (
	"strings"
	"testing"
)

const aliasTSConfig = `{
  // Path aliases for the app.
  "compilerOptions": {
    "baseUrl": ".",
    "paths": {
      "@/*": ["src/*"],
    },
  },
}
`

func TestTSMoveSymbolFollowsAliasImports(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("tsconfig.json", aliasTSConfig)
	f.write("src/util.ts", "export function helper(n: number): number {\n  return n * 2;\n}\n\nexport function keep(): number {\n  return 1;\n}\n")
	f.write("src/app.ts", "import { helper, keep } from \"@/util\";\n\nexport const x = helper(keep());\n")
	f.write("src/other.ts", "import { helper } from \"./util\";\n\nexport const y = helper(2);\n")
	mustOK(t, f.run("move_symbol", map[string]any{"symbol": "helper", "from": "src/util.ts", "to": "src/lib/helpers.ts"}))

	app := f.read("src/app.ts")
	if !strings.Contains(app, `import { helper } from "@/lib/helpers";`) {
		t.Fatalf("alias importer should keep its alias spelling:\n%s", app)
	}
	if !strings.Contains(app, `import { keep } from "@/util";`) {
		t.Fatalf("unmoved name should stay on the alias:\n%s", app)
	}
	other := f.read("src/other.ts")
	if !strings.Contains(other, `from "./lib/helpers"`) {
		t.Fatalf("relative importer should be rewritten relatively:\n%s", other)
	}
}

func TestTSRenameFileFollowsAliasImports(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("tsconfig.json", aliasTSConfig)
	f.write("src/util.ts", "export const v = 1;\n")
	f.write("src/app.ts", "import { v } from \"@/util\";\n\nexport const w = v;\n")
	mustOK(t, f.run("rename_file", map[string]any{"from": "src/util.ts", "to": "src/tools.ts"}))

	app := f.read("src/app.ts")
	if !strings.Contains(app, `from "@/tools"`) {
		t.Fatalf("alias import not rewritten to the new file:\n%s", app)
	}
}

func TestTSAliasMatchRequiresExistingFile(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("tsconfig.json", aliasTSConfig)
	f.write("src/real.ts", "export const a = 1;\n")
	al := loadTSAliases(f.dir)
	if al == nil {
		t.Fatal("tsconfig with paths should load")
	}
	if got := al.match("@/real"); got != "src/real" {
		t.Fatalf("match(@/real) = %q, want src/real", got)
	}
	if got := al.match("@/missing"); got != "" {
		t.Fatalf("match(@/missing) = %q, want no match", got)
	}
	if got := al.match("react"); got != "" {
		t.Fatalf("bare package matched an alias: %q", got)
	}
}
