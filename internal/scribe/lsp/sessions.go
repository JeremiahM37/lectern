package lsp

import (
	"sort"
	"sync"
	"time"
)

// Session states reported by Sessions.
const (
	StateStarting = "starting"
	StateRunning  = "running"
	StateFailed   = "failed"
)

// SessionInfo describes one language server the Manager knows about: one
// that is running, one that is starting, or one whose last start (or crash)
// failed. It is a snapshot for status views such as /lsp.
type SessionInfo struct {
	Language  string    `json:"language"`
	Command   string    `json:"command,omitempty"` // empty until the binary is found
	Root      string    `json:"root"`
	State     string    `json:"state"`
	PID       int       `json:"pid,omitempty"`
	OpenDocs  int       `json:"open_docs"`
	LastError string    `json:"last_error,omitempty"`
	Since     time.Time `json:"since"`
}

// sessionStatus is the bookkeeping behind Sessions. It has its own mutex so
// a status listing never waits for a server start, which holds Manager.mu.
type sessionStatus struct {
	mu   sync.Mutex
	info map[string]SessionInfo // keyed like Manager.sessions
	live map[string]*session
	dir  string // where running servers are recorded; empty records nothing
}

func (st *sessionStatus) init() {
	st.info = map[string]SessionInfo{}
	st.live = map[string]*session{}
}

func (m *Manager) noteStarting(key string, spec *serverSpec, root string) {
	m.status.mu.Lock()
	defer m.status.mu.Unlock()
	m.status.info[key] = SessionInfo{Language: spec.Language, Root: root, State: StateStarting, Since: time.Now()}
}

func (m *Manager) noteStarted(key string, s *session) {
	m.status.mu.Lock()
	defer m.status.mu.Unlock()
	m.status.info[key] = SessionInfo{Language: s.spec.Language, Root: s.root, State: StateRunning,
		Command: s.command, PID: s.pid(), Since: time.Now()}
	m.status.live[key] = s
	m.status.persistLocked()
}

func (m *Manager) noteFailed(key string, spec *serverSpec, root string, err error) {
	m.status.mu.Lock()
	defer m.status.mu.Unlock()
	delete(m.status.live, key)
	m.status.info[key] = SessionInfo{Language: spec.Language, Root: root, State: StateFailed,
		LastError: err.Error(), Since: time.Now()}
	m.status.persistLocked()
}

// Sessions lists the language servers this Manager has started or tried to
// start, sorted by language and root. A running server that has since exited
// is reported as failed, with its last stderr output as the error.
func (m *Manager) Sessions() []SessionInfo {
	m.status.mu.Lock()
	keys := make([]string, 0, len(m.status.info))
	for k := range m.status.info {
		keys = append(keys, k)
	}
	out := make([]SessionInfo, 0, len(keys))
	for _, k := range keys {
		info := m.status.info[k]
		if s := m.status.live[k]; s != nil && info.State == StateRunning {
			if !s.alive() {
				info.State = StateFailed
				info.LastError = "server exited"
				if tail := s.stderr.String(); tail != "" {
					info.LastError = tail
				}
			}
			info.OpenDocs = s.openDocs()
		}
		out = append(out, info)
	}
	m.status.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Language != out[j].Language {
			return out[i].Language < out[j].Language
		}
		return out[i].Root < out[j].Root
	})
	return out
}

// clearStatus forgets every session. Shutdown calls it.
func (m *Manager) clearStatus() {
	m.status.mu.Lock()
	defer m.status.mu.Unlock()
	m.status.info = map[string]SessionInfo{}
	m.status.live = map[string]*session{}
}
