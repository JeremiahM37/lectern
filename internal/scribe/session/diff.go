package session

import (
	"bytes"
	"fmt"
	"strings"
)

// maxLCSCells bounds the middle section compared line by line. Past it the
// middle is shown as a full replacement, which is correct but not minimal.
const maxLCSCells = 4_000_000

const diffContext = 3

// fileDiff renders a unified diff of one file from (aExists, a) to (bExists, b).
// Binary content is reported rather than diffed. It returns "" when nothing
// changed.
func fileDiff(name string, aExists bool, a []byte, bExists bool, b []byte) string {
	if aExists == bExists && bytes.Equal(a, b) {
		return ""
	}
	fromName, toName := "a/"+name, "b/"+name
	if !aExists {
		fromName = "/dev/null"
	}
	if !bExists {
		toName = "/dev/null"
	}
	if bytes.IndexByte(a, 0) >= 0 || bytes.IndexByte(b, 0) >= 0 {
		return fmt.Sprintf("Binary files %s and %s differ\n", fromName, toName)
	}
	ops := diffOps(splitKeepEOL(string(a)), splitKeepEOL(string(b)))
	body := renderHunks(ops)
	if body == "" {
		return ""
	}
	return fmt.Sprintf("--- %s\n+++ %s\n", fromName, toName) + body
}

// splitKeepEOL splits s into lines that keep their newline. The last line may
// lack one.
func splitKeepEOL(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.SplitAfter(s, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

type diffOp struct {
	kind byte // ' ' context, '-' removed, '+' added
	line string
}

// diffOps computes an edit script. Common prefix and suffix are trimmed first,
// then an LCS table is built for the middle.
func diffOps(a, b []string) []diffOp {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	ma, mb := a[p:len(a)-s], b[p:len(b)-s]

	var ops []diffOp
	for _, l := range a[:p] {
		ops = append(ops, diffOp{' ', l})
	}
	if len(ma)*len(mb) > maxLCSCells {
		for _, l := range ma {
			ops = append(ops, diffOp{'-', l})
		}
		for _, l := range mb {
			ops = append(ops, diffOp{'+', l})
		}
	} else {
		ops = append(ops, lcsOps(ma, mb)...)
	}
	for _, l := range a[len(a)-s:] {
		ops = append(ops, diffOp{' ', l})
	}
	return ops
}

func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	w := m + 1
	dp := make([]int32, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i*w+j] = dp[(i+1)*w+j+1] + 1
			} else if dp[(i+1)*w+j] >= dp[i*w+j+1] {
				dp[i*w+j] = dp[(i+1)*w+j]
			} else {
				dp[i*w+j] = dp[i*w+j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case dp[(i+1)*w+j] >= dp[i*w+j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

// renderHunks groups changes with diffContext lines of context and formats them.
func renderHunks(ops []diffOp) string {
	n := len(ops)
	// oldPos[i] and newPos[i] are the lines consumed before op i.
	oldPos := make([]int, n+1)
	newPos := make([]int, n+1)
	for i, op := range ops {
		oldPos[i+1] = oldPos[i]
		newPos[i+1] = newPos[i]
		if op.kind != '+' {
			oldPos[i+1]++
		}
		if op.kind != '-' {
			newPos[i+1]++
		}
	}
	include := make([]bool, n)
	for i, op := range ops {
		if op.kind == ' ' {
			continue
		}
		lo, hi := i-diffContext, i+diffContext
		if lo < 0 {
			lo = 0
		}
		if hi >= n {
			hi = n - 1
		}
		for k := lo; k <= hi; k++ {
			include[k] = true
		}
	}
	var out strings.Builder
	for i := 0; i < n; {
		if !include[i] {
			i++
			continue
		}
		s := i
		for i < n && include[i] {
			i++
		}
		e := i
		oldCount, newCount := 0, 0
		for _, op := range ops[s:e] {
			if op.kind != '+' {
				oldCount++
			}
			if op.kind != '-' {
				newCount++
			}
		}
		oldStart, newStart := oldPos[s]+1, newPos[s]+1
		if oldCount == 0 {
			oldStart = oldPos[s]
		}
		if newCount == 0 {
			newStart = newPos[s]
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", rangeText(oldStart, oldCount), rangeText(newStart, newCount))
		for _, op := range ops[s:e] {
			out.WriteByte(op.kind)
			out.WriteString(op.line)
			if !strings.HasSuffix(op.line, "\n") {
				out.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
	return out.String()
}

func rangeText(start, count int) string {
	if count == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}
