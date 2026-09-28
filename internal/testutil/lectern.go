package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var lectern struct {
	once sync.Once
	path string
	err  error
	out  []byte
}

// LecternBinary builds cmd/lectern once per test process, for tests that run
// target-side helpers (`lectern helper NAME …`) the way a target does.
func LecternBinary(t *testing.T) string {
	t.Helper()
	lectern.once.Do(func() {
		var gomod []byte
		if gomod, lectern.err = exec.Command("go", "env", "GOMOD").Output(); lectern.err != nil {
			return
		}
		dir, err := os.MkdirTemp("", "lectern-helper-bin-")
		if err != nil {
			lectern.err = err
			return
		}
		lectern.path = filepath.Join(dir, "lectern")
		cmd := exec.Command("go", "build", "-o", lectern.path, "./cmd/lectern")
		cmd.Dir = filepath.Dir(strings.TrimSpace(string(gomod)))
		lectern.out, lectern.err = cmd.CombinedOutput()
	})
	if lectern.err != nil {
		t.Fatalf("build lectern: %v: %s", lectern.err, lectern.out)
	}
	return lectern.path
}
