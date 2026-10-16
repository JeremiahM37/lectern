package fs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// editSpec is one string replacement.
type editSpec struct {
	Old        string
	New        string
	ReplaceAll bool
}

// NewWriteFile returns write_file, which creates or overwrites a file.
func NewWriteFile(undo *UndoStack) tools.Tool {
	if undo == nil {
		undo = NewUndoStack()
	}
	return &tool{
		spec: newSpec("write_file",
			"Create a file or overwrite an existing one with the full content. An existing file must be read first. "+
				"Parent directories are created. Prefer edit_file for changes to part of a file.",
			tools.Schema(map[string]any{
				"path":    tools.Str("File to write, relative to the project root."),
				"content": tools.Str("The complete new content of the file."),
			}, "path", "content")),
		run: func(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
			var a struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := tools.Decode(args, &a); err != nil {
				return fail(err)
			}
			t, err := resolve(ctx, env, "write_file", a.Path, accessWrite)
			if err != nil {
				return fail(err)
			}
			var before []byte
			fi, statErr := os.Stat(t.abs)
			exists := statErr == nil
			if exists && fi.IsDir() {
				return tools.Errorf("%s is a directory.", t.rel)
			}
			if exists {
				if env.Files != nil {
					if !env.Files.WasRead(t.abs) {
						return tools.Errorf("%s already exists and has not been read this session. Read it first, or use edit_file for a targeted change.", t.rel)
					}
					if env.Files.StaleSinceRead(t.abs) {
						return tools.Errorf("%s changed on disk since you read it. Read it again before writing.", t.rel)
					}
				}
				if before, err = os.ReadFile(t.abs); err != nil {
					return tools.Errorf("read %s: %v", t.rel, err)
				}
			}
			after := a.Content
			if exists && crlfStyle(before) && !strings.Contains(after, "\r\n") {
				after = fromLF(after, true)
			}
			res := commit(env, undo, t, before, exists, after)
			if res.IsError {
				return res
			}
			if exists {
				res.Output = fmt.Sprintf("Wrote %s (%d lines).", t.rel, countLines(after))
			} else {
				res.Output = fmt.Sprintf("Created %s (%d lines).", t.rel, countLines(after))
			}
			return res
		},
	}
}

// NewEditFile returns edit_file: one exact string replacement, with a
// whitespace-tolerant fallback.
func NewEditFile(undo *UndoStack) tools.Tool {
	if undo == nil {
		undo = NewUndoStack()
	}
	return &tool{
		spec: newSpec("edit_file",
			"Replace old_string with new_string in a file. The file must be read first. old_string must match exactly once "+
				"unless replace_all is true; add surrounding lines to make it unique. If old_string is empty and the file does not exist, "+
				"the file is created with new_string as its content.",
			tools.Schema(map[string]any{
				"path":        tools.Str("File to edit, relative to the project root."),
				"old_string":  tools.Str("Exact text to replace, copied from the file including indentation."),
				"new_string":  tools.Str("Replacement text."),
				"replace_all": tools.Bool("Replace every occurrence instead of requiring a unique match. Default false."),
			}, "path", "old_string", "new_string")),
		run: func(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
			var a struct {
				Path       string   `json:"path"`
				OldString  string   `json:"old_string"`
				NewString  string   `json:"new_string"`
				ReplaceAll flexBool `json:"replace_all"`
			}
			if err := tools.Decode(args, &a); err != nil {
				return fail(err)
			}
			t, err := resolve(ctx, env, "edit_file", a.Path, accessWrite)
			if err != nil {
				return fail(err)
			}
			if _, statErr := os.Stat(t.abs); os.IsNotExist(statErr) {
				if a.OldString != "" {
					return tools.Errorf("File not found: %s. To create it, pass an empty old_string and the full content as new_string.", t.rel)
				}
				res := commit(env, undo, t, nil, false, a.NewString)
				if !res.IsError {
					res.Output = fmt.Sprintf("Created %s (%d lines).", t.rel, countLines(a.NewString))
				}
				return res
			}
			return editExisting(env, undo, t, []editSpec{{Old: a.OldString, New: a.NewString, ReplaceAll: bool(a.ReplaceAll)}}, false)
		},
	}
}

// NewMultiEdit returns multi_edit: several replacements applied in order to
// one file, all or nothing.
func NewMultiEdit(undo *UndoStack) tools.Tool {
	if undo == nil {
		undo = NewUndoStack()
	}
	return &tool{
		spec: newSpec("multi_edit",
			"Apply several replacements to one file in order. Each old_string is matched against the result of the previous edits. "+
				"Atomic: if any edit fails, nothing is written. The file must be read first.",
			tools.Schema(map[string]any{
				"path": tools.Str("File to edit, relative to the project root."),
				"edits": tools.Arr("Replacements, applied in order.", map[string]any{
					"type": "object",
					"properties": map[string]any{
						"old_string":  tools.Str("Exact text to replace."),
						"new_string":  tools.Str("Replacement text."),
						"replace_all": tools.Bool("Replace every occurrence. Default false."),
					},
					"required": []string{"old_string", "new_string"},
				}),
			}, "path", "edits")),
		run: func(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
			var a struct {
				Path  string          `json:"path"`
				Edits json.RawMessage `json:"edits"`
			}
			if err := tools.Decode(args, &a); err != nil {
				return fail(err)
			}
			var list []struct {
				OldString  string   `json:"old_string"`
				NewString  string   `json:"new_string"`
				ReplaceAll flexBool `json:"replace_all"`
			}
			if err := decodeList(a.Edits, &list); err != nil {
				return fail(err)
			}
			if len(list) == 0 {
				return tools.Errorf("edits must contain at least one edit.")
			}
			t, err := resolve(ctx, env, "multi_edit", a.Path, accessWrite)
			if err != nil {
				return fail(err)
			}
			specs := make([]editSpec, len(list))
			for i, e := range list {
				specs[i] = editSpec{Old: e.OldString, New: e.NewString, ReplaceAll: bool(e.ReplaceAll)}
			}
			return editExisting(env, undo, t, specs, true)
		},
	}
}

// editExisting applies edits to a file that exists, after the read and
// staleness gates.
func editExisting(env *tools.Env, undo *UndoStack, t target, edits []editSpec, multi bool) tools.Result {
	fi, err := statPath(t)
	if err != nil {
		return fail(err)
	}
	if fi.IsDir() {
		return tools.Errorf("%s is a directory.", t.rel)
	}
	if env.Files != nil {
		if !env.Files.WasRead(t.abs) {
			return tools.Errorf("%s has not been read this session. Read it first so the edit is based on its current content.", t.rel)
		}
		if env.Files.StaleSinceRead(t.abs) {
			return tools.Errorf("%s changed on disk since you read it. Read it again, then retry the edit.", t.rel)
		}
	}
	for _, e := range edits {
		if e.Old == "" {
			return tools.Errorf("%s exists, so old_string must not be empty. Give the exact text to replace, or use write_file to replace the whole file.", t.rel)
		}
	}
	before, err := os.ReadFile(t.abs)
	if err != nil {
		return tools.Errorf("read %s: %v", t.rel, err)
	}
	if isBinaryData(before) {
		return tools.Errorf("Cannot edit binary file: %s.", t.rel)
	}
	crlf := crlfStyle(before)
	newText, msgs, err := applyEdits(toLF(string(before)), edits, multi)
	if err != nil {
		return tools.Errorf("%s: %v", t.rel, err)
	}
	res := commit(env, undo, t, before, true, fromLF(newText, crlf))
	if res.IsError {
		return res
	}
	if multi {
		res.Output = fmt.Sprintf("Applied %d edits to %s: %s.", len(edits), t.rel, strings.Join(msgs, "; "))
	} else {
		res.Output = fmt.Sprintf("Edited %s: %s.", t.rel, msgs[0])
	}
	return res
}

// commit writes after to the file, records the change for undo and the
// read state, and builds the diff. The caller sets Output.
func commit(env *tools.Env, undo *UndoStack, t target, before []byte, existed bool, after string) tools.Result {
	if err := os.MkdirAll(filepath.Dir(t.abs), 0o755); err != nil {
		return tools.Errorf("create directories for %s: %v", t.rel, err)
	}
	if env.Files != nil {
		env.Files.BeforeWrite(t.abs)
	}
	if err := writeAtomic(t.abs, []byte(after), existingPerm(t.abs)); err != nil {
		return tools.Errorf("write %s: %v", t.rel, err)
	}
	if env.Files != nil {
		env.Files.MarkRead(t.abs)
	}
	undo.push(t.abs, undoEntry{before: before, existed: existed, afterHash: hashBytes([]byte(after))})

	diff, added, removed := UnifiedDiff(t.rel, toLF(string(before)), toLF(after))
	res := tools.Result{Diff: diff, Files: []string{t.rel}}
	if !existed {
		res.Summary = fmt.Sprintf("new file, %d lines", countLines(after))
	} else {
		res.Summary = diffSummary(added, removed)
	}
	return res
}

func diffSummary(added, removed int) string {
	if added == 0 && removed == 0 {
		return "no changes"
	}
	return fmt.Sprintf("+%d -%d", added, removed)
}

// applyEdits applies edits in order to content (already LF). multi selects
// the atomic-batch error wording.
func applyEdits(content string, edits []editSpec, multi bool) (string, []string, error) {
	cur := content
	msgs := make([]string, 0, len(edits))
	for i, e := range edits {
		next, msg, err := applyOne(cur, e)
		if err != nil {
			if multi {
				return "", nil, fmt.Errorf("edit %d of %d: %v. NO edits were applied (atomic rollback). Re-read the file and retry ALL edits with fresh content", i+1, len(edits), err)
			}
			return "", nil, err
		}
		cur = next
		if multi {
			msg = fmt.Sprintf("edit %d: %s", i+1, msg)
		}
		msgs = append(msgs, msg)
	}
	return cur, msgs, nil
}

// applyOne performs one replacement on LF content. Exact matches win. When
// there is none, it tries a line-by-line match that ignores indentation and
// trailing whitespace, and re-indents the new text to fit.
func applyOne(content string, e editSpec) (string, string, error) {
	old := toLF(e.Old)
	nw := toLF(e.New)
	if old == "" {
		return "", "", errors.New("old_string must not be empty")
	}
	if old == nw {
		return "", "", errors.New("old_string and new_string are identical, so there is nothing to change")
	}

	if idx := allIndexes(content, old); len(idx) > 0 {
		lines := lineNumbers(content, idx)
		if len(idx) > 1 && !e.ReplaceAll {
			return "", "", ambiguous(len(idx), lines)
		}
		if e.ReplaceAll {
			return strings.ReplaceAll(content, old, nw),
				fmt.Sprintf("%d replacements (lines %s)", len(idx), joinInts(lines, 10)), nil
		}
		return content[:idx[0]] + nw + content[idx[0]+len(old):],
			fmt.Sprintf("1 replacement at line %d", lines[0]), nil
	}

	spans := fuzzySpans(content, old)
	if len(spans) == 0 {
		return "", "", notFound(content, old)
	}
	if len(spans) > 1 && !e.ReplaceAll {
		lines := make([]int, len(spans))
		for i, s := range spans {
			lines[i] = s.line
		}
		return "", "", ambiguous(len(spans), lines)
	}
	newBody := strings.TrimSuffix(nw, "\n")
	var sb strings.Builder
	prev := 0
	var lines []int
	for _, s := range spans {
		sb.WriteString(content[prev:s.start])
		end := s.end
		if newBody == "" && end < len(content) {
			end++ // deleting whole lines also takes the newline
		}
		sb.WriteString(reindent(newBody, s.ci, s.oi))
		prev = end
		lines = append(lines, s.line)
	}
	sb.WriteString(content[prev:])
	return sb.String(), fmt.Sprintf("%d replacement(s) at line(s) %s, matched ignoring indentation and trailing whitespace; new text re-indented",
		len(spans), joinInts(lines, 10)), nil
}

func ambiguous(n int, lines []int) error {
	return fmt.Errorf("old_string matches %d places (lines %s). Add surrounding lines so it matches once, or set replace_all to change all of them",
		n, joinInts(lines, 10))
}

// notFound explains a failed match and shows the three closest regions.
func notFound(content, old string) error {
	lines := strings.Split(content, "\n")
	n := strings.Count(strings.TrimSuffix(old, "\n"), "\n") + 1
	if n > len(lines) {
		n = len(lines)
	}
	want := bigrams(normalizeSpace(old))
	type cand struct {
		start int
		score float64
	}
	var cands []cand
	for i := 0; i+n <= len(lines); i++ {
		win := strings.Join(lines[i:i+n], "\n")
		cands = append(cands, cand{i, dice(bigrams(normalizeSpace(win)), want)})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })

	var sb strings.Builder
	sb.WriteString("old_string not found. Copy the text exactly from a fresh read of the file")
	shown := 0
	for _, c := range cands {
		if shown == 3 || c.score == 0 {
			break
		}
		shown++
		if shown == 1 {
			sb.WriteString(". Closest regions:")
		}
		fmt.Fprintf(&sb, "\nlines %d-%d (similarity %.0f%%):", c.start+1, c.start+n, c.score*100)
		for i := c.start; i < c.start+n && i-c.start < 6; i++ {
			fmt.Fprintf(&sb, "\n%6d  %s", i+1, clipLine(lines[i], 160))
		}
	}
	if shown == 0 {
		sb.WriteString(". No similar text was found.")
	}
	return errors.New(sb.String())
}

type fuzzySpan struct {
	start, end int    // byte offsets into content, end excludes the newline
	line       int    // 1-based first line
	ci, oi     string // leading whitespace of the matched first non-blank line and of old's
}

// fuzzySpans finds whole-line matches of old that agree after trimming each
// line. Matches do not overlap.
func fuzzySpans(content, old string) []fuzzySpan {
	oldLines := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	want := make([]string, len(oldLines))
	first := -1
	for i, l := range oldLines {
		want[i] = strings.TrimSpace(l)
		if want[i] != "" && first < 0 {
			first = i
		}
	}
	if first < 0 {
		return nil // all blank: too broad to match safely
	}
	lines := strings.Split(content, "\n")
	starts := make([]int, len(lines))
	pos := 0
	for i, l := range lines {
		starts[i] = pos
		pos += len(l) + 1
	}
	var out []fuzzySpan
	for i := 0; i+len(want) <= len(lines); {
		ok := true
		for k := range want {
			if strings.TrimSpace(lines[i+k]) != want[k] {
				ok = false
				break
			}
		}
		if !ok {
			i++
			continue
		}
		last := i + len(want) - 1
		out = append(out, fuzzySpan{
			start: starts[i],
			end:   starts[last] + len(lines[last]),
			line:  i + 1,
			ci:    leadingSpace(lines[i+first]),
			oi:    leadingSpace(oldLines[first]),
		})
		i += len(want)
	}
	return out
}

// reindent shifts text from the old indentation to the matched indentation.
func reindent(text, contentIndent, oldIndent string) string {
	if contentIndent == oldIndent {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		switch {
		case strings.TrimSpace(l) == "":
			lines[i] = ""
		case oldIndent == "":
			lines[i] = contentIndent + l
		case strings.HasPrefix(l, oldIndent):
			lines[i] = contentIndent + l[len(oldIndent):]
		}
	}
	return strings.Join(lines, "\n")
}

func leadingSpace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

func allIndexes(s, sub string) []int {
	var out []int
	for i := 0; ; {
		j := strings.Index(s[i:], sub)
		if j < 0 {
			return out
		}
		out = append(out, i+j)
		i += j + len(sub)
	}
}

// lineNumbers converts byte offsets into 1-based line numbers.
func lineNumbers(s string, offsets []int) []int {
	out := make([]int, len(offsets))
	for i, off := range offsets {
		out[i] = strings.Count(s[:off], "\n") + 1
	}
	return out
}

func joinInts(v []int, max int) string {
	parts := make([]string, 0, len(v))
	for i, n := range v {
		if i == max {
			parts = append(parts, fmt.Sprintf("and %d more", len(v)-max))
			break
		}
		parts = append(parts, fmt.Sprint(n))
	}
	return strings.Join(parts, ", ")
}

func normalizeSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func bigrams(s string) map[string]int {
	r := []rune(s)
	m := map[string]int{}
	for i := 0; i+1 < len(r); i++ {
		m[string(r[i:i+2])]++
	}
	return m
}

// dice is the Dice coefficient of two bigram multisets, in [0, 1].
func dice(a, b map[string]int) float64 {
	na, nb := 0, 0
	for _, c := range a {
		na += c
	}
	for _, c := range b {
		nb += c
	}
	if na+nb == 0 {
		return 0
	}
	common := 0
	for k, c := range a {
		if d := b[k]; d > 0 {
			if c < d {
				common += c
			} else {
				common += d
			}
		}
	}
	return 2 * float64(common) / float64(na+nb)
}
