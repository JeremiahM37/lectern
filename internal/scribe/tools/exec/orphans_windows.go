//go:build windows

package exec

// Orphan records are not kept on Windows: jobs there are not reaped after a
// crash.
var selfStartTicks uint64

func procStartTicks(pid int) uint64 { return 0 }

func persistRecord(dir string, jobs []jobRecord) {}

func reapOrphans(dir string) {}
