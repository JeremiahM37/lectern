package symedit

import (
	"strings"
	"testing"
)

const tsModuleSrc = `import React from "react";
import { useState, useEffect } from "react";
import { helper } from "./utils";
import * as path from "node:path";

export { helper as h } from "./utils";

export const x = helper(path.sep);
`

func TestModuleAddNamedImportExtendsExisting(t *testing.T) {
	f := newFixture(t)
	p := f.write("m.ts", tsModuleSrc)
	got := editOK(t, f, p, map[string]any{"action": "add_named_import", "symbol": "./utils", "new_code": "other, helper"})
	if !strings.Contains(got, `import { helper, other } from "./utils";`) {
		t.Fatalf("names not added to the existing import:\n%s", got)
	}
	if strings.Count(got, `from "./utils"`) != 2 {
		t.Fatalf("an existing import from ./utils should be extended, not repeated:\n%s", got)
	}
}

func TestModuleAddNamedImportNewLineWhenNoneExists(t *testing.T) {
	f := newFixture(t)
	p := f.write("m.ts", tsModuleSrc)
	got := editOK(t, f, p, map[string]any{"action": "add_named_import", "symbol": "lodash", "new_code": "debounce"})
	if !strings.Contains(got, "import { debounce } from 'lodash';") {
		t.Fatalf("new import line missing:\n%s", got)
	}
}

func TestModuleAddNamedImportKeepsDefaultBinding(t *testing.T) {
	f := newFixture(t)
	p := f.write("m.ts", tsModuleSrc)
	got := editOK(t, f, p, map[string]any{"action": "add_named_import", "symbol": "react", "new_code": "useMemo"})
	if !strings.Contains(got, `import { useState, useEffect, useMemo } from "react";`) || !strings.Contains(got, `import React from "react";`) {
		t.Fatalf("names should join the braced import and leave the default import alone:\n%s", got)
	}
}

func TestModuleRemoveNamedImportDropsEmptyLine(t *testing.T) {
	f := newFixture(t)
	p := f.write("m.ts", tsModuleSrc)
	got := editOK(t, f, p, map[string]any{"action": "remove_named_import", "symbol": "./utils", "new_code": "helper"})
	if strings.Contains(got, `import { helper } from "./utils";`) || strings.Contains(got, "import { helper }") {
		t.Fatalf("the only name was not removed, or the line was not dropped:\n%s", got)
	}
	if !strings.Contains(got, `export { helper as h } from "./utils";`) {
		t.Fatalf("re-export must be untouched:\n%s", got)
	}
}

func TestModuleRemoveNamedImportKeepsOthers(t *testing.T) {
	f := newFixture(t)
	p := f.write("m.ts", tsModuleSrc)
	got := editOK(t, f, p, map[string]any{"action": "remove_named_import", "symbol": "react", "new_code": "useState"})
	if !strings.Contains(got, `import { useEffect } from "react";`) || strings.Contains(got, "useState") {
		t.Fatalf("remaining names wrong:\n%s", got)
	}
}

func TestModuleRemoveImportAndSetSpecifier(t *testing.T) {
	f := newFixture(t)
	p := f.write("m.ts", tsModuleSrc)
	got := editOK(t, f, p, map[string]any{"action": "set_module_specifier", "symbol": "./utils", "new_code": "./helpers"})
	if !strings.Contains(got, `import { helper } from "./helpers";`) || !strings.Contains(got, `export { helper as h } from "./helpers";`) {
		t.Fatalf("specifier not changed in the import and the re-export:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "remove_import", "symbol": "./helpers"})
	if strings.Contains(got, "./helpers") && strings.Contains(got, "import { helper }") {
		t.Fatalf("import from ./helpers not removed:\n%s", got)
	}
}

func TestModuleOpsNeedAModulePathAndAMatch(t *testing.T) {
	f := newFixture(t)
	p := f.write("m.ts", tsModuleSrc)
	args := map[string]any{"path": p, "action": "remove_import", "symbol": "./nothing-here"}
	if res := f.run(args); res.IsError == false {
		t.Fatal("remove_import of an unimported module must say so")
	}
	if res := f.run(map[string]any{"path": p, "action": "add_named_import", "new_code": "x"}); res.IsError == false {
		t.Fatal("add_named_import without a module path must fail")
	}
}

func TestModuleOpsRejectOtherLanguages(t *testing.T) {
	f := newFixture(t)
	p := f.write("m.py", "import os\n")
	if res := f.run(map[string]any{"path": p, "action": "remove_import", "symbol": "os"}); res.IsError == false {
		t.Fatal("module actions are TypeScript and JavaScript only")
	}
}
