package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// A subagent report longer than archiveAbove bytes is shown inline as its
// first archiveHead and last archiveTail bytes. The whole text is saved to the
// archive directory and the inline copy names the file (spec 03 section 3.6).
const (
	archiveAbove = 4000
	archiveHead  = 2000
	archiveTail  = 1000
)

// archiveLong returns the inline form of report. name is the file name the
// full text is saved under, without directory. An empty dir keeps the cut but
// saves nothing, and the note says so.
func archiveLong(report, dir, name string) string {
	if len(report) <= archiveAbove {
		return report
	}
	headEnd := archiveHead
	for headEnd > 0 && !utf8.RuneStart(report[headEnd]) {
		headEnd--
	}
	head := report[:headEnd]
	tailStart := len(report) - archiveTail
	for tailStart < len(report) && !utf8.RuneStart(report[tailStart]) {
		tailStart++
	}
	tail := report[tailStart:]
	omitted := tailStart - headEnd

	where := "the full text was not saved: no archive directory is set"
	if dir != "" {
		if path, err := writeArchive(dir, name, report); err != nil {
			where = "the full text could not be saved: " + err.Error()
		} else {
			where = "full output: " + path
		}
	}
	return head + fmt.Sprintf("\n\n[... %d bytes omitted, %s ...]\n\n", omitted, where) + tail
}

// writeArchive saves text under dir as name.txt and returns the path.
func writeArchive(dir, name, text string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, archiveName(name)+".txt")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// archiveName keeps letters, digits, dots, dashes and underscores of name, so
// a call id cannot escape the archive directory.
func archiveName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "report"
	}
	return b.String()
}

// archiveReports applies archiveLong to every task report of a dispatch. It
// runs after the tasks finish, so dependency reports were built from the full
// text.
func archiveReports(results []taskResult, ids []string, dir, callID string) {
	for i := range results {
		if len(results[i].report) <= archiveAbove {
			continue
		}
		results[i].report = archiveLong(results[i].report, dir, callID+"-"+ids[i])
	}
}
