package autonomy

import "fmt"

// Backlog catalog keys are display references, never proposal identities or
// admission authority. A planner may copy one from the catalog. Accept that
// single known field (string or null) only on backlog input and discard it before validation;
// retain the original report bytes through ApplyReport's normal evidence path.
// All other fields, duplicate JSON keys and semantic checks stay strict.
func decodePlanReport(raw []byte, out *PlanReport) error {
	var input struct {
		Items   []Proposal `json:"items"`
		Backlog []struct {
			Proposal
			CatalogKey string `json:"key,omitempty"`
		} `json:"backlog,omitempty"`
		NoWork *NoWorkReport `json:"no_work,omitempty"`
	}
	if err := decodeStrict(raw, &input); err != nil {
		return err
	}
	result := PlanReport{Items: input.Items, NoWork: input.NoWork}
	if input.Backlog != nil {
		result.Backlog = make([]Proposal, 0, len(input.Backlog))
	}
	for _, entry := range input.Backlog {
		if len(entry.CatalogKey) > 128 {
			return fmt.Errorf("backlog catalog key exceeds 128 bytes")
		}
		result.Backlog = append(result.Backlog, entry.Proposal)
	}
	*out = result
	return nil
}
