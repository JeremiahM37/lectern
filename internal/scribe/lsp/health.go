package lsp

import (
	"fmt"
	"time"
)

// degradeAfter is how many operations in a row must fail before a server is
// marked degraded. The spec gives no count for this. Three keeps one unlucky
// request, such as a rename on a keyword, from taking a healthy server out of
// use, while a server that times out repeatedly is caught within a few calls.
const degradeAfter = 3

// opHealth is the per-operation record for one server: how many operations
// have failed in a row and what the last failure said.
type opHealth struct {
	language string
	failures int
	lastErr  string
	lastAt   time.Time
}

// healthKey names the server that would answer for abs, in the same form as
// Manager.sessions. It is empty when no server is catalogued for abs.
func (m *Manager) healthKey(abs string) (string, string) {
	info, ok := languageForPath(abs)
	if !ok {
		return "", ""
	}
	spec := serverSpecs[info.key]
	return spec.Language + "\x00" + m.rootFor(spec, abs), spec.Language
}

// degradedSkip returns the error that stops an operation from being sent to a
// server that has failed degradeAfter operations in a row, or nil when the
// operation may go ahead. Once failCooldown has passed since the last failure
// one operation is allowed through as a probe: its success clears the record,
// and its failure starts the wait again.
func (m *Manager) degradedSkip(key string) error {
	if key == "" {
		return nil
	}
	m.status.mu.Lock()
	defer m.status.mu.Unlock()
	h := m.status.health[key]
	if h == nil || h.failures < degradeAfter {
		return nil
	}
	wait := failCooldown - time.Since(h.lastAt)
	if wait <= 0 {
		return nil
	}
	return fmt.Errorf("the %s language server failed %d operations in a row (last: %s). It is not asked again for %s; use grep or read instead of retrying",
		h.language, h.failures, h.lastErr, wait.Round(time.Second))
}

// noteOpSuccess clears the failure record for key.
func (m *Manager) noteOpSuccess(key string) {
	if key == "" {
		return
	}
	m.status.mu.Lock()
	defer m.status.mu.Unlock()
	delete(m.status.health, key)
}

// noteOpFailure counts one failed operation against key.
func (m *Manager) noteOpFailure(key, language string, err error) {
	if key == "" {
		return
	}
	m.status.mu.Lock()
	defer m.status.mu.Unlock()
	h := m.status.health[key]
	if h == nil {
		h = &opHealth{language: language}
		m.status.health[key] = h
	}
	h.failures++
	h.lastErr = firstLine(err.Error())
	h.lastAt = time.Now()
}
