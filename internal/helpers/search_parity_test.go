package helpers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/nativeidentity"
)

func searchScripts(t *testing.T) (string, string) {
	t.Helper()
	search, err := os.ReadFile("../api/scripts/native_search.py")
	if err != nil {
		t.Fatal(err)
	}
	read, err := os.ReadFile("../api/scripts/native_search_read.py")
	if err != nil {
		t.Fatal(err)
	}
	return nativeidentity.RecordsScript + "\n" + string(search),
		"NATIVE_SEARCH_LIBRARY=True\n" + nativeidentity.RecordsScript + "\n" + string(search) + "\n" + string(read)
}

// withCache runs fn with the search cache pointed at dir.
func withCache(t *testing.T, dir string, fn func() (string, int)) (string, int) {
	t.Helper()
	t.Setenv("LECTERN_NATIVE_SEARCH_CACHE", dir)
	return fn()
}

// Both implementations index the same histories into separate caches; every
// search, incremental re-index and paged read must agree, and each must read
// an index the other built.
func TestNativeSearchParity(t *testing.T) {
	requirePython(t)
	f := newNativeFixture(t)
	oversized := `{"type":"user","message":{"role":"user","content":"` + strings.Repeat("x", searchLineLimit) + `"}}`
	writeFile(t, filepath.Join(f.claudeDir, "big.jsonl"), lines(
		`{"type":"user","sessionId":"`+cidC+`","cwd":"/big","message":{"role":"user","content":"big needle start"}}`,
		oversized,
		`{"type":"assistant","message":{"role":"assistant","content":"after the oversized needle"}}`,
	), at(70))
	searchPy, readPy := searchScripts(t)
	pyCache, goCache := filepath.Join(f.root, "py-cache"), filepath.Join(f.root, "go-cache")
	search := func(label string, args ...string) string {
		t.Helper()
		pyOut, pyCode := withCache(t, pyCache, func() (string, int) { return runPython(t, searchPy, args...) })
		goOut, goCode := withCache(t, goCache, func() (string, int) { return runGo(t, "native-search", args...) })
		if pyOut != goOut || pyCode != goCode {
			t.Errorf("%s: output differs\npython (exit %d):\n%.4000s\ngo (exit %d):\n%.4000s", label, pyCode, pyOut, goCode, goOut)
		}
		return goOut
	}
	queries := []string{"needle", "Ω words", `"quoted`, "nothing-matches-this", "   ", strings.Repeat("a", 501), "hidden", "tool"}
	for _, agent := range []string{"claude", "codex"} {
		for _, q := range queries {
			search(agent+" "+q, agent, "--", q)
		}
		search(agent+" reset", "--reset", agent, "--", "needle")
	}
	search("bad agent", "gemini", "--", "needle")

	// Appending and rewriting transcripts exercises the incremental paths.
	path := filepath.Join(f.claudeDir, cidA+".jsonl")
	data, _ := os.ReadFile(path)
	data = append(data, []byte(`", "x":1}}`+"\n"+`{"type":"user","message":{"role":"user","content":"appended needle"}}`+"\n")...)
	writeFile(t, path, string(data), at(80))
	search("after append", "claude", "--", "needle")
	writeFile(t, filepath.Join(f.claudeDir, ".hidden-"+cidD+".jsonl"), lines(`{"type":"user","sessionId":"`+cidD+`","cwd":`+jsonStr(f.ws)+`,"message":{"role":"user","content":"rewritten needle"}}`), at(81))
	search("after rewrite", "claude", "--", "needle")
	os.Remove(filepath.Join(f.claudeDir, cidB+".jsonl"))
	out := search("after removal", "claude", "--", "needle")

	var reply searchWorkerReply
	if err := json.Unmarshal([]byte(out), &reply); err != nil || len(reply.Matches) < 3 || !strings.Contains(out, cidLong) {
		t.Fatalf("search reply %q: %v", out, err)
	}
	read := func(label, cache string, py bool, args ...string) (string, int) {
		return withCache(t, cache, func() (string, int) {
			if py {
				return runPython(t, readPy, append([]string{"--"}, args...)...)
			}
			return runGo(t, "native-search-read", append([]string{"--"}, args...)...)
		})
	}
	for _, m := range reply.Matches {
		base := []string{"claude", strconv.FormatInt(m.Document, 10), m.CID, m.Cwd, strconv.FormatInt(m.Offset, 10), m.Fingerprint, reply.Profile, "needle"}
		pages := [][]string{nil, {"latest"}, {"match"}, {"sideways"}, {"before"}, {"before", strconv.FormatInt(m.Offset, 10)}, {"after", strconv.FormatInt(m.Offset, 10)}, {"after", "1"}}
		for _, page := range pages {
			args := append(append([]string{}, base...), page...)
			label := m.CID + " " + strings.Join(page, " ")
			pyOut, pyCode := read(label, pyCache, true, args...)
			goOut, goCode := read(label, goCache, false, args...)
			if pyOut != goOut || pyCode != goCode {
				t.Errorf("read %s: output differs\npython (exit %d):\n%.3000s\ngo (exit %d):\n%.3000s", label, pyCode, pyOut, goCode, goOut)
			}
			// Each reads the other's index.
			crossPy, _ := read(label, goCache, true, args...)
			crossGo, _ := read(label, pyCache, false, args...)
			if crossPy != goOut || crossGo != pyOut {
				t.Errorf("read %s: cross-index reads differ\npython on go index:\n%.2000s\ngo on python index:\n%.2000s", label, crossPy, crossGo)
			}
		}
	}
	// A stale result, a changed profile, and a wrong fingerprint.
	m := reply.Matches[0]
	for _, args := range [][]string{
		{"claude", "999", m.CID, m.Cwd, "0", m.Fingerprint, reply.Profile, "needle"},
		{"claude", strconv.FormatInt(m.Document, 10), m.CID, m.Cwd, strconv.FormatInt(m.Offset, 10), m.Fingerprint, "other-profile", "needle"},
		{"claude", strconv.FormatInt(m.Document, 10), m.CID, m.Cwd, strconv.FormatInt(m.Offset, 10), "0000", reply.Profile, "needle"},
		{"claude", "x", m.CID, m.Cwd, "0", m.Fingerprint, reply.Profile, "needle"},
	} {
		pyOut, pyCode := read("error", pyCache, true, args...)
		goOut, goCode := read("error", goCache, false, args...)
		if pyOut != goOut || pyCode != goCode {
			t.Errorf("read %v: python (exit %d) %s go (exit %d) %s", args, pyCode, pyOut, goCode, goOut)
		}
	}
}

// The same shape the API decodes.
type searchWorkerReply struct {
	Profile string `json:"profile_key"`
	Matches []struct {
		Document    int64  `json:"document"`
		CID         string `json:"cid"`
		Cwd         string `json:"cwd"`
		Offset      int64  `json:"offset"`
		Fingerprint string `json:"fingerprint"`
	} `json:"matches"`
}
