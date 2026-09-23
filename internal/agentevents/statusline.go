package agentevents

import "encoding/json"

// StatuslinePayload is Claude Code's statusline JSON, as captured live from
// 2.1.281 in /mnt/bulk/lectern-events-ref/claude-statusline.json. Only the
// fields lectern uses are declared; everything else is ignored rather than
// rejected, since a newer Claude Code is free to add fields.
type StatuslinePayload struct {
	SessionID string `json:"session_id"`
	Model     struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Cost struct {
		TotalCostUSD      float64 `json:"total_cost_usd"`
		TotalLinesAdded   int     `json:"total_lines_added"`
		TotalLinesRemoved int     `json:"total_lines_removed"`
	} `json:"cost"`
	ContextWindow struct {
		// TotalInputTokens/TotalOutputTokens, despite the "total_" prefix, are
		// NOT lifetime-cumulative: in the reference fixture
		// total_input_tokens (36451) exactly equals current_usage's
		// input_tokens+cache_creation_input_tokens+cache_read_input_tokens
		// (2+13590+22859), and total_output_tokens equals current_usage's
		// output_tokens. Both describe the LAST request's token footprint —
		// which is also what used_percentage is computed from — not a
		// running sum across the conversation. See IngestStatusline for how
		// this is still turned into a usage_daily delta.
		TotalInputTokens  int `json:"total_input_tokens"`
		TotalOutputTokens int `json:"total_output_tokens"`
		ContextWindowSize int `json:"context_window_size"`
		UsedPercentage    int `json:"used_percentage"`
	} `json:"context_window"`
	RateLimits struct {
		FiveHour struct {
			UsedPercentage int     `json:"used_percentage"`
			ResetsAt       float64 `json:"resets_at"`
		} `json:"five_hour"`
		SevenDay struct {
			UsedPercentage int     `json:"used_percentage"`
			ResetsAt       float64 `json:"resets_at"`
		} `json:"seven_day"`
	} `json:"rate_limits"`
}

// ParseStatusline decodes a statusline payload. A malformed body is not fatal
// to the caller — see IngestStatusline, which degrades to "touch hook_seen_at
// only" rather than erroring the target's background curl.
func ParseStatusline(body []byte) (*StatuslinePayload, error) {
	var p StatuslinePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
