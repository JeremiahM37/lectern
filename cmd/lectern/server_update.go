package main

// The server's side of "Update and restart" (internal/api/self_update.go):
// the same updater `lectern update` uses, a check that the running program
// can be replaced from here, and the restart that execs the new program in
// place. Sessions run in their own tmux (or PTY host) and survive it, as they
// survive any restart of the server.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// restartSessionsEnv tells a server started by an update to restart every
// session once it is up. It lives only in the exec'd process's environment.
const restartSessionsEnv = "LECTERN_RESTART_SESSIONS_AFTER_UPDATE"

type serverUpdater struct{ u *updater }

func newServerUpdater() (*serverUpdater, error) {
	u, err := newUpdater(io.Discard)
	if err != nil {
		return nil, err
	}
	return &serverUpdater{u: u}, nil
}

func (s *serverUpdater) Latest(ctx context.Context) (string, error) { return s.u.latest(ctx) }

func (s *serverUpdater) Install(ctx context.Context, tag string) error { return s.u.install(ctx, tag) }

// Blocked says why the running program cannot be replaced by this process.
func (s *serverUpdater) Blocked() string {
	if tool, command := managedBy(s.u.exe); tool != "" {
		return "this Lectern was installed with " + tool + "; update it with: " + command
	}
	if runtime.GOOS == "windows" {
		return "update this Lectern with: lectern update, then restart it"
	}
	// Replacing the file is a rename in its folder, so the folder must be
	// writable by the server's user.
	probe, err := os.CreateTemp(filepath.Dir(s.u.exe), ".lectern-update-probe-*")
	if err != nil {
		return "the server cannot replace " + s.u.exe + " (its folder is not writable by this user); update it with: sudo lectern update, then restart the service"
	}
	probe.Close()
	_ = os.Remove(probe.Name())
	return ""
}

// restartInPlace replaces this process with the program now at exe, keeping
// its PID, arguments and environment, so a service manager sees no exit.
func restartInPlace(exe string, restartSessions bool, log *slog.Logger) {
	env := os.Environ()
	if restartSessions {
		env = append(env, restartSessionsEnv+"=1")
	}
	err := syscall.Exec(exe, os.Args, env)
	// Exec only returns on failure. Exiting non-zero lets a service manager
	// (Restart=on-failure) start the new program instead.
	log.Error("could not restart into the updated program; exiting so the service manager restarts it", "err", err)
	os.Exit(1)
}
