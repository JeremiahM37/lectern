package agent

import (
	"encoding/json"
	"io"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/redact"
)

// DumpRequests returns a RequestDump that writes each model request to w as
// one JSON object per line: the step, the model ref and the request body.
// Request headers are left out, and the line is passed through the secret
// redactor, so a dump can be shared. It backs the API export of a session.
// Writes are serialised; a write error is ignored, because a dump must never
// fail the turn it observes.
func DumpRequests(w io.Writer) func(step int, modelRef string, req llm.Request) {
	var mu sync.Mutex
	return func(step int, modelRef string, req llm.Request) {
		line, err := json.Marshal(struct {
			Step    int         `json:"step"`
			Model   string      `json:"model"`
			Request llm.Request `json:"request"`
		}{Step: step, Model: modelRef, Request: withoutHeaders(req)})
		if err != nil {
			return
		}
		out := redact.Default().Redact(string(line)) + "\n"
		mu.Lock()
		defer mu.Unlock()
		_, _ = io.WriteString(w, out)
	}
}

// withoutHeaders returns a copy of req with its extra HTTP headers removed:
// they can carry session or gateway credentials.
func withoutHeaders(req llm.Request) llm.Request {
	req.Headers = nil
	return req
}
