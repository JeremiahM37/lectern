package memory

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// The cleanup hint on the context bar (spec 05 §1.9) appears only when the
// store is big enough and has been around long enough to need tidying:
// at least hintMinActive live memories, at least hintMinSessions sessions
// since the last completed cleanup, and at least hintMinStale stale ones.
const (
	hintMinActive   = 30
	hintMinSessions = 20
	hintMinStale    = 10
)

// cleanupState is the one counter the hint needs. It lives beside the
// global store, so every project shares it, as the store itself is shared.
type cleanupState struct {
	SessionsSince int `json:"sessions_since"`
}

func cleanupStatePath() string { return filepath.Join(config.Home(), "memory-cleanup.json") }

func readCleanupState() cleanupState {
	var st cleanupState
	if b, err := os.ReadFile(cleanupStatePath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func writeCleanupState(st cleanupState) error {
	if err := os.MkdirAll(filepath.Dir(cleanupStatePath()), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(cleanupStatePath(), b, 0o600)
}

// NoteSessionStart counts one session start toward the cleanup hint. One small
// file write per session, never per turn.
func NoteSessionStart() error {
	st := readCleanupState()
	st.SessionsSince++
	return writeCleanupState(st)
}

// NoteCleanupDone resets the session counter after the person completed a
// cleanup action, so the hint waits for a fresh stretch of sessions.
func NoteCleanupDone() error {
	return writeCleanupState(cleanupState{})
}

// LiveCount counts the live records (not hidden, not superseded) across the
// open stores. Errors count as zero.
func (s *Stores) LiveCount() int {
	n := 0
	for _, d := range s.DBs() {
		var c int
		if err := d.db.QueryRow("SELECT COUNT(*) FROM memories WHERE " + live).Scan(&c); err == nil {
			n += c
		}
	}
	return n
}

// CleanupHintCount is the number the context bar shows: the stale count when
// every threshold is met, and 0 otherwise. It never blocks the UI.
func (s *Stores) CleanupHintCount() int {
	if s.LiveCount() < hintMinActive {
		return 0
	}
	if readCleanupState().SessionsSince < hintMinSessions {
		return 0
	}
	n := s.StaleCount()
	if n < hintMinStale {
		return 0
	}
	return n
}
