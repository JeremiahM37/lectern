package outcomes

import (
	"sort"
	"strconv"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// GroupKind is what GET /api/outcomes?group= accepts.
type GroupKind string

const (
	GroupAgent   GroupKind = "agent"
	GroupModel   GroupKind = "model"
	GroupProject GroupKind = "project"
)

// ValidGroup reports whether g is one of the three supported groupings.
func ValidGroup(g string) bool {
	switch GroupKind(g) {
	case GroupAgent, GroupModel, GroupProject:
		return true
	default:
		return false
	}
}

// Row is one line of the Outcomes table: everything GET /api/outcomes
// reports for one agent, one model, or one project, over the requested
// window.
type Row struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Attempts int     `json:"attempts"`
	Sessions int     `json:"sessions"`
	CostUSD  float64 `json:"cost_usd"`
	// Checked/Passed are facts with a known check_passed (not nil) / of
	// those, the ones that passed.
	Checked    int   `json:"checked"`
	Passed     int   `json:"passed"`
	Accepted   int   `json:"accepted"`
	LinesKept  int64 `json:"lines_kept"`
	EvalTotal  int   `json:"eval_total"`
	EvalPassed int   `json:"eval_passed"`
	// Partial is true when any fact contributing to this row has no real
	// cost figure at all (cost_source == "", i.e. not even an estimate) —
	// the UI's "derived from partial data" flag (docs/outcomes.md).
	Partial bool `json:"partial"`
	// Estimated is true when any fact's cost came from the optional price
	// table rather than a measured source.
	Estimated bool `json:"estimated"`

	// Derived metrics — nil when the denominator is zero, so the UI can
	// render "—" instead of a divide-by-zero artifact.
	CostPerPass       *float64 `json:"cost_per_pass,omitempty"`
	CostPerAccepted   *float64 `json:"cost_per_accepted,omitempty"`
	CostPer100Lines   *float64 `json:"cost_per_100_lines,omitempty"`
	PassesPer10USD    *float64 `json:"passes_per_10usd,omitempty"`
	MedianTimeToPassS *float64 `json:"median_time_to_pass_s,omitempty"`
}

// Aggregate groups every outcome_facts row dated at or after cutoffDate by
// group (agent|model|project), computing the derived cost-efficiency
// metrics GET /api/outcomes reports. Call Rebuild first — this only reads.
func Aggregate(db *store.DB, cutoffDate string, group GroupKind, projectName map[int64]string) ([]Row, error) {
	facts, err := db.OutcomeFactsSince(cutoffDate)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*Row{}
	timesByKey := map[string][]float64{}
	var order []string

	keyLabel := func(f *store.OutcomeFact) (key, label string) {
		switch group {
		case GroupModel:
			m := f.Model
			if m == "" {
				m = "(unknown)"
			}
			return m, m
		case GroupProject:
			if f.ProjectID == nil {
				return "none", "Unassigned"
			}
			name := projectName[*f.ProjectID]
			if name == "" {
				name = "(unknown project)"
			}
			k := strconv.FormatInt(*f.ProjectID, 10)
			return k, name
		default: // GroupAgent
			a := f.Agent
			if a == "" {
				a = "(unknown)"
			}
			return a, a
		}
	}

	for _, f := range facts {
		key, label := keyLabel(f)
		row, ok := byKey[key]
		if !ok {
			row = &Row{Key: key, Label: label}
			byKey[key] = row
			order = append(order, key)
		}
		if f.Scope == "attempt" {
			row.Attempts++
		} else {
			row.Sessions++
		}
		row.CostUSD += f.CostUSD
		switch f.CostSource {
		case "":
			row.Partial = true
		case "estimated":
			row.Estimated = true
		}
		if f.CheckPassed != nil {
			row.Checked++
			if *f.CheckPassed {
				row.Passed++
			}
		}
		if f.Accepted {
			row.Accepted++
			if f.LinesKept != nil {
				row.LinesKept += *f.LinesKept
			}
		}
		if f.EvalPass != nil {
			row.EvalTotal++
			if *f.EvalPass {
				row.EvalPassed++
			}
		}
		if f.TimeToPassS != nil {
			timesByKey[key] = append(timesByKey[key], *f.TimeToPassS)
		}
	}

	sort.Strings(order)
	out := make([]Row, 0, len(order))
	for _, k := range order {
		row := *byKey[k]
		if row.Passed > 0 {
			v := row.CostUSD / float64(row.Passed)
			row.CostPerPass = &v
			if row.CostUSD > 0 {
				p := float64(row.Passed) / row.CostUSD * 10
				row.PassesPer10USD = &p
			}
		}
		if row.Accepted > 0 {
			v := row.CostUSD / float64(row.Accepted)
			row.CostPerAccepted = &v
		}
		if row.LinesKept > 0 {
			v := row.CostUSD * 100 / float64(row.LinesKept)
			row.CostPer100Lines = &v
		}
		if times := timesByKey[k]; len(times) > 0 {
			v := median(times)
			row.MedianTimeToPassS = &v
		}
		out = append(out, row)
	}
	// Highest spend first — the same "what is costing the most" framing
	// GET /api/usage's by_agent_model already uses.
	sort.Slice(out, func(i, j int) bool { return out[i].CostUSD > out[j].CostUSD })
	return out, nil
}

func median(vals []float64) float64 {
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
