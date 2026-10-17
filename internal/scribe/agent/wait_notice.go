package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Notifications for waits on the user (spec 05 §7.3). A turn that stops on
// an error or stops early already notifies. The user also has to act when a
// tool asks for approval or a question, and that is when they are most
// likely to be away from the screen, so the Notification hook fires then too.

// noticingHost reports each approval and question to the Notification hook,
// then waits on the host as waitHost does.
type noticingHost struct {
	waitHost
	notify func(ctx context.Context, msg string)
}

func (h noticingHost) Approve(ctx context.Context, a tools.Approval) tools.Decision {
	h.notify(ctx, approvalNotice(a))
	return h.waitHost.Approve(ctx, a)
}

func (h noticingHost) Ask(ctx context.Context, q tools.Question) (string, error) {
	h.notify(ctx, questionNotice(q))
	return h.waitHost.Ask(ctx, q)
}

// notifyWait sends one wait notice to the Notification hook. The hook runs in
// its own goroutine so a slow hook does not hold up the approval prompt, which
// the tool shows only after this returns.
func (a *Agent) notifyWait(ctx context.Context, msg string) {
	n, ok := a.opts.Hooks.(notifier)
	if !ok {
		return
	}
	go n.Notification(context.WithoutCancel(ctx), msg)
}

// approvalNotice names what the user is asked to approve.
func approvalNotice(a tools.Approval) string {
	title := strings.TrimSpace(a.Title)
	if title == "" {
		title = a.Tool
	}
	msg := "Approval needed: " + title
	if d := strings.TrimSpace(a.Detail); d != "" {
		msg += ": " + fragment(d, 120)
	}
	return msg
}

// questionNotice says a question is waiting. A plan review is a question too,
// but the user is asked to read the plan, so it says that.
func questionNotice(q tools.Question) string {
	if q.Kind == "plan_review" {
		return "Plan ready for your review"
	}
	if strings.TrimSpace(q.Question) == "" {
		return "Question for you"
	}
	return "Question for you: " + fragment(strings.TrimSpace(q.Question), 200)
}

// newBadArg records a tool call whose arguments were not a JSON object.
func newBadArg(raw string) badArg {
	return badArg{raw: raw, reason: argsProblem(raw), truncated: looksTruncated(raw)}
}

// badArgResult is the error the model gets for a call it sent with broken
// arguments. A call cut off mid-object also gets the "truncated input" lead
// (spec 05 §5.4), so the model resends the whole call instead of changing
// it. Both forms keep "not valid JSON" and the start of the raw text.
func badArgResult(tool string, bad badArg) tools.Result {
	what := fmt.Sprintf("the arguments for %s are not valid JSON (%s)", tool, bad.reason)
	if bad.truncated {
		what = fmt.Sprintf("truncated input: the arguments for %s were cut off before the JSON object closed, so they are not valid JSON (%s)", tool, bad.reason)
	}
	return tools.Errorf("%s. The raw input starts with %q. The call was not run. Resend the complete call with valid JSON arguments.",
		what, fragment(bad.raw, 200))
}

// looksTruncated reports whether raw is an object that was cut off: it ends
// inside a string or with brackets still open, and nothing before the cut
// closes a bracket it did not open. It only sees what a scan can tell, so a
// stray token outside a string before the cut still reads as truncated.
func looksTruncated(raw string) bool {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		return false
	}
	var closers []byte
	inString, escaped := false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			closers = append(closers, '}')
		case '[':
			closers = append(closers, ']')
		case '}', ']':
			if len(closers) == 0 || closers[len(closers)-1] != c {
				return false
			}
			closers = closers[:len(closers)-1]
		}
	}
	return inString || len(closers) > 0
}
