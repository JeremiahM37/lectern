package scheduler

import (
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/memory"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// recordMemoryDelivery writes the delivery log row for a dispatched attempt and
// announces it, so "what was this task's agent given" is answerable after the
// fact and the operator can correct it — see docs/memory-visibility.md.
//
// The session-side equivalent lives in internal/sessions; the two paths share
// the table (and the bus event name) but not their ids, which is why a row here
// names the task and attempt instead of a session.
func (s *Scheduler) recordMemoryDelivery(att *store.Attempt, c *runCtx, result memory.ContextResult) {
	if result.Context == "" || s.DB == nil {
		return
	}
	taskID, attemptID := c.Task.ID, att.ID
	row, err := s.DB.InsertMemoryDelivery(store.MemoryDelivery{
		TaskID: &taskID, AttemptID: &attemptID, Mode: result.Mode,
		Bytes: result.Size(), ItemsJSON: result.ItemsJSON()})
	if err != nil {
		s.Log.Warn("memory delivery could not be recorded", "attempt", att.ID, "err", err)
		return
	}
	if s.Bus == nil {
		return
	}
	payload := map[string]any{"id": row.ID, "task_id": taskID, "attempt_id": attemptID,
		"at": row.At, "mode": row.Mode, "bytes": row.Bytes, "items": result.Items}
	s.Bus.Publish("board", "memory.delivered", payload)
	s.Bus.Publish(fmt.Sprintf("task:%d", taskID), "memory.delivered", payload)
}
