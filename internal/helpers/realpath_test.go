package helpers

import (
	"os"
	"testing"
)

func TestRealpathParity(t *testing.T) {
	script := "import os,sys;print(os.path.realpath(sys.argv[1]))"
	root := t.TempDir()
	os.MkdirAll(root+"/real/sub", 0o755)
	os.Symlink(root+"/real", root+"/link")
	os.Symlink("loop", root+"/loop")
	for _, p := range []string{root + "/link/sub", root + "/link/missing/x", root + "/loop/x", "relative/../x", "/", "//x", root + "/link/.."} {
		assertParity(t, runSpec{dir: root}, script, "realpath", p)
	}
	assertParity(t, runSpec{dir: root}, script, "realpath")
}
