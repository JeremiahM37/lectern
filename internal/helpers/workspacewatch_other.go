//go:build !linux

package helpers

import "time"

// dirWatcher: without inotify the watch polls.
type dirWatcher struct{}

func (w *wsFiles) startWatch([]string) *dirWatcher { return &dirWatcher{} }

func (d *dirWatcher) ok() bool                { return false }
func (d *dirWatcher) wait(time.Duration) bool { return false }
func (d *dirWatcher) drain()                  {}
func (d *dirWatcher) close()                  {}
