package effects

import (
	"context"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// Result pairs a claim with the verdict of its probe.
type Result struct {
	Claim   Claim
	Verdict Verdict
}

// Evaluate extracts the claims in message, drops repeats of the same kind and
// target, and probes each one in order.
func Evaluate(ctx context.Context, ex executor.Executor, dir, message string, facts Facts) []Result {
	claims := dedupe(Extract(message))
	out := make([]Result, 0, len(claims))
	for _, c := range claims {
		out = append(out, Result{Claim: c, Verdict: Probe(ctx, ex, dir, c, facts)})
	}
	return out
}
