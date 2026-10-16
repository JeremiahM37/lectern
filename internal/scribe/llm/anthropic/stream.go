package anthropic

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// errStreamDone stops readSSE once a terminal event has been seen.
var errStreamDone = errors.New("anthropic: stream done")

// readSSE calls fn with the data payload of each server-sent event. Events
// are separated by blank lines. Comment lines and the event: field are
// ignored because every payload carries its own "type". fn returning an
// error stops the read. errStreamDone is treated as a clean stop.
func readSSE(r io.Reader, fn func(data []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)

	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		payload := data.String()
		data.Reset()
		return fn([]byte(payload))
	}

	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if err := flush(); err != nil {
				return unwrapDone(err)
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		if field == "data" {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return unwrapDone(flush())
}

func unwrapDone(err error) error {
	if errors.Is(err, errStreamDone) {
		return nil
	}
	return err
}

// wireEvent is the union of the streaming event payloads we read.
type wireEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage wireUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Name     string `json:"name"`
		Thinking string `json:"thinking"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *wireUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type wireUsage struct {
	InputTokens   int `json:"input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
}

type openBlock struct {
	kind string // "text", "thinking", "tool_use", or "other"
	id   string
	name string
	args strings.Builder
}

// streamState turns API events into llm events. It is pure apart from its
// own bookkeeping, so the parsing can be tested without a network.
type streamState struct {
	provider string
	blocks   map[int]*openBlock
	usage    llm.Usage
	stop     string
	sawTool  bool
}

func newStreamState(provider string) *streamState {
	return &streamState{provider: provider, blocks: map[int]*openBlock{}}
}

// apply handles one event payload. It returns the llm events it produced.
// When done is true the last returned event is terminal.
func (s *streamState) apply(data []byte) (evs []llm.Event, done bool, err error) {
	var ev wireEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil, false, fmt.Errorf("anthropic: bad stream event: %w", err)
	}

	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			s.mergeUsage(ev.Message.Usage)
		}

	case "content_block_start":
		if ev.ContentBlock == nil {
			return nil, false, nil
		}
		b := &openBlock{}
		switch ev.ContentBlock.Type {
		case "text":
			b.kind = "text"
		case "thinking":
			b.kind = "thinking"
			if ev.ContentBlock.Thinking != "" {
				evs = append(evs, llm.Event{Type: llm.EvReasoning, Text: ev.ContentBlock.Thinking})
			}
		case "tool_use":
			b.kind = "tool_use"
			b.id = ev.ContentBlock.ID
			b.name = ev.ContentBlock.Name
			s.sawTool = true
			evs = append(evs, llm.Event{Type: llm.EvToolCallStart, ToolCallID: b.id, ToolName: b.name})
		default:
			// redacted_thinking and server tools are not surfaced yet.
			b.kind = "other"
		}
		s.blocks[ev.Index] = b

	case "content_block_delta":
		if ev.Delta == nil {
			return nil, false, nil
		}
		b := s.blocks[ev.Index]
		switch ev.Delta.Type {
		case "text_delta":
			evs = append(evs, llm.Event{Type: llm.EvText, Text: ev.Delta.Text})
		case "thinking_delta":
			evs = append(evs, llm.Event{Type: llm.EvReasoning, Text: ev.Delta.Thinking})
		case "signature_delta":
			// The signature rides in Text, as llm.EvReasoningSig documents.
			evs = append(evs, llm.Event{Type: llm.EvReasoningSig, Text: ev.Delta.Signature})
		case "input_json_delta":
			if b == nil || b.kind != "tool_use" {
				return nil, false, nil
			}
			b.args.WriteString(ev.Delta.PartialJSON)
			evs = append(evs, llm.Event{Type: llm.EvToolCallDelta, ToolCallID: b.id, ArgsDelta: ev.Delta.PartialJSON})
		}

	case "content_block_stop":
		b := s.blocks[ev.Index]
		delete(s.blocks, ev.Index)
		if b != nil && b.kind == "tool_use" {
			evs = append(evs, llm.Event{
				Type:       llm.EvToolCallEnd,
				ToolCallID: b.id,
				ToolName:   b.name,
				// Malformed argument JSON is kept as raw text (llm.ToolArgs),
				// never replaced with {}, so the agent can report it.
				Args: llm.ToolArgs(b.args.String()),
			})
		}

	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			s.stop = ev.Delta.StopReason
		}
		if ev.Usage != nil {
			s.mergeUsage(*ev.Usage)
		}

	case "message_stop":
		reason := mapStopReason(s.stop)
		if s.stop == "" {
			reason = llm.FinishStop
			if s.sawTool {
				reason = llm.FinishToolCalls
			}
		}
		evs = append(evs, llm.Event{Type: llm.EvFinish, FinishReason: reason, Usage: s.usage})
		return evs, true, nil

	case "error":
		ae := &llm.APIError{Provider: s.provider}
		if ev.Error != nil {
			ae.Type = ev.Error.Type
			ae.Message = ev.Error.Message
		}
		ae.StatusCode = statusForErrorType(ae.Type)
		evs = append(evs, llm.Event{Type: llm.EvError, Err: ae})
		return evs, true, nil

	case "ping":
		// keep-alive, nothing to do
	}
	return evs, false, nil
}

// mergeUsage copies non-zero counters. message_start and message_delta both
// carry cumulative values, so overwriting is correct.
func (s *streamState) mergeUsage(u wireUsage) {
	if u.InputTokens > 0 {
		s.usage.InputTokens = u.InputTokens
	}
	if u.OutputTokens > 0 {
		s.usage.OutputTokens = u.OutputTokens
	}
	if u.CacheCreation > 0 {
		s.usage.CacheWriteTokens = u.CacheCreation
	}
	if u.CacheRead > 0 {
		s.usage.CacheReadTokens = u.CacheRead
	}
}

func mapStopReason(r string) llm.FinishReason {
	switch r {
	case "tool_use":
		return llm.FinishToolCalls
	case "max_tokens", "model_context_window_exceeded":
		return llm.FinishLength
	case "refusal":
		return llm.FinishFilter
	default: // end_turn, stop_sequence, pause_turn
		return llm.FinishStop
	}
}

// statusForErrorType gives the HTTP status an error type is usually sent
// with, so llm.Classify treats mid-stream errors like the equivalent HTTP
// response.
func statusForErrorType(t string) int {
	switch t {
	case "invalid_request_error":
		return 400
	case "authentication_error":
		return 401
	case "permission_error":
		return 403
	case "not_found_error":
		return 404
	case "request_too_large":
		return 413
	case "rate_limit_error":
		return 429
	case "api_error":
		return 500
	case "overloaded_error":
		return 529
	}
	return 0
}
