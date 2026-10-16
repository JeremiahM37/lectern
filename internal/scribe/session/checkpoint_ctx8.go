package session

// SetContext records the context fill (tokens used of the window) at the end
// of turn, so the context bar can show it while a checkpoint is browsed. It
// is a no-op when the turn wrote no files (it has no checkpoint). It persists
// at once, because the record is only ever written on turn end.
func (c *Checkpoints) SetContext(turn, used, window int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.findLocked(turn)
	if r == nil || window <= 0 {
		return nil
	}
	r.CtxUsed, r.CtxWindow = used, window
	return c.saveLocked()
}
