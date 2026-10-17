package fs

import (
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// impactNames is how many importing files an impact line names before it
// says "and N more".
const impactNames = 3

// editImpact says which files import the edited one, from the code map, for
// the "[impact: ...]" line under an edit's diff. It is for the user to see
// and is not sent to the model. It returns "" when there is no map, the file
// is not in it, or nothing imports it. It never fails an edit.
func editImpact(env *tools.Env, abs string) string {
	m, ok := tools.Service[*repomap.Map](env, "repomap")
	if !ok || m == nil {
		return ""
	}
	im, err := m.Impact(abs)
	if err != nil {
		return ""
	}
	var direct []string
	for _, d := range im.Dependents {
		if d.Level == 1 {
			direct = append(direct, d.Path)
		}
	}
	return formatImpact(direct)
}

// formatImpact words the importers: "imported by a.go, b.go, c.go" and
// "imported by 5 files: a.go, b.go, c.go, and 2 more". Empty input gives "".
func formatImpact(importers []string) string {
	n := len(importers)
	if n == 0 {
		return ""
	}
	shown := importers
	if len(shown) > impactNames {
		shown = shown[:impactNames]
	}
	var sb strings.Builder
	if n == 1 {
		sb.WriteString("imported by ")
	} else {
		fmt.Fprintf(&sb, "imported by %d files: ", n)
	}
	sb.WriteString(strings.Join(shown, ", "))
	if n > len(shown) {
		fmt.Fprintf(&sb, ", and %d more", n-len(shown))
	}
	return sb.String()
}
