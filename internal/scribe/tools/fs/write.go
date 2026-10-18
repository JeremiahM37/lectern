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

// maxEmptyDeleteLines is the most lines one edit may remove without putting
// any text back.
const maxEmptyDeleteLines = 10

// editSpec is one string replacement.
type editSpec struct {
	Old        string
	New        string
	ReplaceAll bool
	// LineStart and LineEnd (1-based, inclusive; 0 = not given) restrict the
	// match to that line range of the content as it is when this edit runs.
	LineStart, LineEnd int
	// IfHash, when set, must be the short hash of the exact text the edit
	// replaces. See shortHash and the read tool's lines/symbol footer.
	IfHash string
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
				return failTagged(err)
			}
			t, err := resolve(ctx, env, "write_file", a.Path, accessWrite)
			if err != nil {
				return failTagged(err)
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
						return errTag("", "%s already exists and has not been read this session. Read it first, or use edit_file for a targeted change.", t.rel)
					}
					if env.Files.StaleSinceRead(t.abs) {
						return errTag("concurrent modification", "%s changed on disk since you read it. Read it again before writing.", t.rel)
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
				res.Output = fmt.Sprintf("Created %s (%d lines).%s", t.rel, countLines(after), creationPreview(after))
			}
			res.Output += memoryFooter(env, t.rel)
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
				"unless replace_all is true; add surrounding lines to make it unique, or give lineStart (and lineEnd) from the read output "+
				"to pick one of several matches: old_string is then only matched inside that line range. Matching ignores indentation "+
				"differences and re-indents new_string to fit. ifHash is optional: pass the hash a lines= or symbol= read printed for the exact "+
				"text you are replacing, and the edit is refused if that text has changed since. If old_string is empty and the file does not "+
				"exist, the file is created with new_string as its content.",
			tools.Schema(map[string]any{
				"path":        tools.Str("File to edit, relative to the project root."),
				"old_string":  tools.Str("Exact text to replace, copied from the file including indentation."),
				"new_string":  tools.Str("Replacement text."),
				"replace_all": tools.Bool("Replace every occurrence instead of requiring a unique match. Default false."),
				"lineStart":   tools.Int("First line of the range to match in, 1-based, from the read output. Optional."),
				"lineEnd":     tools.Int("Last line of the range, inclusive. Default: lineStart plus the lines in old_string, minus one."),
				"ifHash":      tools.Str("Optional short hash (12 hex digits) of the exact text being replaced, as printed by a lines= or symbol= read. Refused on mismatch."),
			}, "path", "old_string", "new_string")),
		run: func(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
			var a struct {
				Path       string   `json:"path"`
				OldString  string   `json:"old_string"`
				NewString  string   `json:"new_string"`
				ReplaceAll flexBool `json:"replace_all"`
				LineStart  flexInt  `json:"lineStart"`
				LineEnd    flexInt  `json:"lineEnd"`
				IfHash     string   `json:"ifHash"`
			}
			if err := tools.Decode(args, &a); err != nil {
				return failTagged(err)
			}
			t, err := resolve(ctx, env, "edit_file", a.Path, accessWrite)
			if err != nil {
				return failTagged(err)
			}
			if _, statErr := os.Stat(t.abs); os.IsNotExist(statErr) {
				if a.OldString != "" {
					return tools.Errorf("File not found: %s. To create it, pass an empty old_string and the full content as new_string.", t.rel)
				}
				res := commit(env, undo, t, nil, false, a.NewString)
				if !res.IsError {
					res.Output = fmt.Sprintf("Created %s (%d lines).%s", t.rel, countLines(a.NewString), creationPreview(a.NewString)) + memoryFooter(env, t.rel)
				}
				return res
			}
			return editExisting(env, undo, t, []editSpec{{
				Old: a.OldString, New: a.NewString, ReplaceAll: bool(a.ReplaceAll),
				LineStart: int(a.LineStart), LineEnd: int(a.LineEnd), IfHash: strings.TrimSpace(a.IfHash),
			}}, false)
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
				"Each edit takes the same options as edit_file: lineStart/lineEnd restrict its match to that range of the file as it is at that point "+
				"in the batch (an earlier edit that adds or removes lines moves later line numbers), and ifHash must match the text it replaces. "+
				"Atomic: if any edit fails, nothing is written. The file must be read first.",
			tools.Schema(map[string]any{
				"path": tools.Str("File to edit, relative to the project root."),
				"edits": tools.Arr("Replacements, applied in order.", map[string]any{
					"type": "object",
					"properties": map[string]any{
						"old_string":  tools.Str("Exact text to replace."),
						"new_string":  tools.Str("Replacement text."),
						"replace_all": tools.Bool("Replace every occurrence. Default false."),
						"lineStart":   tools.Int("First line of the range to match in, 1-based. Optional."),
						"lineEnd":     tools.Int("Last line of the range, inclusive. Optional."),
						"ifHash":      tools.Str("Optional short hash of the exact text being replaced, as printed by a lines= or symbol= read."),
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
				return failTagged(err)
			}
			var list []struct {
				OldString  string   `json:"old_string"`
				NewString  string   `json:"new_string"`
				ReplaceAll flexBool `json:"replace_all"`
				LineStart  flexInt  `json:"lineStart"`
				LineEnd    flexInt  `json:"lineEnd"`
				IfHash     string   `json:"ifHash"`
			}
			if err := decodeList(a.Edits, &list); err != nil {
				return failTagged(err)
			}
			if len(list) == 0 {
				return tools.Errorf("edits must contain at least one edit.")
			}
			t, err := resolve(ctx, env, "multi_edit", a.Path, accessWrite)
			if err != nil {
				return failTagged(err)
			}
			specs := make([]editSpec, len(list))
			for i, e := range list {
				specs[i] = editSpec{
					Old: e.OldString, New: e.NewString, ReplaceAll: bool(e.ReplaceAll),
					LineStart: int(e.LineStart), LineEnd: int(e.LineEnd), IfHash: strings.TrimSpace(e.IfHash),
				}
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
		return failTagged(err)
	}
	if fi.IsDir() {
		return tools.Errorf("%s is a directory.", t.rel)
	}
	if env.Files != nil {
		if !env.Files.WasRead(t.abs) {
			return tools.Errorf("%s has not been read this session. Read it first so the edit is based on its current content.%s", t.rel, astEditHint(t.rel))
		}
		if env.Files.StaleSinceRead(t.abs) {
			return errTag("concurrent modification", "%s changed on disk since you read it. Read it again, then retry the edit.", t.rel)
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
		res := tools.Errorf("%s: %v", t.rel, err)
		res.Tag = tagOf(err)
		return res
	}
	after := fromLF(newText, crlf)
	res := commit(env, undo, t, before, true, after)
	if res.IsError {
		return res
	}
	if multi {
		if len(edits) == 1 {
			// One edit reads like edit_file's result, not "Applied 1 edits".
			res.Output = fmt.Sprintf("Edited %s: %s.", t.rel, strings.TrimPrefix(msgs[0], "edit 1: "))
		} else {
			res.Output = fmt.Sprintf("Applied %d edits to %s: %s.", len(edits), t.rel, strings.Join(msgs, "; "))
		}
		res.Output += memoryFooter(env, t.rel)
		// The whole file before and after the batch, exactly as on disk, so the
		// UI can show the joined change. Only a successful batch gets here: an
		// atomic rollback returns above and carries no Meta.
		if res.Meta == nil {
			res.Meta = map[string]any{}
		}
		res.Meta["before"] = string(before)
		res.Meta["after"] = after
	} else {
		res.Output = fmt.Sprintf("Edited %s: %s.", t.rel, msgs[0])
	}
	res.Output += memoryFooter(env, t.rel)
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
		// Only a change to an existing file can break its importers, so only
		// it names them. The line is best effort: no map means no line.
		if s := editImpact(env, t.abs); s != "" {
			res.Meta = map[string]any{"impact": s}
		}
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
	// A large block removed with nothing put back is almost always a
	// mistake (a stale range, or a model that meant to replace it). Make the
	// model say so with a smaller old_string or a whole-file write.
	if strings.TrimSpace(nw) == "" && countLines(old) > maxEmptyDeleteLines {
		return "", "", fmt.Errorf("Refusing to delete %d lines with empty replacement. Keep old_string to the lines that really change, or use write_file to replace the whole file.", countLines(old))
	}

	lo, hi, ranged, err := e.window(content, old)
	if err != nil {
		return "", "", err
	}
	inWindow := func(first, last int) bool { return !ranged || (first >= lo && last <= hi) }
	var outside []int // matches found outside the window, for the error

	if idx := allIndexes(content, old); len(idx) > 0 {
		var keep, keepLines []int
		for _, off := range idx {
			first, last := lineAt(content, off), lineAt(content, off+len(old)-1)
			if inWindow(first, last) {
				keep = append(keep, off)
				keepLines = append(keepLines, first)
			} else {
				outside = append(outside, first)
			}
		}
		if len(keep) > 0 {
			if e.IfHash != "" && shortHash(old) != e.IfHash {
				return "", "", ifHashMismatch(keepLines[0], keepLines[len(keepLines)-1])
			}
			if len(keep) > 1 && !e.ReplaceAll {
				return "", "", ambiguous(len(keep), keepLines)
			}
			var sb strings.Builder
			prev := 0
			for _, off := range keep {
				sb.WriteString(content[prev:off])
				sb.WriteString(nw)
				prev = off + len(old)
			}
			sb.WriteString(content[prev:])
			if e.ReplaceAll || len(keep) > 1 {
				return sb.String(), fmt.Sprintf("%d replacements (lines %s)", len(keep), joinInts(keepLines, 10)), nil
			}
			return sb.String(), fmt.Sprintf("1 replacement at line %d", keepLines[0]), nil
		}
	}

	spans, tier := fuzzyMatch(content, old)
	var inSpans []fuzzySpan
	for _, s := range spans {
		if inWindow(s.line, s.line+s.n-1) {
			inSpans = append(inSpans, s)
		} else {
			outside = append(outside, s.line)
		}
	}
	spans = inSpans
	if len(spans) == 0 {
		if ranged && len(outside) > 0 {
			return "", "", outsideWindow(outside, lo, hi)
		}
		return "", "", notFound(content, old)
	}
	if len(spans) > 1 && !e.ReplaceAll {
		lines := make([]int, len(spans))
		for i, s := range spans {
			lines[i] = s.line
		}
		return "", "", ambiguous(len(spans), lines)
	}
	if e.IfHash != "" {
		for _, s := range spans {
			if shortHash(content[s.start:s.end]) != e.IfHash {
				return "", "", ifHashMismatch(s.line, s.line+s.n-1)
			}
		}
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
	how := "matched ignoring indentation and trailing whitespace"
	if tier > 0 {
		how = "matched ignoring indentation, trailing whitespace and backslash escapes"
	}
	return sb.String(), fmt.Sprintf("%d replacement(s) at line(s) %s, %s; new text re-indented",
		len(spans), joinInts(lines, 10), how), nil
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
	n          int    // number of lines matched
	ci, oi     string // leading whitespace of the matched first non-blank line and of old's
}

// window returns the line range a match must lie inside, from lineStart and
// lineEnd. ranged is false when neither was given. A missing lineEnd defaults
// to the lines old spans, starting at lineStart. The range is clipped to the
// file.
func (e editSpec) window(content, old string) (lo, hi int, ranged bool, err error) {
	if e.LineStart == 0 && e.LineEnd == 0 {
		return 0, 0, false, nil
	}
	if e.LineStart < 0 || e.LineEnd < 0 {
		return 0, 0, true, errors.New("lineStart and lineEnd count lines from 1.")
	}
	total := countLines(content)
	lo = e.LineStart
	if lo == 0 {
		lo = 1
	}
	hi = e.LineEnd
	if hi == 0 {
		hi = lo + countLines(old) - 1
	}
	if lo > total {
		return 0, 0, true, fmt.Errorf("Invalid line range: lineStart %d (file has %d lines).", lo, total)
	}
	if hi < lo {
		return 0, 0, true, fmt.Errorf("lineEnd %d is before lineStart %d.", hi, lo)
	}
	if hi > total {
		hi = total
	}
	return lo, hi, true, nil
}

// lineAt is the 1-based line that byte offset off falls on.
func lineAt(s string, off int) int { return strings.Count(s[:off], "\n") + 1 }

func outsideWindow(lines []int, lo, hi int) error {
	var uniq []int
	seen := map[int]bool{}
	for _, l := range lines {
		if !seen[l] {
			seen[l] = true
			uniq = append(uniq, l)
		}
	}
	return fmt.Errorf("old_string matches at line(s) %s, outside lines %d-%d (lineStart/lineEnd). Fix the range from the read output, or leave lineStart and lineEnd out.",
		joinInts(uniq, 10), lo, hi)
}

func ifHashMismatch(first, last int) error {
	return &taggedError{tag: "concurrent modification", msg: fmt.Sprintf("ifHash does not match the text at lines %d-%d: the file changed since you read it. Re-read lines %d-%d and retry the edit with fresh content.",
		first, last, first, last)}
}

// lineNorms are the per-line normalizers of the fuzzy match, tried in order.
// The first trims whitespace. The next two also fold backslash escapes, which
// models often corrupt when they copy regex or markdown text: the second
// collapses doubled backslashes and drops the backslash of a regex
// metacharacter escape, the third drops the backslash of any escape that is
// not standard.
var lineNorms = []func(string) string{
	strings.TrimSpace,
	func(s string) string { return unescapeMeta(collapseBackslashes(strings.TrimSpace(s))) },
	func(s string) string { return dropOddEscapes(strings.TrimSpace(s)) },
}

// fuzzyMatch returns the whole-line matches of old under the first normalizer
// that finds any, with that normalizer's index (0 = whitespace only).
func fuzzyMatch(content, old string) ([]fuzzySpan, int) {
	for i, norm := range lineNorms {
		if spans := fuzzySpans(content, old, norm); len(spans) > 0 {
			return spans, i
		}
	}
	return nil, 0
}

// collapseBackslashes turns every run of backslashes into one.
func collapseBackslashes(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i > 0 && s[i-1] == '\\' {
			continue
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// regexMeta are the characters whose backslash escape means the bare
// character in a pattern the model copied.
const regexMeta = `.()[]{}*+?|^$/-`

// unescapeMeta drops the backslash in front of a regex metacharacter.
func unescapeMeta(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte(regexMeta, s[i+1]) >= 0 {
			continue
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// dropOddEscapes drops the backslash in front of any non-standard escape.
// Standard escapes (\\ \n \r \t \b \f \u \U \0-9) and escapes of letters and digits
// are kept.
func dropOddEscapes(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			next := s[i+1]
			standard := strings.IndexByte(`\nrtbfuU0123456789`, next) >= 0
			alnum := next == '_' || (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z')
			if !standard && !alnum {
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// fuzzySpans finds whole-line matches of old whose lines agree after norm.
// Matches do not overlap.
func fuzzySpans(content, old string, norm func(string) string) []fuzzySpan {
	oldLines := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	want := make([]string, len(oldLines))
	first := -1
	for i, l := range oldLines {
		want[i] = norm(l)
		if strings.TrimSpace(l) != "" && first < 0 {
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
			if norm(lines[i+k]) != want[k] {
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
			n:     len(want),
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

// creationPreview shows the first three and last two lines of a new file, so
// the model can check what it wrote without reading it back. Shorter files
// are shown whole. The result starts with a newline, or is empty.
func creationPreview(content string) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	const head, tail = 3, 2
	var b strings.Builder
	if len(lines) <= head+tail {
		for _, l := range lines {
			b.WriteString("\n  " + l)
		}
		return b.String()
	}
	for _, l := range lines[:head] {
		b.WriteString("\n  " + l)
	}
	fmt.Fprintf(&b, "\n  ... (%d lines not shown)", len(lines)-head-tail)
	for _, l := range lines[len(lines)-tail:] {
		b.WriteString("\n  " + l)
	}
	return b.String()
}

// astEditHint points a source-file edit at ast_edit, which edits a definition
// by name and does not need the text matched. It names the tool only: fs
// cannot import symedit, which imports fs.
func astEditHint(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go", ".py", ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs", ".java", ".kt", ".cs", ".rs",
		".swift", ".c", ".h", ".cc", ".cpp", ".hpp", ".rb", ".php", ".lua", ".zig", ".ex", ".exs":
		return " For a whole function, method or class, ast_edit can edit it by name (symbol) without matching its text."
	}
	return ""
}
