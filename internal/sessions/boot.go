package sessions

import (
	"context"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

var linuxBootID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ProbeBootID is Linux-only and deliberately returns unknown on any error.
func ProbeBootID(ctx context.Context, ex executor.Executor) (string, bool) {
	r, err := ex.Run(ctx, "cat /proc/sys/kernel/random/boot_id", executor.RunOpts{Timeout: 5})
	id := strings.TrimSpace(r.Stdout)
	if err != nil || !r.OK() || !linuxBootID.MatchString(id) {
		return "", false
	}
	return id, true
}
