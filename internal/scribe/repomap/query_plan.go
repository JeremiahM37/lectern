package repomap

import (
	"errors"
	"fmt"
	"strings"
)

// Limits for soul_query (spec 04 2.7). They keep one query quick and its
// answer small enough to sit comfortably in the model's context.
const (
	maxQueryStages      = 12        // stages in one pipeline
	maxQueryFiles       = 200       // files a working set may hold after a stage
	maxQuerySearchReads = 2000      // files a search stage may read from disk
	maxQueryLines       = 300       // lines of outline output
	maxQueryReadLines   = 200       // lines shown per file when no range is given
	maxQueryFileBytes   = 256 << 10 // larger files are not read by read
	maxQueryOutput      = 16 << 10  // bytes of text one query may return
)

// Stage operations (spec 04 2.7).
const (
	opSearch  = "search"
	opFind    = "find"
	opFilter  = "filter"
	opDeps    = "deps"
	opOutline = "outline"
	opRead    = "read"
	opLimit   = "limit"
)

// queryOps lists the valid op values in the order the error messages show.
var queryOps = []string{opSearch, opFind, opFilter, opDeps, opOutline, opRead, opLimit}

// queryStage is one step of a pipeline. Each op reads only its own fields and
// ignores the rest, so a stage object can be sent with stray keys.
type queryStage struct {
	Op           string      `json:"op"`
	Pattern      string      `json:"pattern"`
	Query        string      `json:"query"`
	Ext          string      `json:"ext"`
	PathContains string      `json:"pathContains"`
	Recent       bool        `json:"recent"`
	Direction    string      `json:"direction"`
	N            int         `json:"n"`
	Ranges       []lineRange `json:"ranges"`
}

// lineRange is a 1-based, inclusive span of lines for the read op.
type lineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// isTerminal reports whether op produces the answer rather than narrowing the
// working set. A terminal stage must be the last one.
func isTerminal(op string) bool { return op == opOutline || op == opRead }

// validatePipeline checks every stage before any of them runs, so a bad
// pipeline is reported in full rather than one stage at a time.
func validatePipeline(stages []queryStage) error {
	if len(stages) == 0 {
		return errors.New(`pipeline is empty. Give at least one stage, for example [{"op":"find","query":"Refresh"}].`)
	}
	if len(stages) > maxQueryStages {
		return fmt.Errorf("pipeline has %d stages; the limit is %d.", len(stages), maxQueryStages)
	}
	for i, st := range stages {
		if err := checkStage(st, i == 0); err != nil {
			return fmt.Errorf("stage %d (%s): %v", i+1, st.Op, err)
		}
		if isTerminal(st.Op) && i != len(stages)-1 {
			return fmt.Errorf("stage %d: %s produces the answer, so it must be the last stage.", i+1, st.Op)
		}
	}
	return nil
}

// checkStage validates the fields one stage needs. first is true for the
// stage that opens the pipeline, which has no file set to walk from yet.
func checkStage(st queryStage, first bool) error {
	switch st.Op {
	case opSearch:
		if strings.TrimSpace(st.Pattern) == "" {
			return errors.New("pattern is required: the literal text to look for")
		}
	case opFind:
		if strings.TrimSpace(st.Query) == "" {
			return errors.New("query is required: a symbol name, a fragment, or a file name")
		}
	case opFilter:
		if st.Ext == "" && st.PathContains == "" && !st.Recent {
			return errors.New("filter needs ext, pathContains or recent")
		}
	case opDeps:
		if first {
			return errors.New("deps needs files to start from. Put find, search or filter before it.")
		}
		if st.Direction != "imports" && st.Direction != "imported_by" {
			return errors.New(`direction must be "imports" (what the files import) or "imported_by" (what imports them)`)
		}
	case opLimit:
		if st.N < 1 || st.N > maxQueryFiles {
			return fmt.Errorf("n must be between 1 and %d", maxQueryFiles)
		}
	case opRead:
		for _, r := range st.Ranges {
			if r.Start < 1 || r.End < r.Start {
				return fmt.Errorf("range %d-%d is invalid: start must be at least 1 and end at least start", r.Start, r.End)
			}
		}
	case opOutline:
	default:
		return fmt.Errorf("unknown op %q. Use one of: %s.", st.Op, strings.Join(queryOps, ", "))
	}
	return nil
}

// describeStage is the one-line form of a stage used in the trace the model
// reads back, so it names the arguments that narrowed the set.
func describeStage(st queryStage) string {
	switch st.Op {
	case opSearch:
		return fmt.Sprintf("search %q", st.Pattern)
	case opFind:
		return fmt.Sprintf("find %q", st.Query)
	case opFilter:
		var parts []string
		if st.Ext != "" {
			parts = append(parts, "ext "+normalizeExt(st.Ext))
		}
		if st.PathContains != "" {
			parts = append(parts, fmt.Sprintf("path contains %q", st.PathContains))
		}
		if st.Recent {
			parts = append(parts, "modified in the last 48 hours")
		}
		return "filter " + strings.Join(parts, ", ")
	case opDeps:
		return "deps " + st.Direction
	case opLimit:
		return fmt.Sprintf("limit %d", st.N)
	case opRead:
		if len(st.Ranges) > 0 {
			return fmt.Sprintf("read %d range(s)", len(st.Ranges))
		}
		return "read"
	}
	return st.Op
}

// normalizeExt makes ".go" and "go" mean the same extension, lower-cased.
func normalizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}
