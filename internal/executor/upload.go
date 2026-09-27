package executor

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"strings"
)

func streamFileCommand(file string) string {
	return fmt.Sprintf("umask 077; mkdir -p %s && cat > %s", ShellQuote(path.Dir(file)), ShellQuote(file))
}

func fileWriteResult(r Result, err error) error {
	if err != nil {
		return err
	}
	if !r.OK() {
		return Errf("file transfer failed (exit %d): %s", r.RC, strings.TrimSpace(r.Stderr))
	}
	return nil
}

// Bound command size even after wrapper encoding, and propagate remote disk /
// permission errors. No document bytes are interpolated as shell syntax.
func writeFileChunks(ctx context.Context, run func(context.Context, string, RunOpts) (Result, error), file string, data []byte, size int) error {
	r, err := run(ctx, fmt.Sprintf("umask 077; mkdir -p %s && : > %s", ShellQuote(path.Dir(file)), ShellQuote(file)), RunOpts{Timeout: 120})
	if err := fileWriteResult(r, err); err != nil {
		return err
	}
	for offset := 0; offset < len(data); offset += size {
		end := min(offset+size, len(data))
		cmd := fmt.Sprintf("printf '%%s' %s | base64 -d >> %s", base64.StdEncoding.EncodeToString(data[offset:end]), ShellQuote(file))
		r, err := run(ctx, cmd, RunOpts{Timeout: 120})
		if err := fileWriteResult(r, err); err != nil {
			return err
		}
	}
	return nil
}

// WriteFileChunks is writeFileChunks for executors outside this package (a
// sandbox provider's), whose command path may not pass stdin through.
func WriteFileChunks(ctx context.Context, run func(context.Context, string, RunOpts) (Result, error), file string, data []byte, size int) error {
	return writeFileChunks(ctx, run, file, data, size)
}
