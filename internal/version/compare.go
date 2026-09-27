package version

import (
	"strconv"
	"strings"
	"time"
)

// Older reports whether build a is older than build b. known is false when
// the two cannot be ordered — equal versions with no commit times to tell
// the builds apart, or a version string that is not major.minor.patch.
func Older(a, b Info) (older, known bool) {
	if c, ok := compareStrings(a.Version, b.Version); ok && c != 0 {
		return c < 0, true
	} else if !ok {
		return false, false
	}
	at, aerr := time.Parse(time.RFC3339, a.CommitTime)
	bt, berr := time.Parse(time.RFC3339, b.CommitTime)
	if aerr != nil || berr != nil {
		return false, false
	}
	return at.Before(bt), true
}

// compareStrings orders two release strings by major.minor.patch, then a
// pre-release ("-dev") before the release itself. Build metadata after "+"
// is ignored: it names a commit, not an order.
func compareStrings(a, b string) (int, bool) {
	ac, apre, aok := parse(a)
	bc, bpre, bok := parse(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range ac {
		if ac[i] != bc[i] {
			if ac[i] < bc[i] {
				return -1, true
			}
			return 1, true
		}
	}
	switch {
	case apre == bpre:
		return 0, true
	case apre == "":
		return 1, true
	case bpre == "":
		return -1, true
	case apre < bpre:
		return -1, true
	default:
		return 1, true
	}
}

func parse(v string) (core [3]int, pre string, ok bool) {
	v, _, _ = strings.Cut(strings.TrimPrefix(strings.TrimSpace(v), "v"), "+")
	v, pre, _ = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return core, "", false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return core, "", false
		}
		core[i] = n
	}
	return core, pre, true
}
