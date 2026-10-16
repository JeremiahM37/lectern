package fs

import (
	"fmt"
	"strings"
)

// maxDiffD bounds the Myers search. Past it UnifiedDiff shows the whole old
// text as removed and the whole new text as added. The counts stay right.
const maxDiffD = 2000

type opKind uint8

const (
	opEq opKind = iota
	opDel
	opIns
)

type diffOp struct {
	kind opKind
	text string
}

// UnifiedDiff returns a unified diff from old to new with three lines of
// context, and the number of lines added and removed. Empty old is treated
// as a new file, so the header says /dev/null. The diff is empty when the
// texts are equal.
func UnifiedDiff(path, old, new string) (string, int, int) {
	a := splitDiffLines(old)
	b := splitDiffLines(new)
	ops, ok := myersDiff(a, b)
	if !ok {
		ops = ops[:0]
		for _, l := range a {
			ops = append(ops, diffOp{opDel, l})
		}
		for _, l := range b {
			ops = append(ops, diffOp{opIns, l})
		}
	}
	added, removed := 0, 0
	for _, op := range ops {
		switch op.kind {
		case opIns:
			added++
		case opDel:
			removed++
		}
	}
	if added == 0 && removed == 0 {
		return "", 0, 0
	}
	var sb strings.Builder
	oldName := "a/" + path
	if old == "" {
		oldName = "/dev/null"
	}
	fmt.Fprintf(&sb, "--- %s\n+++ b/%s\n", oldName, path)
	for _, h := range diffHunks(ops, 3) {
		sb.WriteString(h)
	}
	return sb.String(), added, removed
}

func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// myersDiff is the greedy O(ND) Myers algorithm. It returns ok=false when the
// edit distance exceeds maxDiffD.
func myersDiff(a, b []string) ([]diffOp, bool) {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil, true
	}
	maxD := n + m
	off := maxD + 1
	v := make([]int, 2*maxD+4)
	var trace [][]int // trace[d] holds v[k] for k in [-d, d] after step d
	dFinal := -1
search:
	for d := 0; d <= maxD; d++ {
		if d > maxDiffD {
			return nil, false
		}
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				dFinal = d
				break search
			}
		}
		snap := make([]int, 2*d+1)
		for k := -d; k <= d; k++ {
			snap[k+d] = v[off+k]
		}
		trace = append(trace, snap)
	}

	x, y := n, m
	var rev []diffOp
	for d := dFinal; d > 0; d-- {
		prev := trace[d-1]
		pv := func(k int) int { return prev[k+d-1] }
		k := x - y
		var prevK int
		if k == -d || (k != d && pv(k-1) < pv(k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := pv(prevK)
		prevY := prevX - prevK
		down := prevK == k+1
		var midX, midY int
		if down {
			midX, midY = prevX, prevY+1
		} else {
			midX, midY = prevX+1, prevY
		}
		for x > midX && y > midY {
			rev = append(rev, diffOp{opEq, a[x-1]})
			x--
			y--
		}
		if down {
			rev = append(rev, diffOp{opIns, b[y-1]})
		} else {
			rev = append(rev, diffOp{opDel, a[x-1]})
		}
		x, y = prevX, prevY
	}
	for x > 0 && y > 0 {
		rev = append(rev, diffOp{opEq, a[x-1]})
		x--
		y--
	}
	ops := make([]diffOp, len(rev))
	for i, op := range rev {
		ops[len(rev)-1-i] = op
	}
	return ops, true
}

// diffHunks groups changes into hunks with ctx lines of context.
func diffHunks(ops []diffOp, ctx int) []string {
	oldBefore := make([]int, len(ops)+1)
	newBefore := make([]int, len(ops)+1)
	for i, op := range ops {
		oldBefore[i+1], newBefore[i+1] = oldBefore[i], newBefore[i]
		if op.kind != opIns {
			oldBefore[i+1]++
		}
		if op.kind != opDel {
			newBefore[i+1]++
		}
	}
	var out []string
	i := 0
	for i < len(ops) {
		if ops[i].kind == opEq {
			i++
			continue
		}
		// Extend the group while the next change is close enough to share context.
		last := i
		for {
			k := last + 1
			for k < len(ops) && ops[k].kind == opEq {
				k++
			}
			if k < len(ops) && k-last-1 <= 2*ctx {
				last = k
				continue
			}
			break
		}
		start := i - ctx
		if start < 0 {
			start = 0
		}
		end := last + ctx
		if end > len(ops)-1 {
			end = len(ops) - 1
		}

		oc := oldBefore[end+1] - oldBefore[start]
		nc := newBefore[end+1] - newBefore[start]
		os := oldBefore[start] + 1
		ns := newBefore[start] + 1
		if oc == 0 {
			os--
		}
		if nc == 0 {
			ns--
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", os, oc, ns, nc)
		for _, op := range ops[start : end+1] {
			switch op.kind {
			case opEq:
				sb.WriteString(" " + op.text + "\n")
			case opDel:
				sb.WriteString("-" + op.text + "\n")
			case opIns:
				sb.WriteString("+" + op.text + "\n")
			}
		}
		out = append(out, sb.String())
		i = end + 1
	}
	return out
}
