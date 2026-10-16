package tui

import (
	"bytes"
	"encoding/json"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// attachErrorArgs keeps the arguments of a failed tool call on its error-log
// row, so the detail can show what was asked. addError leaves the row it just
// wrote (or moved, for a repeat) at the end of the log, so the newest entry is
// the one to fill in.
func (m *Model) attachErrorArgs(ev agent.Event) {
	if len(m.errLog) == 0 || len(ev.Args) == 0 {
		return
	}
	m.errLog[len(m.errLog)-1].Args = string(ev.Args)
}

// prettyArgs indents a JSON argument blob for the detail view. Anything that
// is not valid JSON comes back unchanged.
func prettyArgs(raw string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
		return raw
	}
	return buf.String()
}
