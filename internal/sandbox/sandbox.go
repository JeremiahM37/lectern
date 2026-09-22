// Package sandbox owns the ephemeral LXC lifecycle: clone template, start, run,
// destroy.
//
// Target kind 'sandbox' puts the Proxmox TEMPLATE vmid in `host`. Every attempt
// gets its own linked clone; the container IS the isolation, so no git worktree
// is needed and bypassPermissions is safe. Host-side pct commands run through
// the target's (local) executor so mock mode can script the whole lifecycle.
package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/internal/executor"
)

// Provision clones the template, starts it, and waits for it to answer.
func Provision(ctx context.Context, host executor.Executor, templateVMID string, attemptID int64, log *slog.Logger) (string, error) {
	r, err := host.Run(ctx, "sudo pvesh get /cluster/nextid", executor.RunOpts{Timeout: 30})
	if err != nil {
		return "", err
	}
	vmid := strings.TrimSpace(r.Stdout)
	if !r.OK() || vmid == "" {
		return "", executor.Errf("could not allocate vmid: %s", strings.TrimSpace(r.Stderr))
	}
	cloneCmd := fmt.Sprintf("sudo pct clone %s %s --hostname lec-sb-%d",
		templateVMID, vmid, attemptID)
	if r, err := host.Run(ctx, cloneCmd, executor.RunOpts{Timeout: 300}); err != nil {
		return "", err
	} else if !r.OK() {
		return "", executor.Errf("pct clone failed: %s", strings.TrimSpace(r.Stderr))
	}
	if r, err := host.Run(ctx, "sudo pct start "+vmid, executor.RunOpts{Timeout: 120}); err != nil {
		Destroy(ctx, host, vmid, log)
		return "", err
	} else if !r.OK() {
		Destroy(ctx, host, vmid, log)
		return "", executor.Errf("pct start failed: %s", strings.TrimSpace(r.Stderr))
	}
	ready := false
	for i := 0; i < 30; i++ {
		if r, err := host.Run(ctx, "sudo pct exec "+vmid+" -- true",
			executor.RunOpts{Timeout: 20}); err == nil && r.OK() {
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			Destroy(ctx, host, vmid, log)
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if !ready {
		Destroy(ctx, host, vmid, log)
		return "", executor.Errf("sandbox %s never became ready", vmid)
	}
	// NB: auth is provisioned by the scheduler right before launch, via the
	// attempt's own executor, so it shares one credentials code path and works
	// under the mock executor in tests.
	log.Info("sandbox provisioned", "vmid", vmid, "template", templateVMID)
	return vmid, nil
}

// Destroy stops and removes a container, best-effort.
func Destroy(ctx context.Context, host executor.Executor, vmid string, log *slog.Logger) {
	_, _ = host.Run(ctx, "sudo pct stop "+vmid+" 2>/dev/null || true", executor.RunOpts{Timeout: 120})
	r, err := host.Run(ctx, "sudo pct destroy "+vmid, executor.RunOpts{Timeout: 120})
	if err == nil && r.OK() {
		log.Info("sandbox destroyed", "vmid", vmid)
		return
	}
	log.Warn("sandbox destroy failed", "vmid", vmid, "err", err)
}

// IsRepoURL reports whether a project's repo_path must be cloned rather than
// being a path already baked into the template.
func IsRepoURL(repoPath string) bool {
	for _, p := range []string{"http://", "https://", "git@", "ssh://"} {
		if strings.HasPrefix(repoPath, p) {
			return true
		}
	}
	return false
}
