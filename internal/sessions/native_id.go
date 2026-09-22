package sessions

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/nativeidentity"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// CaptureNativeID invokes the shared identity implementation with the
// configured native home explicitly. An empty result is inconclusive.
func CaptureNativeID(ctx context.Context, ex executor.Executor, agent, workdir, home, tmuxName, tracking string) string {
	args := make([]string, 0, 4)
	for _, v := range []string{agent, workdir, home, tmuxName, tracking} {
		b, _ := json.Marshal(v)
		args = append(args, string(b))
	}
	// native_identity validates the transcript header with native_metadata;
	// embed the shared decoder in the same isolated invocation so capture does
	// not depend on files/modules installed on the target.
	cmd := "python3 -c " + shellq.Quote(nativeidentity.RecordsScript+"\n"+nativeidentity.IdentityScript+"\nimport json\nprint(json.dumps(native_identity("+strings.Join(args, ",")+")))")
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 10})
	if err != nil || !r.OK() {
		return ""
	}
	var out struct{ State, ID string }
	if json.Unmarshal([]byte(strings.TrimSpace(r.Stdout)), &out) != nil || out.State != "identified" {
		return ""
	}
	return out.ID
}

func (m *Manager) checkpointNativeID(sessionID int64, agent, workdir, home, tmux string) {
	// Kept as a small worker for compatibility with callers that already have a
	// resolved launch identity. New callers should use startNativeCheckpoint so
	// a restarted App cannot create a second worker for the same row.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.runNativeCheckpoint(ctx, sessionID, agent, workdir, home, tmux)
}

func nativeHome(spec Spec, agent string) string {
	if agent == "codex" {
		return spec.Env["CODEX_HOME"]
	}
	if agent == "claude" {
		return spec.Env["CLAUDE_CONFIG_DIR"]
	}
	return ""
}

// startNativeCheckpoint installs at most one refresh worker per session. It is
// safe to call from every poll tick, which is necessary for sessions that were
// already alive when the control plane restarted.
func (m *Manager) startNativeCheckpoint(sessionID int64, agent, workdir, home, tmux string) {
	m.checkpointMu.Lock()
	if m.checkpointClosed {
		m.checkpointMu.Unlock()
		return
	}
	if m.checkpoints == nil {
		m.checkpoints = map[int64]context.CancelFunc{}
	}
	if m.checkpointGeneration == nil {
		m.checkpointGeneration = map[int64]uint64{}
	}
	if _, ok := m.checkpoints[sessionID]; ok {
		m.checkpointMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.checkpointGeneration[sessionID]++
	generation := m.checkpointGeneration[sessionID]
	m.checkpoints[sessionID] = cancel
	m.checkpointWG.Add(1)
	m.checkpointMu.Unlock()
	go func() {
		defer m.checkpointWG.Done()
		m.runNativeCheckpoint(ctx, sessionID, agent, workdir, home, tmux)
		m.checkpointMu.Lock()
		if current, ok := m.checkpoints[sessionID]; ok && current != nil && m.checkpointGeneration[sessionID] == generation {
			delete(m.checkpoints, sessionID)
		}
		m.checkpointMu.Unlock()
	}()
}

func (m *Manager) stopNativeCheckpoint(sessionID int64) {
	m.checkpointMu.Lock()
	if cancel, ok := m.checkpoints[sessionID]; ok {
		cancel()
		delete(m.checkpoints, sessionID)
	}
	m.checkpointMu.Unlock()
}

// RestartNativeCheckpoint changes only Lectern's read-only observer after a
// promotion; the user's existing process and tmux terminal remain untouched.
func (m *Manager) RestartNativeCheckpoint(sessionID int64, agent, workdir, home, tmux string) {
	m.stopNativeCheckpoint(sessionID)
	m.startNativeCheckpoint(sessionID, agent, workdir, home, tmux)
}

func (m *Manager) runNativeCheckpoint(ctx context.Context, sessionID int64, agent, workdir, home, tmux string) {
	// Capture immediately: a process can disappear during the old 30-second
	// blind window. Subsequent captures allow a native CLI to move to a new
	// conversation after compaction or an explicit fork.
	refresh := func() bool {
		if ctx.Err() != nil {
			return false
		}
		row, err := m.DB.Session(sessionID)
		if err != nil || row.EndedAt != nil {
			return false
		}
		// A pre-recovery row without a persisted tmux marker cannot prove that
		// the current process is the one Lectern launched. Leave its native
		// checkpoint unknown; boot recovery will keep it visible for inspection.
		if row.TrackingIdentity == "" {
			return false
		}
		bootID, trackingIdentity := row.BootID, row.TrackingIdentity
		target, err := m.DB.Target(row.TargetID)
		if err != nil {
			return false
		}
		ex, err := m.Reg.For(target)
		if err != nil {
			return false
		}
		captureCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		cid := CaptureNativeID(captureCtx, ex, agent, workdir, home, tmux, row.TrackingIdentity)
		cancel()
		if ctx.Err() != nil {
			return false
		}
		if cid != "" && cid != row.NativeRecoveryCID {
			// Do not overwrite a new lifecycle generation if the capture completed
			// after the row was stopped and reused.
			_, _ = m.DB.Exec("UPDATE sessions SET native_recovery_cid=? WHERE id=? AND ended_at IS NULL AND tmux_session=? AND boot_id=? AND tracking_identity=?", cid, sessionID, tmux, bootID, trackingIdentity)
		}
		return true
	}
	if !refresh() {
		return
	}
	// The first process paint and transcript open are asynchronous relative to
	// tmux creation. Retry briefly so a just-launched session does not spend its
	// entire first checkpoint interval with an unknown native binding.
	for i := 0; i < 10; i++ {
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if !refresh() {
			return
		}
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !refresh() {
				return
			}
		}
	}
}

// startCheckpointsForLiveRows is called by the poller after every App start;
// it also covers a process that was alive before the new manager existed.
func (m *Manager) startCheckpointsForLiveRows(rows []*store.Session) {
	for _, row := range rows {
		if row.EndedAt != nil || row.ArchivedAt != nil || row.SetupState == "creating" || row.Status == StatusInterrupted || row.Origin != "lectern" {
			continue
		}
		var spec Spec
		if cfg, err := m.SessionLaunchConfiguration(row); err == nil {
			spec = cfg.Spec
		}
		m.startNativeCheckpoint(row.ID, row.Agent, row.Workdir, nativeHome(spec, row.Agent), row.TmuxSession)
	}
}

// Close stops refresh workers before the shared database is closed.
func (m *Manager) Close() {
	m.checkpointMu.Lock()
	m.checkpointClosed = true
	for id, cancel := range m.checkpoints {
		cancel()
		delete(m.checkpoints, id)
	}
	m.checkpointMu.Unlock()
	m.checkpointWG.Wait()
}
