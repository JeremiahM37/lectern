package symedit

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// A moniker names one definition in one file so that a symbol listing and a
// later edit can agree on it without the model retyping the name. The format
// is
//
//	<path>#<kind>:<qualified name>[~<ordinal>]
//
// for example internal/app/server.go#method:Server.Start or
// web/app.ts#function:init~2. The path is the file as the edit tools see it
// (workdir-relative). The kind is the definition kind symedit reports, and the
// qualified name is Parent.Name for members. No line number is part of it, so
// a moniker still names the same definition after edits elsewhere in the file.
//
// The ordinal is only present when the same kind and qualified name appear
// more than once in the file, as with several Go init functions or TypeScript
// overloads. Each such definition is numbered from 1 in source order. An
// insert or delete before a duplicate can renumber the duplicates after it,
// and a moniker that no longer exists fails with the current list rather than
// guessing a neighbour.

// monikerRe splits a moniker into its parts. The path is the shortest prefix
// followed by #kind:, so a '#' inside a path does not confuse it, and the
// optional ordinal is the last thing in the string.
var monikerRe = regexp.MustCompile(`^(.+?)#([a-z]+):(.+?)(?:~([1-9][0-9]*))?$`)

// moniker is the parsed form of a moniker string.
type moniker struct {
	path, kind, qual string
	ord              int // 0 when the moniker has no ordinal
}

func (m moniker) String() string {
	s := m.path + "#" + m.kind + ":" + m.qual
	if m.ord > 0 {
		s += "~" + strconv.Itoa(m.ord)
	}
	return s
}

// parseMoniker reports whether ref has the moniker form. A plain name such as
// ParseList or Server.Start never does, because it has no '#kind:' part.
func parseMoniker(ref string) (moniker, bool) {
	ref = strings.TrimSpace(ref)
	mm := monikerRe.FindStringSubmatch(ref)
	if mm == nil {
		return moniker{}, false
	}
	m := moniker{path: mm[1], kind: mm[2], qual: mm[3]}
	if mm[4] != "" {
		m.ord, _ = strconv.Atoi(mm[4])
	}
	return m, true
}

// normalizeMonikerPath makes a path comparable with the one in a moniker.
func normalizeMonikerPath(rel string) string {
	return filepath.ToSlash(filepath.Clean(rel))
}

// monikerTable returns the moniker of each symbol, in the order of syms.
func monikerTable(syms []symbol, rel string) []string {
	path := normalizeMonikerPath(rel)
	type key struct{ kind, qual string }
	count := map[key]int{}
	for _, s := range syms {
		count[key{s.kind, s.qual()}]++
	}
	// Ordinals follow source order, not the order symedit happened to list.
	order := make([]int, len(syms))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return syms[order[a]].start < syms[order[b]].start
	})
	seen := map[key]int{}
	out := make([]string, len(syms))
	for _, i := range order {
		s := syms[i]
		k := key{s.kind, s.qual()}
		m := moniker{path: path, kind: s.kind, qual: s.qual()}
		if count[k] > 1 {
			seen[k]++
			m.ord = seen[k]
		}
		out[i] = m.String()
	}
	return out
}

// resolveMoniker finds the one definition that raw names. It never guesses:
// a moniker for another file, or one that matches no definition exactly, fails
// and lists what the file does have.
func resolveMoniker(syms []symbol, rel, raw string) (symbol, error) {
	want, ok := parseMoniker(raw)
	if !ok {
		return symbol{}, fmt.Errorf("%q is not a moniker", raw)
	}
	path := normalizeMonikerPath(rel)
	if want.path != path {
		return symbol{}, fmt.Errorf("moniker %q names %s, but this edit targets %s. Use a moniker from %s or change the path",
			raw, want.path, path, path)
	}
	if len(syms) == 0 {
		return symbol{}, fmt.Errorf("no definitions found in %s, so moniker %q cannot be located. Use edit_file for this file", rel, raw)
	}
	table := monikerTable(syms, rel)
	for i, m := range table {
		if m == want.String() {
			return syms[i], nil
		}
	}
	// A bare moniker for a name declared several times gets a hint naming the
	// ordinals that do exist, since that is the usual mistake.
	var same []int
	for i, s := range syms {
		if s.kind == want.kind && s.qual() == want.qual {
			same = append(same, i)
		}
	}
	if want.ord == 0 && len(same) > 1 {
		return symbol{}, fmt.Errorf("moniker %q matches %d definitions in %s. Add an ordinal to pick one: %s",
			raw, len(same), rel, monikerRange(table, same))
	}
	return symbol{}, fmt.Errorf("no definition has moniker %q in %s. Monikers in this file: %s",
		raw, rel, monikerList(table))
}

// monikerRange lists the monikers of the given indices, for an ambiguous bare moniker.
func monikerRange(table []string, idx []int) string {
	var parts []string
	for _, i := range idx {
		parts = append(parts, table[i])
	}
	return strings.Join(parts, ", ")
}

// monikerList caps the list the way the name-lookup error does, so a large
// file does not flood the message.
func monikerList(table []string) string {
	const maxShown = 25
	if len(table) == 0 {
		return "none"
	}
	shown := table
	more := ""
	if len(shown) > maxShown {
		more = fmt.Sprintf(", ... and %d more", len(table)-maxShown)
		shown = shown[:maxShown]
	}
	return strings.Join(shown, ", ") + more
}
