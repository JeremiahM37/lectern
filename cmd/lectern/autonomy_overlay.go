package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

// autonomyOverlayCommand is a local file utility for the trusted runner. It
// never loads the server database or dispatches work. Exit 2 means the submitted
// tree violates the documentary policy; exit 1 means an operational failure.
func autonomyOverlayCommand(command string, args []string, stdout, stderr io.Writer) int {
	var result any
	var err error
	switch command {
	case "autonomy-overlay":
		if len(args) != 3 {
			fmt.Fprintln(stderr, "usage: lectern autonomy-overlay BASELINE CANDIDATE OUTPUT")
			return 1
		}
		result, err = autonomy.ReconstructCompletionOverlay(args[0], args[1], args[2])
	case "autonomy-overlay-inspect":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: lectern autonomy-overlay-inspect ROOT")
			return 1
		}
		var digest string
		digest, err = autonomy.InspectCompletionTree(args[0])
		result = struct {
			TreeSHA256 string `json:"tree_sha256"`
		}{digest}
	default:
		fmt.Fprintln(stderr, "unknown completion utility")
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			return 1
		}
		return 2
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
