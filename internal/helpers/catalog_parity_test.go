package helpers

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/nativeidentity"
)

// Catalog readers for the three source kinds, with record shapes from real
// CLIs and every timestamp form seconds() accepts or refuses.
func TestCatalogSessionsParity(t *testing.T) {
	requirePython(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := filepath.Join(home, "repo")
	link := filepath.Join(home, "repo-link")
	other := filepath.Join(home, "elsewhere")
	for _, d := range []string{ws, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(ws, link); err != nil {
		t.Fatal(err)
	}
	stamps := []string{`"2026-09-27T09:20:06.864Z"`, `"2026-W39-3"`, `"20260927T092006"`, `"1790500748265"`, `"1.5e9"`, `"garbage"`,
		`true`, `1790500748265123`, `1.7905e12`, `"2026-09-27T09:20:06+05:30"`, `" 2026-09-27 "`, `null`, `"2026-02-30"`, `"2026-09-27T09:20:06.1234567-01:00"`,
		`"1_790_500_748"`, `"2026-09-27 25:00"`, `12`, `"nan"`}
	for i, s := range stamps {
		row := fmt.Sprintf(`{"type":"session","id":"s-%02d","timestamp":%s,"cwd":%s,"name":"  title %d\nline  "}`, i, s, jsonStr(ws), i)
		writeFile(t, filepath.Join(home, ".pi/sessions/a", fmt.Sprintf("%02d.jsonl", i)), lines(`{"type":"other","id":"not-this"}`, row, `{"type":"message"}`), at(int64(i)))
	}
	writeFile(t, filepath.Join(home, ".pi/sessions/a/zz.jsonl"), lines(`{"type":"session","id":"elsewhere","timestamp":1,"cwd":`+jsonStr(other)+`}`), at(100))
	writeFile(t, filepath.Join(home, ".pi/sessions/a/.hidden.jsonl"), lines(`{"type":"session","id":"hidden","timestamp":1,"cwd":`+jsonStr(ws)+`}`), at(101))
	writeFile(t, filepath.Join(home, ".pi/sessions/a/bad id.jsonl"), lines(`{"type":"session","id":"../bad","timestamp":1,"cwd":`+jsonStr(ws)+`}`), at(102))
	writeFile(t, filepath.Join(home, ".pi/sessions/a/dup.jsonl"), lines(`{"type":"session","id":"s-01","timestamp":5,"cwd":`+jsonStr(ws)+`}`), at(103))
	pi := `{"files":["$UNSET_CATALOG_VAR/x/*.jsonl","~/.pi/sessions/*/*.jsonl"],"header":"session","id":"id","dir":"cwd","created":"timestamp","title":"name","agent":"pi"}`
	piMerged := `{"files":["~/.pi/sessions/a/[0-1]?.jsonl"],"id":"id","dir":"cwd","created":"timestamp","updated":"missing","agent":"pi"}`

	writeFile(t, filepath.Join(home, ".vibe/logs/session_1/meta.json"), `{"session_id":"087b2cf2-da56","start_time":"2026-09-27T09:22:11.155428+00:00","origin_directory":`+jsonStr(ws)+`,"title":null,"meta":{"updated":1790500999}}`, at(1))
	writeFile(t, filepath.Join(home, ".vibe/logs/session_2/meta.json"), `[1,2]`, at(2))
	writeFile(t, filepath.Join(home, ".vibe/logs/session_3/meta.json"), `{"session_id":"v3","start_time":1790500000,"origin_directory":`+jsonStr(link)+`,"title":"Vibe ✓ three"}`, at(3))
	vibe := `{"files":["${VIBE_TEST_HOME}/logs/*/meta.json","~/.vibe/logs/*/meta.json"],"whole":true,"id":"session_id","dir":"origin_directory","created":"start_time","updated":"meta.updated","title":"title","agent":"vibe"}`
	stem := `{"files":["~/.vibe/logs/*"],"whole":true,"id":"@stem","created":"start_time","agent":"stem"}`

	db := filepath.Join(home, ".hermes/state.db")
	os.MkdirAll(filepath.Dir(db), 0o755)
	py := `import sqlite3,sys
db=sqlite3.connect(sys.argv[1])
db.execute("create table sessions(id text, cwd text, started_at real, last_at DATETIME, title text, n int, raw blob)")
rows=[('h1',sys.argv[2],1790500883.49,'2026-09-27 09:22:11','say hi',1,b'x'),
      ('h2',sys.argv[2],1790500900,1790500950123,None,2,None),
      ('h3',sys.argv[3],1790500901,'2026-09-27T09:22:11Z','elsewhere',3,None),
      ('h4',sys.argv[2],None,'not a date','',4,None)]
db.executemany("insert into sessions values(?,?,?,?,?,?,?)",rows)
db.commit()`
	if out, err := exec.Command("python3", "-c", py, db, ws, other).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	hermes := `{"sqlite":["$UNSET_CATALOG_VAR/state.db","~/.hermes/state.db"],"query":"SELECT id, cwd, started_at, last_at AS updated, title, n AS id2, raw FROM sessions ORDER BY n","id":"id","dir":"cwd","created":"started_at","updated":"updated","title":"title","agent":"hermes"}`
	hermesBad := `{"sqlite":["~/.hermes/state.db"],"query":"SELECT * FROM missing_table","id":"id","created":"c","agent":"hermes"}`
	hermesNone := `{"sqlite":["~/.hermes/none.db"],"query":"SELECT 1","id":"id","created":"c","agent":"hermes"}`

	listing := fmt.Sprintf(`{"meta":{"n":2},"sessions":[{"id":"ses_1","title":"t","updated":1790500749323,"created":1790500748265,"directory":%s},{"id":"ses_2","created":1790500748265,"directory":%s},"skip"]}`, jsonStr(ws), jsonStr(other))
	writeFile(t, filepath.Join(home, "listing.json"), listing, at(1))
	writeFile(t, filepath.Join(home, "listing.jsonl"), lines(`{"id":"l1","created":"2026-09-27T09:20:06Z","directory":`+jsonStr(ws)+`}`, `junk`, ``, `{"id":"l2","created":5,"directory":`+jsonStr(ws)+`}`), at(2))
	command := func(cmd string) string {
		return `{"command":` + jsonStr(cmd) + `,"id":"id","dir":"directory","created":"created","updated":"updated","title":"title","agent":"opencode"}`
	}
	cases := []struct {
		spec string
		args []string
	}{
		{pi, []string{ws, "pi"}},
		{pi, []string{link, "pi"}},
		{pi, []string{ws, "pi", "s-03"}},
		{pi, []string{ws, "pi", "s-99"}},
		{pi, []string{ws, "pi", "../etc"}},
		{piMerged, []string{ws, "pi"}},
		{vibe, []string{ws, "vibe"}},
		{stem, []string{ws, "stem"}},
		{hermes, []string{ws, "hermes"}},
		{hermes, []string{ws, "hermes", "h2"}},
		{hermesBad, []string{ws, "hermes"}},
		{hermesNone, []string{ws, "hermes"}},
		{command("cat " + filepath.Join(home, "listing.json")), []string{ws, "oc"}},
		{command("cat {bin}"), []string{ws, filepath.Join(home, "listing.jsonl")}},
		{command("pwd >&2; echo '[]'"), []string{ws, "oc"}},
		{command("exit 3"), []string{ws, "oc"}},
		{command("printf '\\xff\\xfe not json'"), []string{ws, "oc"}},
		{command("echo '{\"a\":1}'"), []string{ws, "oc"}},
		{command("cat /dev/null"), []string{filepath.Join(home, "missing"), "oc"}},
		{`{"id":"id","created":"c","agent":"none"}`, []string{ws, "x"}},
		{`{"sqlite":["~/.hermes/state.db"],"id":"id","created":"c"}`, []string{ws, "x"}},
	}
	for _, c := range cases {
		args := append([]string{c.spec}, c.args...)
		same(t, c.spec+" "+fmt.Sprint(c.args), nativeidentity.CatalogSessionsScript, "catalog-sessions", args, args)
	}
}

// seconds() on many strings in one Python process, including every
// fromisoformat shape (dates, ISO weeks, basic and extended times, offsets,
// fractions) and float() forms.
func TestSecondsParity(t *testing.T) {
	requirePython(t)
	inputs := []string{"2026-09-27", "20260927", "2026-W39", "2026W39", "2026W393", "2026-W39-3", "2026-W53", "2020-W53-7", "2026-W00-1", "2026-W39-8",
		"2026-09-27T09", "2026-09-27T0920", "2026-09-27T09:20", "2026-09-27T092006", "2026-09-27T09:20:06", "2026-09-27T09:20:06.5",
		"2026-09-27T09:20:06,123", "2026-09-27T09:20:06.1234567", "2026-09-27T09:20:06.", "2026-09-27T09:20:06+01", "2026-09-27T09:20:06-0130",
		"2026-09-27T09:20:06+01:30:15.25", "2026-09-27T09:20:06+24:00", "2026-09-27T09:20:06+23:59", "2026-09-27 09:20:06Z", "2026-09-27X09:20",
		"2026-09-27T", "2026-09-27T9:20", "2026-09-27T09:2", "2026-09-27T09:20:06:07", "2026-09-2", "2026-13-01", "2026-02-29", "2024-02-29",
		"0000-01-01", "9999-12-31T23:59:59.999999", "9999-12-31T23:59:59-01:00", "0001-01-01T00:00:00+01:00", "2026-09-27T24:00", "2026-09-27T23:60",
		"2026-09-27T09:20:06.123+05", "2026-09-27T09:20:06.123456789+05:30", "2026-9-27", "2026/09/27", "abcdefgh", "12345", "1e3", "1_000", "_1",
		"1__0", "1e", ".5", "5.", "-1.5e10", "+7", "0x10", "1.5E+12", "infinity"[0:3] + "x", "NaN", "-nan", "   42   ", "４２", "2026-09-27T09:20:06 ",
		"2026-09-27T09:20:06+05:3", "2026-09-27T09:20:06+0530", "2026-09-27T09:20:06-05:30:00", "2026-09-27é09:20", "20260927T0920", "20260927T09",
		"2026-W39-3T10:00", "2026W393T1000", "2026-W3", "2026-W39-", "2026-W39T10", "2026-09-27T09:20:06.999999999999",
		"2026-09-27T09:20:06:07:08", "2026-09-27T09.5", "2026-09-27T09:20.5", "2026-09-27T0920:06", "2026-09-27T09:2006", "2026-09-27T092006.5",
		"2026-09-27T09:20:06:", "20260927\u00e90920", "2026-W39\u00e910", "2026-09-27\u20ac09:20", "2026-09-27\U0001F600\u0030\u0039",
		"2026-09-27T09:20:06+05.5", "2026-09-27T09:20:06+0530:15", "2026-09-27T12-05", "2026-09-27T12:30:45,", "2026-09-27T1", "2026-09-27T12345",
		"\u0661\u0662", "\U0001D7D0\U0001D7D1", "1\u00a0", "-\u0033.5"}
	py := `import sys, json, datetime
def seconds(value):
    if isinstance(value, bool) or value is None: return None
    if isinstance(value, (int, float)):
        v = float(value)
        while v > 1e11: v /= 1000.0
        return v
    if isinstance(value, str) and value:
        text = value.strip().replace('Z', '+00:00')
        try: stamp = datetime.datetime.fromisoformat(text)
        except ValueError:
            try: return seconds(float(text))
            except ValueError: return None
        if stamp.tzinfo is None: stamp = stamp.replace(tzinfo=datetime.timezone.utc)
        return stamp.timestamp()
    return None
for s in json.loads(sys.argv[1]): print(json.dumps(seconds(s)))`
	raw := make([]any, len(inputs))
	for i, s := range inputs {
		raw[i] = s
	}
	out, code := runPython(t, py, jsonDumps(raw, true))
	if code != 0 {
		t.Fatalf("python failed: %s", out)
	}
	want := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	for i, s := range inputs {
		v, err := seconds(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := jsonDumps(v, true); got != want[i] {
			t.Errorf("seconds(%q) = %s, python %s", s, got, want[i])
		}
	}
}
