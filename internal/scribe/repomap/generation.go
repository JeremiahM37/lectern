package repomap

// Generation returns a counter that changes every time the map's graph is
// rebuilt or updated (Build, Update, and the watcher's and Recheck's updates
// through Update). A caller that renders the map keeps the generation it
// rendered at and re-renders when this differs; reading it costs nothing.
func (m *Map) Generation() uint64 { return m.generation.Load() }
