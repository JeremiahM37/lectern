package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// EnsureGlobal writes the default global config when GlobalPath does not
// exist yet, and reports whether it wrote one. It never changes an existing
// file, including an empty one: a user who removed every key still owns that
// file. Startup calls it and treats an error as a warning, so a read-only
// home still lets Scribe run on built-in defaults.
func EnsureGlobal() (bool, error) {
	path := GlobalPath()
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	body, err := defaultGlobalJSON()
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	return createNew(path, body)
}

// defaultGlobalJSON is the content of a new global config. It names the
// defaults a user is likely to look for and edit. Everything else stays out
// of the file, so an unset key keeps tracking the code's default. Agent
// features are left out on purpose: a key written here counts as the user's
// choice, so writing all of them made the scope picker treat every feature
// as set globally and copy them all into a project on a scope switch.
func defaultGlobalJSON() ([]byte, error) {
	doc := struct {
		Editor      EditorSettings `json:"editor"`
		ToolTimeout float64        `json:"tool_timeout"`
	}{
		Editor:      EditorSettings{Command: DefaultEditorCommand},
		ToolTimeout: DefaultToolTimeout,
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// createNew writes data to a new file at path. It reports false, and leaves
// path alone, when something already exists there. The data goes to a
// temporary file in the same directory first, then is hard-linked into place:
// link fails if the name is taken, which makes the create exclusive, and a
// crash mid-write cannot leave a half-written config that would stop the next
// start. CreateTemp makes the file 0600.
func createNew(path string, data []byte) (bool, error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
