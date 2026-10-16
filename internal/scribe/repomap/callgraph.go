package repomap

import (
	"fmt"
	"sort"
	"strings"
)

// callRef locates a calling function without copying its Symbol: the call
// graph is built on every recompute, and copying symbols with their call
// lists made recompute the dominant cost of a warm rebuild.
type callRef struct {
	file string
	line int
}

// maxCallerDefs is how many function definitions a name may have before a
// call to it is left unresolved. A common name such as "Close" would link
// every caller to every type that has one.
const maxCallerDefs = 3

func isFuncKind(k string) bool { return k == "function" || k == "method" }

// buildCallers indexes, by callee name, the functions and methods whose
// bodies mention that name. Matching is by name only, so a caller may be
// credited with a same-named function elsewhere; the maxCallerDefs limit
// keeps that to unambiguous names. Callers hold m.mu for writing.
func buildCallers(paths []string, files map[string]*fileInfo) map[string][]callRef {
	defs := map[string]int{}
	for _, p := range paths {
		for _, s := range files[p].Symbols {
			if isFuncKind(s.Kind) {
				defs[s.Name]++
			}
		}
	}
	out := map[string][]callRef{}
	for _, p := range paths {
		for _, s := range files[p].Symbols {
			if !isFuncKind(s.Kind) {
				continue
			}
			for rest := s.calls; rest != ""; {
				var c string
				if i := strings.IndexByte(rest, ' '); i >= 0 {
					c, rest = rest[:i], rest[i+1:]
				} else {
					c, rest = rest, ""
				}
				if n := defs[c]; n == 0 || n > maxCallerDefs {
					continue
				}
				out[c] = append(out[c], callRef{file: s.File, line: s.Line})
			}
		}
	}
	return out
}

// Callers returns the functions and methods whose bodies mention name, in
// file and line order. It is name-based and approximate; see buildCallers.
func (m *Map) Callers(name string) []Symbol {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Symbol
	for _, ref := range m.callers[name] {
		for _, s := range m.fileSyms[ref.file] {
			if s.Line == ref.line && isFuncKind(s.Kind) {
				out = append(out, s)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// Callees returns the function definitions that the function at file:line
// calls. It uses the same name-based rule as Callers: a called name counts only
// when it has at most maxCallerDefs function definitions, so a common name does
// not link to every type that has it. Results are in file and line order.
func (m *Map) Callees(file string, line int) []Symbol {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.calleesOf(file, line)
}

// CalleesNamed returns the callees of every function named name, merged and
// de-duplicated. It is the name-level counterpart of Callees.
func (m *Map) CalleesNamed(name string) []Symbol {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := map[string]bool{}
	var out []Symbol
	for _, s := range m.symsByName[name] {
		if !isFuncKind(s.Kind) {
			continue
		}
		for _, c := range m.calleesOfSym(s) {
			k := fmt.Sprintf("%s:%d", c.File, c.Line)
			if !seen[k] {
				seen[k] = true
				out = append(out, c)
			}
		}
	}
	sortSymbols(out)
	return out
}

// calleesOf finds the function at file:line and returns what it calls. Callers
// hold m.mu for reading.
func (m *Map) calleesOf(file string, line int) []Symbol {
	for _, s := range m.fileSyms[file] {
		if s.Line == line && isFuncKind(s.Kind) {
			return m.calleesOfSym(s)
		}
	}
	return nil
}

// calleesOfSym resolves the names in s.calls to function definitions. Callers
// hold m.mu for reading.
func (m *Map) calleesOfSym(s Symbol) []Symbol {
	var out []Symbol
	seen := map[string]bool{}
	for rest := s.calls; rest != ""; {
		var c string
		if i := strings.IndexByte(rest, ' '); i >= 0 {
			c, rest = rest[:i], rest[i+1:]
		} else {
			c, rest = rest, ""
		}
		var defs []Symbol
		for _, d := range m.symsByName[c] {
			if isFuncKind(d.Kind) {
				defs = append(defs, d)
			}
		}
		if len(defs) == 0 || len(defs) > maxCallerDefs {
			continue
		}
		for _, d := range defs {
			k := fmt.Sprintf("%s:%d", d.File, d.Line)
			if !seen[k] {
				seen[k] = true
				out = append(out, d)
			}
		}
	}
	sortSymbols(out)
	return out
}

func sortSymbols(ss []Symbol) {
	sort.SliceStable(ss, func(i, j int) bool {
		if ss[i].File != ss[j].File {
			return ss[i].File < ss[j].File
		}
		return ss[i].Line < ss[j].Line
	})
}
