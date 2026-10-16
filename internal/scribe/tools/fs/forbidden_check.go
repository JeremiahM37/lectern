package fs

import (
	"path/filepath"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// CheckForbidden applies the forbidden-path rules to abs for a caller that
// touches a file without going through this package's tools, such as the
// editor. It returns the same refusal the file tools give, or nil.
func CheckForbidden(env *tools.Env, abs string) error {
	real := abs
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		real = r
	}
	rel := abs
	if env != nil && env.Workdir != "" {
		if r, err := filepath.Rel(env.Workdir, abs); err == nil {
			rel = filepath.ToSlash(r)
		}
	}
	return checkForbidden(env, target{abs: abs, rel: rel, real: real})
}
