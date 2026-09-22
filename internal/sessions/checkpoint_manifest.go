package sessions

// The checkpoint manifest bridges an old Lectern database and the first
// process which opens it after an upgrade. Export is independent of store.Open:
// store.Open creates the current schema and runs migrations, which would erase
// the evidence that this exporter is meant to capture.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/shellq"
	"github.com/JeremiahM37/lectern/internal/store"
)

const checkpointVersion = 2

var (
	// A session started before the rename is still named adk-s<id>.
	checkpointTmuxName = regexp.MustCompile(`^(lec|adk)-s[0-9]+$`)
	checkpointUUID     = regexp.MustCompile(`^[a-fA-F0-9]{8}(?:-[a-fA-F0-9]{4}){3}-[a-fA-F0-9]{12}$`)
	checkpointTracking = regexp.MustCompile(`^[a-f0-9]{32}$`)
	checkpointHash     = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// DBIdentity uses file identity rather than mutable size or mtime. Device and
// inode remain stable while SQLite updates the database in place.
type DBIdentity struct {
	Path   string `json:"path"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

// TargetConfig is the target connection identity used to establish an
// executor. Display and scheduling fields are intentionally omitted: renaming
// a target or changing its concurrency must not discard a valid checkpoint.
type TargetConfig struct {
	Kind          string `json:"kind"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	User          string `json:"user"`
	KeyPath       string `json:"key_path"`
	CommandPrefix string `json:"command_prefix"`
}

func targetConfig(t *store.Target) TargetConfig {
	return TargetConfig{Kind: t.Kind, Host: t.Host, Port: t.Port, User: t.User,
		KeyPath: t.KeyPath, CommandPrefix: t.CommandPrefix}
}

// TargetFingerprint is stable across health probes and target row timestamps.
func TargetFingerprint(t *store.Target) string {
	b, _ := json.Marshal(targetConfig(t))
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func launchConfigFingerprint(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// IdentityState says where a native conversation id came from. A durable
// explicit id is the UUID already stored in the old row (for example, from an
// explicit native restore); it does not claim the current process still owns
// that transcript. Unknown is never silently upgraded to a guessed id.
const (
	IdentityVerifiedCurrent = "verified_current"
	IdentityDurableExplicit = "durable_explicit"
	IdentityUnknown         = "unknown"
)

type CheckpointManifest struct {
	Version   int                 `json:"version"`
	CreatedAt string              `json:"created_at"`
	DB        DBIdentity          `json:"database"`
	Sessions  []CheckpointSession `json:"sessions"`
}

type CheckpointSession struct {
	ID                 int64   `json:"id"`
	CreatedAt          float64 `json:"created_at"`
	TargetID           int64   `json:"target_id"`
	TargetFingerprint  string  `json:"target_fingerprint"`
	Name               string  `json:"name"`
	Agent              string  `json:"agent"`
	Model              string  `json:"model"`
	LaunchConfigSHA256 string  `json:"launch_config_sha256"`
	TmuxSession        string  `json:"tmux_session"`
	TrackingIdentity   string  `json:"tracking_identity"`
	Workdir            string  `json:"workdir"`
	NativeHome         string  `json:"native_home"`
	BootID             string  `json:"boot_id"`
	NativeRecoveryCID  string  `json:"native_recovery_cid"`
	IdentityState      string  `json:"identity_state"`
}

type CheckpointSkip struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

type ImportReport struct {
	Imported int              `json:"imported"`
	Skipped  []CheckpointSkip `json:"skipped,omitempty"`
}

func (r *ImportReport) skip(id int64, reason string) {
	r.Skipped = append(r.Skipped, CheckpointSkip{ID: id, Reason: reason})
}

// CheckpointExecutorFactory is injected so export can use a real local, SSH,
// or PCT executor. The exporter never uses the mock registry.
type CheckpointExecutorFactory func(*store.Target) (executor.Executor, error)

func dbIdentity(path string) (DBIdentity, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return DBIdentity{}, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return DBIdentity{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return DBIdentity{}, err
	}
	device, inode, err := platformFileIdentity(info)
	if err != nil {
		return DBIdentity{}, err
	}
	return DBIdentity{Path: abs, Device: device, Inode: inode}, nil
}

func openReadonly(ctx context.Context, path string) (*sql.DB, error) {
	// mode=ro is essential; query_only is a second guard against accidental
	// writes if this code is changed later.
	db, err := sql.Open("sqlite", path+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

func column(cols map[string]bool, name, fallback string) string {
	if cols[name] {
		return name
	}
	return fallback
}

func readTargets(ctx context.Context, db *sql.DB) (map[int64]*store.Target, error) {
	cols, err := tableColumns(ctx, db, "targets")
	if err != nil {
		return nil, err
	}
	for _, required := range []string{"id", "name", "kind"} {
		if !cols[required] {
			return nil, fmt.Errorf("database targets table lacks %s", required)
		}
	}
	q := "SELECT id,name," + column(cols, "kind", "'ssh'") + "," +
		column(cols, "host", "''") + "," + column(cols, "port", "22") + "," +
		column(cols, "user", "'root'") + "," + column(cols, "key_path", "''") + "," +
		column(cols, "workroot", "''") + "," + column(cols, "max_concurrent", "4") + "," +
		column(cols, "sandbox", "0") + "," + column(cols, "context_json", "'[]'") + "," +
		column(cols, "memory_dir", "''") + "," + column(cols, "command_prefix", "''") +
		" FROM targets ORDER BY id"
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*store.Target{}
	for rows.Next() {
		var t store.Target
		var name, kind, host, user, keyPath, workroot, contextJSON, memoryDir, prefix sql.NullString
		var port, maxConcurrent, sandbox sql.NullInt64
		if err := rows.Scan(&t.ID, &name, &kind, &host, &port, &user, &keyPath,
			&workroot, &maxConcurrent, &sandbox, &contextJSON, &memoryDir, &prefix); err != nil {
			return nil, err
		}
		t.Name, t.Kind, t.Host, t.User, t.KeyPath, t.Workroot = name.String, kind.String, host.String, user.String, keyPath.String, workroot.String
		t.Port, t.MaxConcurrent, t.Sandbox = int(port.Int64), int(maxConcurrent.Int64), int(sandbox.Int64)
		t.ContextJSON, t.MemoryDir, t.CommandPrefix = contextJSON.String, memoryDir.String, prefix.String
		out[t.ID] = &t
	}
	return out, rows.Err()
}

type sourceSession struct {
	ID, TargetID                 int64
	Name, Agent, Model           string
	Workdir, TmuxSession, Status string
	CreatedAt                    float64
	Tracking, ResumeID           string
	LaunchConfig                 string
}

func readSourceSessions(ctx context.Context, db *sql.DB) ([]sourceSession, error) {
	cols, err := tableColumns(ctx, db, "sessions")
	if err != nil {
		return nil, err
	}
	for _, required := range []string{"id", "target_id", "name", "agent", "workdir", "tmux_session", "created_at"} {
		if !cols[required] {
			return nil, fmt.Errorf("database sessions table lacks %s", required)
		}
	}
	ended, archived := column(cols, "ended_at", "NULL"), column(cols, "archived_at", "NULL")
	q := "SELECT id,target_id,name," + column(cols, "agent", "'claude'") + "," +
		column(cols, "model", "''") + ",workdir,tmux_session," + column(cols, "status", "'starting'") +
		",created_at," + column(cols, "tracking_identity", "''") + "," +
		column(cols, "resume_id", "''") + "," + column(cols, "launch_config_json", "''") +
		" FROM sessions WHERE " + ended + " IS NULL AND " + archived + " IS NULL ORDER BY id"
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sourceSession
	for rows.Next() {
		var s sourceSession
		var name, agent, model, workdir, tmuxName, status, tracking, resumeID, launchConfig sql.NullString
		var createdAt sql.NullFloat64
		if err := rows.Scan(&s.ID, &s.TargetID, &name, &agent, &model, &workdir,
			&tmuxName, &status, &createdAt, &tracking, &resumeID, &launchConfig); err != nil {
			return nil, err
		}
		s.Name, s.Agent, s.Model, s.Workdir, s.TmuxSession, s.Status = name.String, agent.String, model.String, workdir.String, tmuxName.String, status.String
		s.Tracking, s.ResumeID, s.LaunchConfig = tracking.String, resumeID.String, launchConfig.String
		if createdAt.Valid {
			s.CreatedAt = createdAt.Float64
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Discover the configuration directory from the agent process below the exact
// tmux pane. This deliberately does not use the exporting user's HOME.
const configuredHomeScript = `import os, subprocess, sys
agent, name = sys.argv[1:]
try:
    root = int(subprocess.check_output(['tmux','display-message','-p','-t','='+name+':','#{pane_pid}'], stderr=subprocess.DEVNULL, text=True).strip())
    seen = set(); queue = [root]; children = {}
    for ent in os.listdir('/proc'):
        if not ent.isdigit(): continue
        try:
            p = int(ent); fields = open('/proc/'+ent+'/stat').read().rsplit(')',1)[1].split(); children.setdefault(int(fields[1]), []).append(p)
        except (OSError, ValueError, IndexError): pass
    while queue:
        p = queue.pop()
        if p in seen: continue
        seen.add(p); queue.extend(children.get(p, []))
    for p in sorted(seen):
        try:
            executable = os.path.basename(os.readlink('/proc/'+str(p)+'/exe'))
            # Claude Code installs each release under a versioned executable
            # path (for example .../versions/2.1.268), while its process comm
            # remains the name claude. This probe only locates the configured home;
            # CaptureNativeID separately validates PID/starttime, transcript,
            # workspace, and tmux tracking identity before accepting a CID.
            comm = open('/proc/'+str(p)+'/comm').read().strip()
            if executable != agent and not (agent == 'claude' and comm == 'claude'): continue
            env = {}
            for line in open('/proc/'+str(p)+'/environ','rb').read().split(b'\0'):
                if b'=' in line:
                    k,v=line.split(b'=',1); env[k.decode(errors='ignore')]=v.decode(errors='ignore')
            key = 'CODEX_HOME' if agent == 'codex' else 'CLAUDE_CONFIG_DIR'
            value = env.get(key, '')
            if not value: value = os.path.join(env.get('HOME',''), '.codex' if agent == 'codex' else '.claude')
            if value: print(os.path.realpath(os.path.expanduser(value))); raise SystemExit(0)
        except (OSError, ValueError): pass
except (OSError, ValueError, subprocess.SubprocessError): pass
raise SystemExit(1)`

func ProbeConfiguredHome(ctx context.Context, ex executor.Executor, agent, tmuxName string) (string, error) {
	cmd := "python3 -c " + shellq.Quote(configuredHomeScript) + " " + shellq.Quote(agent) + " " + shellq.Quote(tmuxName)
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 10})
	if err != nil || !r.OK() {
		if err != nil {
			return "", err
		}
		return "", errors.New("configured native home is unavailable")
	}
	home := strings.TrimSpace(r.Stdout)
	if home == "" || strings.ContainsAny(home, "\r\n") {
		return "", errors.New("configured native home is empty")
	}
	return home, nil
}

func ProbeTrackingIdentity(ctx context.Context, ex executor.Executor, tmuxName string) (string, error) {
	if !checkpointTmuxName.MatchString(tmuxName) {
		return "", errors.New("invalid tmux session name")
	}
	q := "tmux display-message -p -t " + shellq.Quote("="+tmuxName+":") + " " + shellq.Quote(trackingFormat)
	r, err := ex.Run(ctx, q, executor.RunOpts{Timeout: 10})
	identity := strings.TrimSpace(r.Stdout)
	if err != nil || !r.OK() || !checkpointTracking.MatchString(identity) {
		return "", errors.New("tmux tracking identity is unavailable")
	}
	return identity, nil
}

func ProbeExactTmux(ctx context.Context, ex executor.Executor, name string) error {
	if !checkpointTmuxName.MatchString(name) {
		return errors.New("invalid tmux session name")
	}
	r, err := ex.Run(ctx, "tmux has-session -t "+shellq.Quote("="+name), executor.RunOpts{Timeout: 10})
	if err != nil || !r.OK() {
		return fmt.Errorf("tmux session %q is unavailable", name)
	}
	r, err = ex.Run(ctx, "tmux display-message -p -t "+shellq.Quote("="+name+":")+" '#{session_name}'", executor.RunOpts{Timeout: 10})
	if err != nil || !r.OK() || strings.TrimSpace(r.Stdout) != name {
		return fmt.Errorf("tmux session %q failed exact identity check", name)
	}
	return nil
}

// ExportCheckpoint reads the old DB read-only and captures every live session.
// An unavailable native identity is represented as unknown; callers requiring
// complete coverage must reject that manifest before replacing the binary.
// shellAgent marks a session that is a plain shell rather than an agent.
const shellAgent = "shell"

func ExportCheckpoint(ctx context.Context, dbPath string, factory CheckpointExecutorFactory) (CheckpointManifest, error) {
	identity, err := dbIdentity(dbPath)
	if err != nil {
		return CheckpointManifest{}, err
	}
	db, err := openReadonly(ctx, identity.Path)
	if err != nil {
		return CheckpointManifest{}, err
	}
	defer db.Close()
	targets, err := readTargets(ctx, db)
	if err != nil {
		return CheckpointManifest{}, err
	}
	rows, err := readSourceSessions(ctx, db)
	if err != nil {
		return CheckpointManifest{}, err
	}
	m := CheckpointManifest{Version: checkpointVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), DB: identity}
	for _, row := range rows {
		target := targets[row.TargetID]
		if target == nil {
			return CheckpointManifest{}, fmt.Errorf("session %d references missing target %d", row.ID, row.TargetID)
		}
		if !checkpointTmuxName.MatchString(row.TmuxSession) {
			return CheckpointManifest{}, fmt.Errorf("session %d has invalid tmux name", row.ID)
		}
		// A blank shell has no agent conversation to carry across an upgrade:
		// there is nothing to checkpoint and nothing a manifest could restore. Its
		// tmux session survives the restart like any other. Treating it as an
		// unsupported row failed the whole export, so one open terminal blocked
		// every upgrade — and the Terminals view makes opening one a single tap.
		if row.Agent == shellAgent {
			continue
		}
		if row.Workdir == "" || row.Agent == "" || row.LaunchConfig == "" {
			return CheckpointManifest{}, fmt.Errorf("session %d is unsupported: missing workdir, agent, or launch configuration", row.ID)
		}
		if !checkpointTracking.MatchString(row.Tracking) {
			return CheckpointManifest{}, fmt.Errorf("session %d is unsupported: missing tracking identity", row.ID)
		}
		if factory == nil {
			return CheckpointManifest{}, errors.New("checkpoint export requires a real executor factory")
		}
		ex, err := factory(target)
		if err != nil {
			return CheckpointManifest{}, fmt.Errorf("session %d target %d: %w", row.ID, row.TargetID, err)
		}
		if err := ProbeExactTmux(ctx, ex, row.TmuxSession); err != nil {
			return CheckpointManifest{}, fmt.Errorf("session %d: %w", row.ID, err)
		}
		liveTracking, err := ProbeTrackingIdentity(ctx, ex, row.TmuxSession)
		if err != nil || liveTracking != row.Tracking {
			return CheckpointManifest{}, fmt.Errorf("session %d: tmux tracking identity changed", row.ID)
		}
		boot, known := ProbeBootID(ctx, ex)
		if !known {
			return CheckpointManifest{}, fmt.Errorf("session %d target boot id is unavailable", row.ID)
		}
		home, homeErr := ProbeConfiguredHome(ctx, ex, row.Agent, row.TmuxSession)
		cid, state := "", IdentityUnknown
		if homeErr == nil {
			cid = CaptureNativeID(ctx, ex, row.Agent, row.Workdir, home, row.TmuxSession, row.Tracking)
			if checkpointUUID.MatchString(cid) {
				state = IdentityVerifiedCurrent
			} else {
				cid = ""
			}
		}
		if cid == "" && checkpointUUID.MatchString(row.ResumeID) {
			cid, state = row.ResumeID, IdentityDurableExplicit
		}
		m.Sessions = append(m.Sessions, CheckpointSession{ID: row.ID, CreatedAt: row.CreatedAt,
			TargetID: row.TargetID, TargetFingerprint: TargetFingerprint(target),
			Name: row.Name, Agent: row.Agent, Model: row.Model, LaunchConfigSHA256: launchConfigFingerprint(row.LaunchConfig),
			TmuxSession: row.TmuxSession, TrackingIdentity: row.Tracking, Workdir: row.Workdir,
			NativeHome: home, BootID: boot, NativeRecoveryCID: cid, IdentityState: state})
	}
	return m, nil
}

func WriteCheckpoint(path string, m CheckpointManifest) error {
	if err := validateManifest(m); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".lectern-checkpoint-*.partial")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func ReadCheckpoint(path string) (CheckpointManifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return CheckpointManifest{}, err
	}
	var m CheckpointManifest
	if err = json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if err = validateManifest(m); err != nil {
		return m, err
	}
	return m, nil
}

func validateManifest(m CheckpointManifest) error {
	if m.Version != checkpointVersion || m.DB.Path == "" || m.DB.Device == 0 || m.DB.Inode == 0 {
		return errors.New("invalid checkpoint manifest version or database identity")
	}
	seen := map[int64]bool{}
	for _, s := range m.Sessions {
		if s.ID <= 0 || seen[s.ID] {
			return fmt.Errorf("invalid or duplicate checkpoint session %d", s.ID)
		}
		seen[s.ID] = true
		if s.CreatedAt <= 0 || s.TargetID <= 0 || !checkpointHash.MatchString(s.TargetFingerprint) || s.Workdir == "" || s.Agent == "" || !checkpointHash.MatchString(s.LaunchConfigSHA256) {
			return fmt.Errorf("session %d has incomplete checkpoint identity", s.ID)
		}
		if !checkpointTmuxName.MatchString(s.TmuxSession) || !checkpointTracking.MatchString(s.TrackingIdentity) || !checkpointUUID.MatchString(s.BootID) {
			return fmt.Errorf("session %d has invalid target identity", s.ID)
		}
		if s.NativeRecoveryCID != "" && !checkpointUUID.MatchString(s.NativeRecoveryCID) {
			return fmt.Errorf("session %d has invalid native recovery id", s.ID)
		}
		if s.IdentityState != IdentityVerifiedCurrent && s.IdentityState != IdentityDurableExplicit && s.IdentityState != IdentityUnknown {
			return fmt.Errorf("session %d has invalid identity state", s.ID)
		}
		if s.IdentityState != IdentityUnknown && s.NativeRecoveryCID == "" {
			return fmt.Errorf("session %d identity state has no id", s.ID)
		}
		if s.IdentityState == IdentityUnknown && s.NativeRecoveryCID != "" {
			return fmt.Errorf("session %d unknown identity state has an id", s.ID)
		}
	}
	return nil
}

func sameFloat(a, b float64) bool {
	return a == b
}

// ImportCheckpoint runs after store.Open has migrated the database and before
// the first poll. It fills only empty recovery fields with the target's old
// boot id and captured native id. Mismatches are reported per row.
func ImportCheckpoint(path, dbPath string) (ImportReport, error) {
	m, err := ReadCheckpoint(path)
	if err != nil {
		return ImportReport{}, err
	}
	current, err := dbIdentity(dbPath)
	if err != nil {
		return ImportReport{}, err
	}
	if current != m.DB {
		return ImportReport{}, errors.New("checkpoint database identity does not match current database")
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return ImportReport{}, err
	}
	defer db.Close()
	type candidate struct {
		s      CheckpointSession
		row    *store.Session
		target *store.Target
	}
	var candidates []candidate
	report := ImportReport{}
	for _, s := range m.Sessions {
		row, e := db.Session(s.ID)
		if errors.Is(e, store.ErrNotFound) {
			report.skip(s.ID, "session missing")
			continue
		}
		if e != nil {
			report.skip(s.ID, "session lookup failed: "+e.Error())
			continue
		}
		if row.EndedAt != nil || row.ArchivedAt != nil {
			report.skip(s.ID, "session ended or archived")
			continue
		}
		target, e := db.Target(s.TargetID)
		if e != nil {
			report.skip(s.ID, "target missing: "+e.Error())
			continue
		}
		if row.TargetID != s.TargetID || TargetFingerprint(target) != s.TargetFingerprint || row.TmuxSession != s.TmuxSession || row.Workdir != s.Workdir || row.Agent != s.Agent || row.Model != s.Model || !sameFloat(row.CreatedAt, s.CreatedAt) || row.TrackingIdentity != s.TrackingIdentity || launchConfigFingerprint(row.LaunchConfigJSON) != s.LaunchConfigSHA256 {
			report.skip(s.ID, "durable session identity changed")
			continue
		}
		if s.IdentityState == IdentityUnknown || s.NativeRecoveryCID == "" {
			report.skip(s.ID, "native identity unavailable")
			continue
		}
		// A field from a newer checkpoint is authoritative. Do not combine it
		// with an older manifest's other half, even though the SQL update could
		// technically leave the populated field untouched.
		if (row.BootID != "" && row.BootID != s.BootID) || (row.NativeRecoveryCID != "" && row.NativeRecoveryCID != s.NativeRecoveryCID) {
			report.skip(s.ID, "newer checkpoint already populated")
			continue
		}
		candidates = append(candidates, candidate{s: s, row: row, target: target})
	}
	tx, err := db.Begin()
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	for _, c := range candidates {
		set, args, guards := []string{}, []any{}, []string{}
		if c.row.BootID == "" {
			set, args = append(set, "boot_id=?"), append(args, c.s.BootID)
			guards = append(guards, "(boot_id IS NULL OR boot_id='')")
		}
		if c.row.NativeRecoveryCID == "" {
			set, args = append(set, "native_recovery_cid=?"), append(args, c.s.NativeRecoveryCID)
			guards = append(guards, "(native_recovery_cid IS NULL OR native_recovery_cid='')")
		}
		if len(set) == 0 {
			report.skip(c.s.ID, "checkpoint already populated")
			continue
		}
		args = append(args, c.s.ID, c.s.TargetID, c.s.Agent, c.s.Model, c.s.TmuxSession,
			c.s.Workdir, c.s.CreatedAt, c.s.TrackingIdentity)
		q := "UPDATE sessions SET " + strings.Join(set, ",") + " WHERE id=? AND target_id=? AND agent=? AND model=? AND tmux_session=? AND workdir=? AND created_at=? AND tracking_identity=? AND ended_at IS NULL AND archived_at IS NULL AND " + strings.Join(guards, " AND ")
		res, e := tx.Exec(q, args...)
		if e != nil {
			report.skip(c.s.ID, "checkpoint update failed")
			continue
		}
		n, e := res.RowsAffected()
		if e != nil || n != 1 {
			report.skip(c.s.ID, "session changed during import")
			continue
		}
		report.Imported++
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	sort.Slice(report.Skipped, func(i, j int) bool { return report.Skipped[i].ID < report.Skipped[j].ID })
	return report, nil
}
