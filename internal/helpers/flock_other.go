//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package helpers

import "os"

// lockFile is a no-op where flock does not exist; codexTrust's Python has no
// lock there either (fcntl is POSIX-only), so nothing is lost.
func lockFile(*os.File) error { return nil }
