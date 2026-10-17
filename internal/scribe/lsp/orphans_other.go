//go:build !unix

package lsp

// Server records are not kept where process start times cannot be read, so
// nothing is reaped there.
var selfStartTicks uint64

func procStartTicks(pid int) uint64 { return 0 }

func reapOrphans(dir string) {}
