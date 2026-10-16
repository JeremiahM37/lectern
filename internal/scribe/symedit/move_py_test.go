package symedit

import (
	"strings"
	"testing"
)

const pyUtil = `import os
from pkg.other import twice

LIMIT = 3


def helper(n):
    """Doubles n."""
    return twice(n) + LIMIT + len(os.sep)


def local(n):
    return helper(n) + 1
`

func TestPyMoveSymbolRewritesFromImports(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("pkg/__init__.py", "")
	f.write("pkg/other.py", "def twice(n):\n    return 2 * n\n")
	f.write("pkg/util.py", pyUtil)
	f.write("app.py", "from pkg.util import helper, local\n\nprint(helper(1), local(2))\n")
	mustOK(t, f.run("move_symbol", map[string]any{"symbol": "helper", "from": "pkg/util.py", "to": "pkg/lib.py"}))

	app := f.read("app.py")
	if !strings.Contains(app, "from pkg.lib import helper") {
		t.Fatalf("importer not rewritten to the destination module:\n%s", app)
	}
	if !strings.Contains(app, "from pkg.util import local") {
		t.Fatalf("unrelated name should stay on the old import:\n%s", app)
	}
	lib := f.read("pkg/lib.py")
	if !strings.Contains(lib, "def helper(n):") || !strings.Contains(lib, "from pkg.other import twice") {
		t.Fatalf("destination must hold the definition and the import it uses:\n%s", lib)
	}
	util := f.read("pkg/util.py")
	if strings.Contains(util, "def helper(") {
		t.Fatalf("definition still in source:\n%s", util)
	}
	if !strings.Contains(util, "from pkg.lib import helper") {
		t.Fatalf("source still uses helper, so it must import it back:\n%s", util)
	}
}

func TestPyMoveSymbolKeepsRelativeImports(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("pkg/__init__.py", "")
	f.write("pkg/util.py", "def helper(n):\n    return n * 2\n\n\ndef keep():\n    return 1\n")
	f.write("pkg/sub/__init__.py", "")
	f.write("pkg/sub/user.py", "from ..util import helper, keep\n\nVALUE = helper(keep())\n")
	mustOK(t, f.run("move_symbol", map[string]any{"symbol": "helper", "from": "pkg/util.py", "to": "pkg/moved.py"}))

	user := f.read("pkg/sub/user.py")
	if !strings.Contains(user, "from ..moved import helper") {
		t.Fatalf("relative importer should keep its relative form:\n%s", user)
	}
	if !strings.Contains(user, "from ..util import keep") {
		t.Fatalf("unmoved name should stay:\n%s", user)
	}
}

func TestPyMoveSymbolRefusesExistingDefinitionAndMembers(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("a.py", "def f():\n    return 1\n")
	f.write("b.py", "def f():\n    return 2\n")
	res := f.run("move_symbol", map[string]any{"symbol": "f", "from": "a.py", "to": "b.py"})
	if !res.IsError || !strings.Contains(res.Output, "already defines f") {
		t.Fatalf("expected refusal for existing definition, got %q", res.Output)
	}
}

func TestPyMoveSymbolWarnsOnModuleStyleUse(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("pkg/__init__.py", "")
	f.write("pkg/util.py", "def helper():\n    return 1\n")
	f.write("pkg/lib.py", "")
	f.write("mod_user.py", "import pkg.util\n\nprint(pkg.util.helper())\n")
	res := f.run("move_symbol", map[string]any{"symbol": "helper", "from": "pkg/util.py", "to": "pkg/lib.py"})
	mustOK(t, res)
	if !strings.Contains(res.Output, "mod_user.py") {
		t.Fatalf("module-style use should be reported as a warning:\n%s", res.Output)
	}
}
