package sessions

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/helpers"
	"github.com/JeremiahM37/lectern/v2/internal/nativeidentity"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Catalog agents keep their conversations in their own formats, and none of
// them exposes the process evidence the built-in capture relies on. Their
// sessions become exact in one of two ways:
//
//   - Named at launch: when the definition has SessionIDArgs, Lectern
//     generates the id, passes it on the command line and records it on the
//     session before the process starts.
//   - Matched afterwards: when the definition has Sessions, the checkpoint
//     worker lists the CLI's saved conversations for the workspace and binds
//     the one created after this launch — only when exactly one qualifies, no
//     other session holds it, and no other unbound session of the same agent
//     is running in the same folder. Anything less certain stays unbound, and
//     Restore then offers the saved-conversation picker instead of guessing.

// CatalogConversation is one saved conversation a catalog CLI lists.
type CatalogConversation struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Created  *float64 `json:"created"`
	Modified *float64 `json:"modified"`
}

// captureSlack allows for a CLI that stamps its session a moment before
// tmux reports the launch complete, and for clock rounding.
const captureSlack = 10.0

// captureSettle is how long after a launch capture first reads the CLI's
// sessions.
const captureSettle = 5.0

// MatchLaunchedConversation picks the conversation created by one launch,
// never one another session holds. existing, when known, is every
// conversation listed just before the launch: anything absent from it and
// written since is new, which also covers a fork whose CLI copies its
// parent's creation time. Without it, the conversation must have been created
// at or after launchedAt (less captureSlack). It returns "" with
// ambiguous=true when more than one qualifies.
func MatchLaunchedConversation(convs []CatalogConversation, launchedAt float64, taken, existing map[string]bool) (id string, ambiguous bool) {
	for _, c := range convs {
		if c.ID == "" || taken[c.ID] {
			continue
		}
		created := c.Created != nil && *c.Created >= launchedAt-captureSlack
		if existing != nil {
			written := created || (c.Modified != nil && *c.Modified >= launchedAt-captureSlack)
			if existing[c.ID] || !written {
				continue
			}
		} else if !created {
			continue
		}
		if id != "" && id != c.ID {
			return "", true
		}
		id = c.ID
	}
	return id, false
}

// CatalogConversationsCommand is the target-side command that lists (cid
// empty) or validates (cid set) a catalog agent's saved conversations in
// workdir, for the target ex drives. spec must already be resolved and have
// Sessions set.
func CatalogConversationsCommand(ex executor.Executor, spec Spec, workdir, cid string) (string, error) {
	if spec.Sessions == nil {
		return "", fmt.Errorf("agent %q does not say where it keeps sessions", spec.Name)
	}
	raw, err := json.Marshal(struct {
		*SessionsSpec
		Agent string `json:"agent"`
	}{spec.Sessions, spec.Name})
	if err != nil {
		return "", err
	}
	prefix, err := EnvPrefix(spec.Env)
	if err != nil {
		return "", err
	}
	bin := spec.Command
	if fields := strings.Fields(bin); len(fields) > 0 {
		bin = fields[0]
	}
	args := []string{string(raw), workdir, bin}
	if cid != "" {
		args = append(args, cid)
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellq.Quote(a)
	}
	py := "python3 -c " + shellq.Quote(nativeidentity.CatalogSessionsScript) + " " + strings.Join(quoted, " ")
	return prefix + helpers.Command(ex, "catalog-sessions", args, py), nil
}

// CatalogConversations runs CatalogConversationsCommand and returns its raw
// JSON document, or the reader's own error message.
func CatalogConversations(ctx context.Context, ex executor.Executor, spec Spec, workdir, cid string) (map[string]json.RawMessage, error) {
	cmd, err := CatalogConversationsCommand(ex, spec, workdir, cid)
	if err != nil {
		return nil, err
	}
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 40})
	var out map[string]json.RawMessage
	if err != nil || json.Unmarshal([]byte(strings.TrimSpace(r.Stdout)), &out) != nil {
		return nil, fmt.Errorf("could not read %s's saved conversations on this target", spec.Name)
	}
	if !r.OK() {
		var message string
		_ = json.Unmarshal(out["error"], &message)
		if message == "" {
			message = "could not read saved conversations"
		}
		return nil, fmt.Errorf("%s", message)
	}
	return out, nil
}

// launchTimes remembers when each launch happened, for the capture window,
// and which conversations the CLI listed just before it; a row the control
// plane did not launch itself falls back to its stored creation or relaunch
// time and the created-after-launch rule.
type launchTimes struct {
	mu       sync.Mutex
	at       map[int64]float64
	existing map[int64]map[string]bool
}

func (l *launchTimes) set(id int64, at float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.at == nil {
		l.at = map[int64]float64{}
	}
	l.at[id] = at
}

func (l *launchTimes) get(row *store.Session) float64 {
	l.mu.Lock()
	at, ok := l.at[row.ID]
	l.mu.Unlock()
	if ok {
		return at
	}
	at = row.CreatedAt
	if row.RelaunchedAt != nil && *row.RelaunchedAt > at {
		at = *row.RelaunchedAt
	}
	return at
}

// snapshot returns the pre-launch listing, nil when unknown.
func (l *launchTimes) snapshot(id int64) map[string]bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.existing[id]
}

func (l *launchTimes) setExisting(id int64, ids map[string]bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.existing == nil {
		l.existing = map[int64]map[string]bool{}
	}
	l.existing[id] = ids
}

// snapshotCatalogConversations lists the conversations that exist just
// before a launch, so the one the launch creates can be told apart even when
// its CLI stamps it with an older creation time (a fork). It runs before the
// CLI starts, never beside it: a listing command can initialise the same
// store the starting CLI is migrating (Crush failed to start when both ran at
// once). A failed listing leaves no snapshot and the created-after-launch rule
// applies.
func (m *Manager) snapshotCatalogConversations(ctx context.Context, id int64, ex executor.Executor, spec Spec, workdir string) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := CatalogConversations(ctx, ex, m.Resolve(spec), workdir, "")
	if err != nil {
		return
	}
	var convs []CatalogConversation
	if json.Unmarshal(out["conversations"], &convs) != nil {
		return
	}
	ids := map[string]bool{}
	for _, c := range convs {
		ids[c.ID] = true
	}
	m.launched.setExisting(id, ids)
}

// newConversationID is a random RFC 4122 version 4 UUID, the form every CLI
// with a session-id flag accepts.
func newConversationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// captureCatalogConversation is one checkpoint pass for a catalog agent. It
// returns the id bound (possibly one bound earlier), or "".
func (m *Manager) captureCatalogConversation(ctx context.Context, ex executor.Executor, row *store.Session, spec Spec) string {
	if row.NativeRecoveryCID != "" {
		return row.NativeRecoveryCID
	}
	// A resumed launch continues a known conversation; its binding is
	// ResumeID. Only a new conversation (fresh or fork) is captured.
	if row.ResumeID != "" || spec.Sessions == nil {
		return ""
	}
	rows, err := m.DB.Sessions(true)
	if err != nil {
		return ""
	}
	taken := map[string]bool{}
	for _, other := range rows {
		if other.ID == row.ID || other.TargetID != row.TargetID || other.Agent != row.Agent {
			continue
		}
		if other.NativeRecoveryCID != "" || other.ResumeID != "" {
			taken[other.NativeRecoveryCID] = true
			taken[other.ResumeID] = true
			continue
		}
		// Another unbound session of this agent in this folder that was alive
		// after this launch could own any new conversation just as well: never
		// guess between them.
		if other.Workdir == row.Workdir && (other.EndedAt == nil || *other.EndedAt >= m.launched.get(row)-captureSlack) {
			return ""
		}
	}
	// Give a just-started CLI time to finish initialising its store before
	// its own listing command runs beside it.
	if store.Now()-m.launched.get(row) < captureSettle {
		return ""
	}
	existing := m.launched.snapshot(row.ID)
	out, err := CatalogConversations(ctx, ex, m.Resolve(spec), row.Workdir, "")
	if err != nil {
		return ""
	}
	var convs []CatalogConversation
	if json.Unmarshal(out["conversations"], &convs) != nil {
		return ""
	}
	cid, _ := MatchLaunchedConversation(convs, m.launched.get(row), taken, existing)
	if cid == "" {
		return ""
	}
	res, err := m.DB.Exec("UPDATE sessions SET native_recovery_cid=? WHERE id=? AND native_recovery_cid='' AND resume_id='' AND tmux_session=?",
		cid, row.ID, row.TmuxSession)
	if err != nil {
		return ""
	}
	if n, _ := res.RowsAffected(); n == 1 {
		m.Log.Info("bound a catalog session to its conversation", "session", row.ID, "agent", row.Agent)
	}
	return cid
}

// finishCatalogCapture gives a catalog session that ended unbound a last
// chance to be bound. Some CLIs list a conversation only once its process has
// exited (Qwen Code leaves live sessions out of `qwen sessions list`), which
// is after the checkpoint worker stopped watching.
func (m *Manager) finishCatalogCapture(id int64) {
	row, err := m.DB.Session(id)
	if err != nil || row.Agent == "claude" || row.Agent == "codex" || row.Origin != "lectern" ||
		row.NativeRecoveryCID != "" || row.ResumeID != "" || row.EndReason == EndFailed {
		return
	}
	config, err := m.SessionLaunchConfiguration(row)
	if err != nil || config.Spec.Sessions == nil {
		return
	}
	m.checkpointMu.Lock()
	if m.checkpointClosed {
		m.checkpointMu.Unlock()
		return
	}
	m.checkpointWG.Add(1)
	m.checkpointMu.Unlock()
	go func() {
		defer m.checkpointWG.Done()
		for attempt := 0; attempt < 4; attempt++ {
			// Wait in short steps so Close is never held up for long.
			for step := 0; step < 12; step++ {
				time.Sleep(250 * time.Millisecond)
				m.checkpointMu.Lock()
				closed := m.checkpointClosed
				m.checkpointMu.Unlock()
				if closed {
					return
				}
			}
			row, ex, err := m.resolve(id)
			if err != nil || row.NativeRecoveryCID != "" {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			bound := m.captureCatalogConversation(ctx, ex, row, config.Spec)
			cancel()
			if bound != "" {
				return
			}
		}
	}()
}
