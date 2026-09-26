package limits

import (
	"fmt"
	"time"
)

// DetectEvent reads one normalised task event (internal/agents) for a usage
// limit. Claude's stream reports it structurally (a rate_limit_event whose
// status is "rejected", normalised to a "rate_limit" event); every other
// agent is read from the text it printed — the result, an error record, or a
// plain output line — with the same table as a pane.
func DetectEvent(typ string, payload map[string]any, now time.Time) (Hit, bool) {
	switch typ {
	case "rate_limit":
		if s, _ := payload["status"].(string); s != "rejected" {
			return Hit{}, false
		}
		hit := Hit{Pattern: "claude-stream-rejected", Agent: "claude", Message: "Usage limit reached"}
		if kind, _ := payload["rate_limit_type"].(string); kind != "" {
			hit.Message = fmt.Sprintf("Usage limit reached (%s)", kind)
		}
		if reset, ok := payload["resets_at"].(float64); ok && reset > 0 {
			hit.ResetAt = time.Unix(int64(reset), 0)
		}
		return hit, true
	case "result":
		return textHit(payload["result"], now)
	case "error":
		return textHit(payload["message"], now)
	case "text":
		return textHit(payload["text"], now)
	case "raw":
		return textHit(payload["line"], now)
	}
	return Hit{}, false
}

func textHit(v any, now time.Time) (Hit, bool) {
	s, _ := v.(string)
	if s == "" {
		return Hit{}, false
	}
	return DetectLines(s, now)
}
