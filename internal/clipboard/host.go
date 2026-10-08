package clipboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// HostBinary is the lectern binary to run on the machine ex drives: localBin
// for this machine, the target's own lectern otherwise, "" when it has none.
func HostBinary(ex executor.Executor, localBin string) string {
	if _, local := ex.(*executor.Local); local {
		return localBin
	}
	return executor.TargetEnvOf(ex).Lectern
}

// HostEnv is what a session on a machine needs to reach its clipboard.
type HostEnv struct {
	Display, Xauthority string
	// Shims is the directory of wl-paste/xclip shims to put first on PATH.
	Shims string
}

// Prepare installs the shims on the machine ex drives and starts (or finds)
// its headless clipboard, the way the other target-side helpers are run:
// through the lectern binary there. A machine without Xvfb still gets the
// shims; a machine without a lectern binary gets nothing.
func Prepare(ctx context.Context, ex executor.Executor, localBin string) (HostEnv, error) {
	bin := HostBinary(ex, localBin)
	if bin == "" {
		return HostEnv{}, errors.New("no lectern binary on this machine")
	}
	r, err := ex.Run(ctx, shellq.Quote(bin)+" clipboard ensure --bin "+shellq.Quote(bin), executor.RunOpts{Timeout: 20})
	if err != nil {
		return HostEnv{}, err
	}
	var e HostEnv
	for _, l := range strings.Split(r.Stdout, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		switch k {
		case "DISPLAY":
			e.Display = v
		case "XAUTHORITY":
			e.Xauthority = v
		case "SHIMS":
			e.Shims = v
		}
	}
	if e.Shims == "" && e.Display == "" {
		return e, errors.New(strings.TrimSpace(r.Stderr + " " + r.Stdout))
	}
	return e, nil
}

// EnvAssignments renders e as assignments for a sourced env file; PATH
// expands the existing PATH when the file is sourced.
func (e HostEnv) EnvAssignments() string {
	var b strings.Builder
	if e.Display != "" {
		b.WriteString("DISPLAY=" + shellq.Quote(e.Display) + " ")
		if e.Xauthority != "" {
			b.WriteString("XAUTHORITY=" + shellq.Quote(e.Xauthority) + " ")
		}
	}
	if e.Shims != "" {
		b.WriteString("PATH=" + shellq.Quote(e.Shims) + ":\"$PATH\" ")
	}
	return b.String()
}

// Mirror puts an item on the headless clipboard of the machine ex drives.
func Mirror(ctx context.Context, ex executor.Executor, localBin, mime string, data []byte) error {
	bin := HostBinary(ex, localBin)
	if bin == "" {
		return errors.New("no lectern binary on this machine")
	}
	if _, err := Prepare(ctx, ex, localBin); err != nil {
		return err
	}
	var rnd [8]byte
	rand.Read(rnd[:])
	tmp := "/tmp/.lectern-clip-" + hex.EncodeToString(rnd[:])
	if err := ex.WriteFile(ctx, tmp, data); err != nil {
		return err
	}
	r, err := ex.Run(ctx, shellq.Quote(bin)+" clipboard set --type "+shellq.Quote(mime)+" --file "+shellq.Quote(tmp)+" --remove; rc=$?; rm -f "+shellq.Quote(tmp)+"; exit $rc", executor.RunOpts{Timeout: 20})
	if err != nil {
		return err
	}
	if !r.OK() {
		return errors.New(strings.TrimSpace(r.Stderr))
	}
	return nil
}

// Clear empties the headless clipboard.
func Clear(ctx context.Context, ex executor.Executor, localBin string) error {
	bin := HostBinary(ex, localBin)
	if bin == "" {
		return errors.New("no lectern binary on this machine")
	}
	_, err := ex.Run(ctx, shellq.Quote(bin)+" clipboard set --clear", executor.RunOpts{Timeout: 10})
	return err
}
