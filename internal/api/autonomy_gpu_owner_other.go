//go:build !unix

package api

import "os"

// The GPU qualification evidence is a Unix root-owned file. Other platforms
// deliberately fail closed because they cannot establish that ownership.
func autoGPUOwnedByRoot(os.FileInfo) bool { return false }
