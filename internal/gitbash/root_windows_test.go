//go:build windows

package gitbash

import "testing"

func TestInstallRoot(t *testing.T) {
	for _, bash := range []string{`C:\Program Files\Git\bin\bash.exe`, `C:\Program Files\Git\usr\bin\bash.exe`} {
		if got := installRoot(bash); got != `C:\Program Files\Git` {
			t.Errorf("installRoot(%q) = %q", bash, got)
		}
	}
}
