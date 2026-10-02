//go:build !windows

package main

import (
	"os"

	"github.com/muesli/cancelreader"
)

// newInputReader reads this terminal's keys in a way that can be stopped
// while another program (the controls popup, a pager) has the terminal.
func newInputReader(f *os.File) (cancelreader.CancelReader, error) {
	return cancelreader.NewReader(f)
}
