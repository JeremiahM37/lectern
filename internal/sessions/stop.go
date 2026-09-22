package sessions

import (
	"context"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/shellq"
	"github.com/JeremiahM37/lectern/internal/store"
)

// stopProcess identifies the original process and requires a complete remote
// observation of its absence. A successful command alone does not prove a stop.
func (m *Manager) stopProcess(ctx context.Context, ex executor.Executor, row *store.Session) error {
	if row.TmuxSession == "" {
		return fmt.Errorf("session has no terminal to check")
	}
	check := func() (PollCapture, error) {
		names := []string{row.TmuxSession}
		r, err := ex.Run(ctx, PollCommand(names), executor.RunOpts{Timeout: 20})
		if err != nil || !r.OK() {
			return PollCapture{}, fmt.Errorf("could not check whether the terminal stopped")
		}
		panes, complete := ParsePollSnapshot(r.Stdout, names)
		if !complete || panes[row.TmuxSession].Failed {
			return PollCapture{}, fmt.Errorf("incomplete terminal check; retry when the target is reachable")
		}
		return panes[row.TmuxSession], nil
	}
	before, err := check()
	if err != nil {
		return err
	}
	if before.Missing {
		return nil
	}
	r, err := ex.Run(ctx, trackingIdentityCommand(row.TmuxSession, ""), executor.RunOpts{Timeout: 10})
	if err != nil || !r.OK() {
		return fmt.Errorf("could not identify the terminal to stop")
	}
	identity := strings.TrimSpace(r.Stdout)
	if identity == "" && row.TrackingIdentity == "" && row.EndedAt == nil {
		identity = captureTrackingIdentity(ctx, ex, row.TmuxSession)
	}
	if !validTrackingIdentity(identity) || (row.TrackingIdentity != "" && identity != row.TrackingIdentity) || (row.EndedAt != nil && row.TrackingIdentity == "") {
		return fmt.Errorf("the original terminal could not be identified; track the current terminal before stopping it")
	}
	if row.TrackingIdentity == "" {
		// Retain identity even when the subsequent stop is refused, so a retry cannot
		// silently adopt a replacement process with the same tmux name.
		result, err := m.DB.Exec("UPDATE sessions SET tracking_identity=? WHERE id=? AND tracking_identity='' AND target_id=? AND tmux_session=? AND ended_at IS NULL", identity, row.ID, row.TargetID, row.TmuxSession)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return fmt.Errorf("session changed before stop; refresh before retrying")
		}
	}
	command := "tmux if-shell -F -t " + shellq.Quote("="+row.TmuxSession+":") + " " + shellq.Quote("#{==:#{"+trackingOption+"},"+identity+"}") + " " + shellq.Quote("kill-session -t "+shellq.Quote("="+row.TmuxSession))
	r, err = ex.Run(ctx, command, executor.RunOpts{Timeout: 20})
	if err != nil || !r.OK() {
		return fmt.Errorf("terminal stop failed; the session remains tracked")
	}
	after, err := check()
	if err != nil {
		return err
	}
	if !after.Missing {
		return fmt.Errorf("terminal did not stop; the session remains tracked")
	}
	return nil
}
