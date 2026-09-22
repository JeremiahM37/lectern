package sessions

import (
	"context"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

const StatusInterrupted = "interrupted"

func (m *Manager) recoverAfterBoot(ctx context.Context, target *store.Target, ex executor.Executor, rows []*store.Session, boot string) {
	if boot == "" {
		return
	}
	for _, row := range rows {
		// setup_state=ready is a completed, durable workspace setup and must be
		// recoverable. Only an in-flight setup is excluded; it has its own
		// recovery path and launching here could duplicate its worktree.
		if row.EndedAt != nil || row.ArchivedAt != nil || row.Origin != "lectern" || row.SetupState == "creating" || row.BootID == "" || row.BootID == boot || row.Status == StatusInterrupted {
			continue
		}
		r, err := ex.Run(ctx, PollCommand([]string{row.TmuxSession}), executor.RunOpts{Timeout: 10})
		if err != nil || !r.OK() {
			continue
		}
		panes, complete := ParsePollSnapshot(r.Stdout, []string{row.TmuxSession})
		if !complete {
			continue
		}
		pane, found := panes[row.TmuxSession]
		if !found || pane.Failed || !pane.Missing {
			continue
		}
		if row.NativeRecoveryCID == "" {
			_ = m.DB.Update("sessions", row.ID, map[string]any{"status": StatusInterrupted, "updated_at": store.Now()})
			continue
		}
		// Serialize the claim through the same lifecycle gate used by Kill and
		// Release. Holding it through launch means an operator stop cannot win
		// after the old row is claimed but before its replacement exists.
		m.lifecycleMu.Lock()
		// Advance the boot checkpoint before launching, preventing repeated
		// attempts if the target rejects the launch.
		res, err := m.DB.Exec("UPDATE sessions SET boot_id=?, status=? WHERE id=? AND ended_at IS NULL AND boot_id=?", boot, StatusStarting, row.ID, row.BootID)
		if err != nil {
			m.lifecycleMu.Unlock()
			continue
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			// Stop/release/archive won the race. Never launch a successor for a
			// row whose lifecycle changed while the target was being probed.
			m.lifecycleMu.Unlock()
			continue
		}
		// The worker observing the pre-reboot process must not occupy the
		// per-session slot while the replacement process is being launched.
		m.stopNativeCheckpoint(row.ID)
		config, err := m.SessionLaunchConfiguration(row)
		if err != nil {
			_ = m.DB.Update("sessions", row.ID, map[string]any{"status": StatusInterrupted, "ended_at": nil, "updated_at": store.Now()})
			m.lifecycleMu.Unlock()
			continue
		}
		// lifecycleMu is held from the atomic claim through the target launch.
		// Calling the public wrapper here would deadlock; the unlocked body is
		// deliberately private to this recovery path.
		_, err = m.launch(ctx, LaunchOpts{ReservedID: row.ID, Configuration: config, TargetID: target.ID, ProjectID: row.ProjectID, GroupPath: row.GroupPath, Name: row.Name, Agent: row.Agent, Model: row.Model, Workdir: row.Workdir, ResumeID: row.ResumeID, RecoveryCID: row.NativeRecoveryCID})
		if err != nil {
			_ = m.DB.Update("sessions", row.ID, map[string]any{"status": StatusInterrupted, "ended_at": nil, "updated_at": time.Now().UnixNano() / 1e9})
		}
		m.lifecycleMu.Unlock()
	}
}
