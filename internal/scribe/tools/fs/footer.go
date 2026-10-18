package fs

import (
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// FileMemory is what the memory store offers the file tools. The store
// publishes itself under Services["memory"]. FileHint returns one line of
// memory about rel that belongs on a read or edit result, such as a gotcha
// on that file, or "" when there is none. The implementer suppresses hints
// for the rest of a turn once the model has searched memory itself (spec 05
// §1.7): the file tools do not track turns.
type FileMemory interface {
	FileHint(rel string) string
}

// maxFileHint keeps a hint to one short line. Every read and edit result
// carries it, so it costs tokens each time.
const maxFileHint = 200

// memoryFooter returns the memory hint for rel as a result footer, or "" when
// no store is published or it has nothing to say. The footer starts with a
// newline so it can follow the body or the hash line directly.
func memoryFooter(env *tools.Env, rel string) string {
	m, ok := tools.Service[FileMemory](env, "memory")
	if !ok {
		return ""
	}
	hint := strings.Join(strings.Fields(m.FileHint(rel)), " ")
	if hint == "" {
		return ""
	}
	if r := []rune(hint); len(r) > maxFileHint {
		hint = string(r[:maxFileHint]) + "…"
	}
	return "\n" + hint
}
