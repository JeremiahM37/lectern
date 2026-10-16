package exec

import (
	"regexp"
	"strconv"
	"strings"
)

// Diagnostic is one error or failure location found in tool output.
type Diagnostic struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Msg  string `json:"msg"`
}

// maxDiagnostics caps what one result reports, so a flood of errors does not
// crowd out the model's context.
const maxDiagnostics = 50

var (
	reGoFail     = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	rePyFailed   = regexp.MustCompile(`^FAILED (\S+)`)
	reJestFail   = regexp.MustCompile(`^\s*● (.+?)\s*$`)
	reCargoFail  = regexp.MustCompile(`^test (\S+) \.\.\. FAILED$`)
	reTscError   = regexp.MustCompile(`^(.+?)\((\d+),\d+\): error (TS\d+): (.*)$`)
	reCargoErr   = regexp.MustCompile(`^error(\[\w+\])?: (.*)$`)
	reCargoLoc   = regexp.MustCompile(`^\s*--> (.+?):(\d+):\d+\s*$`)
	reGoLoc      = regexp.MustCompile(`^\s*(\S+\.go):(\d+)(?::\d+)?:?\s*(.*)$`)
	reGenericLoc = regexp.MustCompile(`^(\S+?\.[A-Za-z]{1,5}):(\d+)(?::\d+)?:\s*(.*)$`)
)

// parseDiagnostics reads the output of go test, pytest, jest and vitest,
// cargo, tsc, and generic file:line:message compilers and linters. It returns
// the error locations and the names of failing tests, in output order.
func parseDiagnostics(out string) (diags []Diagnostic, failed []string) {
	seen := map[string]bool{}
	add := func(d Diagnostic) {
		key := d.File + "\x00" + strconv.Itoa(d.Line) + "\x00" + d.Msg
		if d.File == "" || seen[key] || len(diags) >= maxDiagnostics {
			return
		}
		seen[key] = true
		diags = append(diags, d)
	}
	var cargoMsg string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")

		if m := reGoFail.FindStringSubmatch(line); m != nil {
			failed = append(failed, m[1])
			continue
		}
		if m := rePyFailed.FindStringSubmatch(line); m != nil {
			failed = append(failed, m[1])
			continue
		}
		if m := reCargoFail.FindStringSubmatch(line); m != nil {
			failed = append(failed, m[1])
			continue
		}
		if m := reTscError.FindStringSubmatch(line); m != nil {
			add(Diagnostic{File: m[1], Line: lineNo(m[2]), Msg: m[3] + ": " + m[4]})
			continue
		}
		if m := reCargoErr.FindStringSubmatch(line); m != nil {
			cargoMsg = m[2]
			continue
		}
		if m := reCargoLoc.FindStringSubmatch(line); m != nil {
			add(Diagnostic{File: m[1], Line: lineNo(m[2]), Msg: cargoMsg})
			cargoMsg = ""
			continue
		}
		if m := reGoLoc.FindStringSubmatch(line); m != nil {
			add(Diagnostic{File: m[1], Line: lineNo(m[2]), Msg: m[3]})
			continue
		}
		if m := reGenericLoc.FindStringSubmatch(line); m != nil && !strings.Contains(m[1], "://") {
			add(Diagnostic{File: m[1], Line: lineNo(m[2]), Msg: m[3]})
			continue
		}
	}
	// jest and vitest print "● Suite › test" headers; they are failures only
	// when the output also carries a FAIL summary, which avoids picking up
	// bullet lists from ordinary output.
	if len(failed) == 0 && strings.Contains(out, "FAIL ") {
		for _, line := range strings.Split(out, "\n") {
			if m := reJestFail.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
				failed = append(failed, m[1])
			}
		}
	}
	return diags, failed
}

// lineNo parses a line number from a regexp capture; the pattern guarantees digits.
func lineNo(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
