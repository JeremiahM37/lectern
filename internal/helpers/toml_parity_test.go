package helpers

import (
	"fmt"
	"math/big"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// tomlSummary renders a parsed document canonically, the same way
// tomlSummaryPy does for tomllib's result.
func tomlSummary(v any) string {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			ks, _ := pyJSONDumps(k, -1, true)
			parts[i] = ks + ":" + tomlSummary(x[k])
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = tomlSummary(item)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case string:
		s, _ := pyJSONDumps(x, -1, true)
		return s
	case bool:
		return pyRepr(x)
	case tomlInt:
		n, ok := new(big.Int).SetString(string(x), 0)
		if !ok {
			return "badint"
		}
		return n.String()
	case float64:
		return pyFloatRepr(x)
	case tomlDateTime:
		return "dt"
	}
	return fmt.Sprintf("?%T", v)
}

const tomlSummaryPy = `
import sys, tomllib, json, datetime
def s(v):
    if isinstance(v, dict):
        return "{" + ",".join(json.dumps(k) + ":" + s(v[k]) for k in sorted(v)) + "}"
    if isinstance(v, list):
        return "[" + ",".join(s(i) for i in v) + "]"
    if isinstance(v, str):
        return json.dumps(v)
    if isinstance(v, (datetime.date, datetime.time)):
        return "dt"
    return repr(v)
try:
    d = tomllib.loads(sys.stdin.buffer.read().decode())
except tomllib.TOMLDecodeError:
    print("invalid", end="")
    sys.exit()
print(s(d), end="")
`

// The TOML parser decides whether codex-trust appends to a config, so it must
// accept, reject and read exactly what tomllib does.
func TestTOMLMatchesTomllib(t *testing.T) {
	testutil.RequireIsolated(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	docs := []string{
		"",
		"# only a comment\n",
		"model = \"o3\"\n[projects.\"/home/a/x\"]\ntrust_level = \"trusted\"\n",
		"[projects.'/lit/path']\ntrust_level = 'trusted'\n[projects.\"/b\"]\n",
		"projects = { \"/a\" = { trust_level = \"trusted\" }, b = 1 }\n",
		"projects.\"/a\".trust_level = \"trusted\"\nprojects.\"/b\".x = 1\n",
		"[projects]\n\"/a\" = {trust_level=\"trusted\"}\n\"/b\".trust_level = \"t\"\n",
		"projects = [\"/a\", \"/b\", 1, [\"/c\"]]\n",
		"projects = \"/a:/b\"\n",
		"projects = 5\n",
		"projects = 1979-05-27T07:32:00Z\n",
		"[projects.\"/a\"]\nx=1\n[projects.\"/a\"]\ny=2\n",
		"[projects.\"/a\"]\n[projects]\nz = 1\n",
		"[projects]\nz = 1\n[projects]\n",
		"a.b = 1\n[a.b]\n",
		"a.b = 1\n[a]\nc = 2\n",
		"[a]\nb.c = 1\n[a.b]\n",
		"[[fruit]]\nname = \"apple\"\n[fruit.physical]\ncolor = \"red\"\n[[fruit.variety]]\nname = \"x\"\n[[fruit]]\nname = \"b\"\n",
		"a = [1]\n[[a]]\n",
		"a = {b = 1}\na.c = 2\n",
		"a = {b = {c = 1}, b.d = 2}\n",
		"a = {x.y = 1, x.z = 2}\n",
		"a = {x = 1, x = 2}\n",
		"a = { }\nb = {\n}\n",
		"a = {b = 1,}\n",
		"a = [\n  1, # one\n  2,\n]\n",
		"a = [1 2]\n",
		"s1 = \"\\u00e9 \\U0001F600 \\t \\\\ \\\"\"\ns2 = 'C:\\path'\n",
		"s = \"\\x41\"\n",
		"s = \"\\uD800\"\n",
		"s = \"tab\there\"\n",
		"s = \"bell\u0007\"\n",
		"s = \"\"\"\nline one\\\n    continued \\\n\n  end\"\"\"\"\"\n",
		"s = '''\nraw ''\\n'''''\n",
		"s = '''no end\n",
		"s = \"\"\"a\\   x\"\"\"\n",
		"s = \"\"\"a\\   \n  b\"\"\"\n",
		"ints = [0, +1, -0, 1_000, 0xDEAD_beef, 0o755, 0b1010, 99999999999999999999999]\n",
		"bad = 01\n",
		"bad = 1__0\n",
		"bad = 0x\n",
		"floats = [1.0, -0.0, 3e2, 1E-7, 6.626e-34, 1_0.5_0, inf, -inf, +nan, nan, 1e400]\n",
		"bad = 1.\n",
		"bad = .5\n",
		"bad = 1e\n",
		"dates = [1979-05-27, 1979-05-27T07:32:00, 1979-05-27 07:32:00.999999999-07:00, 07:32:00, 00:32:00.5]\n",
		"bad = 2021-02-29\n",
		"ok = 2020-02-29\n",
		"bad = 0000-01-01\n",
		"odd = 1979-05-27T25:00:00\n",
		"odd = 1979-05-27Tfoo\n",
		"b = true\nc = false\n",
		"b = truth\n",
		"key with space = 1\n",
		"\"\" = 1\n",
		"'' = 2\n",
		"a . b . c = 1\n",
		"a = 1 # trailing\n",
		"a = 1 junk\n",
		"a = \n",
		"= 1\n",
		"[a\n",
		"[[a]\n",
		"[ a . b ]\n[ [ c ] ]\n",
		"a = 1\r\nb = 2\r\n",
		"a = 1\rb = 2\n",
		"# comment with \u0001 control\n",
		"a = \"\u00e9\u4e2d\" # \u00e9\n\"\u00e9\" = 1\n",
		"[\"a\\u0000b\"]\nx = 1\n",
		"a = [[1], [\"x\"], [{}]]\n",
		"a = [{b = 1}, {b = 2}]\n[[a]]\n",
		"x = 1\nx = 2\n",
		"[x]\n[x.y]\n[x]\n",
		"[x.y]\n[x]\ny = 1\n",
		"[x.y]\n[x]\ny.z = 1\n",
		"[[x.y]]\n[x]\n[x.y.z]\n",
		"[[x]]\n[[x.y]]\n[x.y.z]\nq = 1\n[x.y]\n",
		"a = \"\"\"\"\"\"\nb = ''''''\n",
		"a = \"\"\"\"quoted\"\"\"\"\n",
	}
	for _, doc := range docs {
		cmd := exec.Command("python3", "-c", tomlSummaryPy)
		cmd.Stdin = strings.NewReader(doc)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("python on %q: %v", doc, err)
		}
		got := "invalid"
		if v, err := tomlLoads(doc); err == nil {
			got = tomlSummary(v)
		}
		if got != string(out) {
			t.Errorf("document %q\ntomllib: %s\nport:    %s", doc, out, got)
		}
	}
}
