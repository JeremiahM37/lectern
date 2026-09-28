package helpers

// realpath is os.path.realpath(argv[1]) printed: the non-strict form, which
// resolves the part of a path that exists and keeps the rest, so a
// workspace allocation that is not (or no longer) there still canonicalises.
//
//	lectern helper realpath PATH

import (
	"errors"
	"fmt"
	"io"
)

func init() { Register("realpath", realpathHelper) }

func realpathHelper(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		return pyUncaught(stderr, "IndexError", errors.New("list index out of range"))
	}
	fmt.Fprintln(stdout, pyRealpath(args[0]))
	return 0
}
