package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/redact"
)

// HistoryCap is the most prompts kept in history.jsonl. The file is pruned
// back to HistoryCap once it holds HistoryPruneSlack more, so the rewrite
// happens about once every fifty prompts rather than on every one.
const (
	HistoryCap        = 5000
	HistoryPruneSlack = 50
)

// HistoryEntry is one submitted prompt.
type HistoryEntry struct {
	Text string    `json:"text"`
	CWD  string    `json:"cwd"`
	At   time.Time `json:"at"`
}

var historyMu sync.Mutex

func historyPath() string { return filepath.Join(config.Home(), "history.jsonl") }

// readHistory returns every entry, oldest first. Unparseable lines are skipped.
func readHistory() ([]HistoryEntry, error) {
	var out []HistoryEntry
	err := readLines(historyPath(), func(line []byte) {
		var e HistoryEntry
		if json.Unmarshal(line, &e) == nil && e.Text != "" {
			out = append(out, e)
		}
	})
	return out, err
}

// AppendHistory records a submitted prompt made in cwd. Blank prompts are
// ignored. A prompt identical to the most recent one is not added again. A
// prompt that was submitted before is moved to the top: its older copies are
// removed and it is appended again, so each text appears once. When the file
// grows past HistoryCap+HistoryPruneSlack, the oldest entries are dropped.
func AppendHistory(text, cwd string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	// A pasted key must not be kept for up-arrow recall or for search.
	text = redact.Default().Redact(text)
	historyMu.Lock()
	defer historyMu.Unlock()
	entries, err := readHistory()
	if err != nil {
		return err
	}
	if n := len(entries); n > 0 && entries[n-1].Text == text {
		return nil
	}
	e := HistoryEntry{Text: text, CWD: cwd, At: time.Now().UTC()}
	kept := make([]HistoryEntry, 0, len(entries)+1)
	for _, old := range entries {
		if old.Text != text {
			kept = append(kept, old)
		}
	}
	repeated := len(kept) != len(entries)
	entries = append(kept, e)
	if repeated || len(entries) > HistoryCap+HistoryPruneSlack {
		// Rewrite the file: a repeat was removed, or the cap was reached.
		if len(entries) > HistoryCap {
			entries = entries[len(entries)-HistoryCap:]
		}
		var buf bytes.Buffer
		for _, x := range entries {
			b, err := json.Marshal(x)
			if err != nil {
				return err
			}
			buf.Write(b)
			buf.WriteByte('\n')
		}
		return atomicWrite(historyPath(), buf.Bytes(), 0o600)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(historyPath()), 0o700); err != nil {
		return err
	}
	if err := repairTail(historyPath()); err != nil {
		return err
	}
	f, err := os.OpenFile(historyPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Recent returns up to n prompts, newest first. An empty cwd means every
// project; n <= 0 means no limit.
func Recent(cwd string, n int) ([]HistoryEntry, error) {
	historyMu.Lock()
	all, err := readHistory()
	historyMu.Unlock()
	if err != nil {
		return nil, err
	}
	var out []HistoryEntry
	for i := len(all) - 1; i >= 0; i-- {
		if cwd != "" && all[i].CWD != cwd {
			continue
		}
		out = append(out, all[i])
		if n > 0 && len(out) == n {
			break
		}
	}
	return out, nil
}

// Search ranks prompts against query for Ctrl+R. Query characters must
// appear in order (case-insensitive subsequence). Matches are scored, with
// bonuses for substrings, word starts and consecutive characters, and a
// penalty for gaps. Ties go to the newer prompt. Duplicate texts are shown once.
// limit <= 0 means no limit. An empty query returns recent prompts.
func Search(query string, limit int) ([]HistoryEntry, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return Recent("", limit)
	}
	historyMu.Lock()
	all, err := readHistory()
	historyMu.Unlock()
	if err != nil {
		return nil, err
	}
	type scored struct {
		e     HistoryEntry
		score int
		idx   int
	}
	var hits []scored
	for i := len(all) - 1; i >= 0; i-- {
		if s, ok := fuzzyScore(q, all[i].Text); ok {
			hits = append(hits, scored{all[i], s, len(all) - i})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].idx < hits[j].idx
	})
	seen := map[string]bool{}
	var out []HistoryEntry
	for _, h := range hits {
		if seen[h.e.Text] {
			continue
		}
		seen[h.e.Text] = true
		out = append(out, h.e)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// fuzzyScore reports whether pattern (already lower-case) is a subsequence of
// text, and how well it matches.
func fuzzyScore(pattern, text string) (int, bool) {
	t := []rune(strings.ToLower(text))
	p := []rune(pattern)
	score := 0
	if strings.Contains(string(t), pattern) {
		score += 100
	}
	pi := 0
	prev := -2
	first := -1
	for ti := 0; ti < len(t) && pi < len(p); ti++ {
		if t[ti] != p[pi] {
			continue
		}
		score += 5
		if ti == 0 || isWordBreak(t[ti-1]) {
			score += 3
		}
		if ti == prev+1 {
			score += 2
		}
		if first < 0 {
			first = ti
		} else if gap := ti - prev - 1; gap > 0 {
			score -= gap
		}
		prev = ti
		pi++
	}
	if pi < len(p) {
		return 0, false
	}
	// Shorter texts that match equally well rank higher.
	score -= len(t) / 20
	return score, true
}

func isWordBreak(r rune) bool {
	return unicode.IsSpace(r) || r == '/' || r == '-' || r == '_' || r == '.'
}
