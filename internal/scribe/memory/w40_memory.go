package memory

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Memory hints on read and edit results (spec 05 §1.7) and the compaction
// diary (spec 03 §8.4). Both are small and both are written from the
// signals the store already keeps, so they add no new storage.

// hintFileSignal is the recall signal for "this record names the file".
const hintFileSignal = "file"

// diaryMaxFiles caps the files a diary entry names (spec 03 §8.4).
const diaryMaxFiles = 10

// diaryBodyChars keeps the diary body within the memory tool's own limit.
const diaryBodyChars = 2000

// diaryTitleChars keeps the title within the memory tool's own limit.
const diaryTitleChars = 200

// MarkSearched records that the model searched memory this turn. Until the
// next BeginTurn, FileHint returns nothing: the model already has the memory
// it asked for, so repeating it on every read would only cost tokens.
func (s *Stores) MarkSearched() {
	if s != nil {
		s.searchedThisTurn.Store(true)
	}
}

// BeginTurn clears the per-turn suppression set by MarkSearched.
func (s *Stores) BeginTurn() {
	if s != nil {
		s.searchedThisTurn.Store(false)
	}
}

// FileHint returns one line of memory about rel for a read or edit result: the
// top record that names rel, as "<category> <title> (<id>)". It returns "" when
// no record names the file, or the model searched memory this turn.
//
// It reads with no query, so it never calls the embedding provider: the file
// signal alone decides, and a read or edit does not wait on the network.
func (s *Stores) FileHint(rel string) string {
	if s == nil || rel == "" || s.searchedThisTurn.Load() {
		return ""
	}
	var all []Scored
	for _, d := range s.readDBs() {
		got, err := d.recallWith("", []string{rel}, 3, nil, "")
		if err != nil {
			return ""
		}
		all = append(all, got...)
	}
	for _, m := range all {
		if !hasSignal(m, hintFileSignal) {
			continue
		}
		return m.Category + " " + m.Title + " (" + shortID(m.ID) + ")"
	}
	return ""
}

func hasSignal(m Scored, code string) bool {
	for _, w := range m.Why {
		if w == code {
			return true
		}
	}
	return false
}

// pathToken finds candidate relative paths in a summary: a word with a dot
// and an extension. Each candidate is kept only when it is a file under the
// workdir, so prose such as "e.g." never becomes a file.
var pathToken = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_./-]*\.[A-Za-z0-9]{1,8}`)

// filesMentioned returns up to max relative paths in text that exist as files
// under workdir, in order of first mention.
func filesMentioned(text, workdir string, max int) []string {
	if workdir == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, tok := range pathToken.FindAllString(text, -1) {
		tok = strings.TrimPrefix(tok, "./")
		if seen[tok] || filepath.IsAbs(tok) || strings.Contains(tok, "..") {
			continue
		}
		seen[tok] = true
		info, err := os.Stat(filepath.Join(workdir, tok))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, tok)
		if len(out) == max {
			break
		}
	}
	return out
}

// SaveCompaction writes the compaction summary to the write store as a diary
// entry: the summary is the body (it already carries the task, decisions,
// discoveries and failures), and up to diaryMaxFiles files it names become
// the record's files. A summary with nothing in it writes nothing. The caller
// runs this in the background, so a slow or failed write never delays the
// turn that compacted.
func (s *Stores) SaveCompaction(summary, workdir string) (Record, bool, error) {
	text := strings.TrimSpace(summary)
	if text == "" {
		return Record{}, false, nil
	}
	db, err := s.writeDB()
	if err != nil {
		return Record{}, false, err
	}
	const prefix = "Session diary: "
	title := prefix + trimRunes(firstLine(text), diaryTitleChars-len(prefix))
	return db.Add(Record{
		Category: CatFact,
		Title:    title,
		Body:     trimRunes(text, diaryBodyChars),
		Files:    filesMentioned(text, workdir, diaryMaxFiles),
		Tags:     []string{"diary", "compaction"},
		Source:   SourceAgent,
	})
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "compaction summary"
	}
	return line
}
