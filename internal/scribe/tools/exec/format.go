package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNoFormatter means the project has no format command to run on a file.
var ErrNoFormatter = errors.New("no format command detected for this project")

// formatWords are command words that name the whole project; a file-scoped run
// drops them so only the one file is rewritten.
var formatWords = map[string]bool{".": true, "./...": true, "./": true}

// FormatFile runs the project's format command on one file, bounded by
// timeout, and returns once it has finished. Output is discarded; callers
// compare the file before and after to see what changed. A failing formatter
// is not an error here: the file is simply left as it was.
func FormatFile(ctx context.Context, dir, abs string, timeout time.Duration) error {
	cmd := Detect(dir).Commands["format"]
	if cmd == "" {
		return ErrNoFormatter
	}
	var words []string
	for _, w := range strings.Fields(cmd) {
		if !formatWords[w] {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return ErrNoFormatter
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil {
		rel = abs
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	line := strings.Join(words, " ") + " " + shellQuote(filepath.ToSlash(rel))
	res := runProcess(tctx, procSpec{
		argv: shellArgv(line),
		dir:  dir,
		env:  append(ScrubEnv(os.Environ()), "FORCE_COLOR=0", "NO_COLOR=1"),
	})
	if res.err != nil {
		return res.err
	}
	return nil
}
