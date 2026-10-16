package telegram

import (
	"context"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// approvalWait is how long a Telegram approval waits for a tap. It matches
// the 5 minute approval timeout of the spec. When it runs out the approval
// is denied, never allowed.
const approvalWait = 5 * time.Minute

// DefaultQuestionWait is how long a question waits for a phone answer.
const DefaultQuestionWait = 5 * time.Minute

// Host is a tools.Host that puts each approval, question and plan review to
// the local host and to the paired Telegram chats at once.
//
// Approvals: the first answer wins. A phone answer is the decision (approve,
// approve for session, or deny, with the reason typed). A phone failure
// (unreachable, timed out, tab closed, surface turned off) is a deny with the
// reason, and the local prompt is cancelled. When no chat is paired the phone
// is not involved and the local host decides alone.
//
// Questions: a phone answer wins; a phone failure leaves the question to the
// local host, because a question is not a permission.
type Host struct {
	bot          *Bot
	owner        any
	inner        tools.Host
	questionWait time.Duration
}

// NewHost wraps inner with the Telegram surface. questionWait <= 0 uses
// DefaultQuestionWait. inner must not be nil.
func NewHost(bot *Bot, inner tools.Host, questionWait time.Duration) *Host {
	if questionWait <= 0 {
		questionWait = DefaultQuestionWait
	}
	return &Host{bot: bot, inner: inner, questionWait: questionWait}
}

// Approve races the local host against the phone. See the type comment.
func (h *Host) Approve(ctx context.Context, ap tools.Approval) tools.Decision {
	remoteCtx, cancelRemote := context.WithTimeout(ctx, approvalWait)
	defer cancelRemote()
	remote := make(chan outcome, 1)
	go func() { remote <- h.bot.askApproval(remoteCtx, h.owner, ap) }()

	if !localAnswers(h.inner) {
		o := <-remote
		if o.notAsked {
			return h.inner.Approve(ctx, ap)
		}
		return settleApproval(ctx, o)
	}

	localCtx, cancelLocal := context.WithCancel(ctx)
	defer cancelLocal()
	local := make(chan tools.Decision, 1)
	go func() { local <- h.inner.Approve(localCtx, ap) }()
	select {
	case d := <-local:
		cancelRemote()
		return d
	case o := <-remote:
		if o.notAsked {
			return <-local
		}
		if o.answer != "" || o.failed != "" {
			cancelLocal()
			return settleApproval(ctx, o)
		}
		// Cancelled: the local host is answering, so wait for it.
		return <-local
	}
}

// settleApproval turns a phone outcome into the decision the agent gets, and
// records the reason on the context for the tool result.
func settleApproval(ctx context.Context, o outcome) tools.Decision {
	if o.failed != "" {
		tools.SetApprovalReason(ctx, o.failed+" The request was denied, so nothing ran.")
		return tools.Deny
	}
	if o.reason != "" {
		tools.SetApprovalReason(ctx, o.reason)
	}
	switch o.answer {
	case "approve":
		return tools.AllowOnce
	case "session":
		return tools.AllowSession
	}
	return tools.Deny
}

// Ask races the local host against the phone for a question or plan review.
func (h *Host) Ask(ctx context.Context, q tools.Question) (string, error) {
	remoteCtx, cancelRemote := context.WithTimeout(ctx, h.questionWait)
	defer cancelRemote()
	remote := make(chan outcome, 1)
	go func() { remote <- h.bot.askQuestion(remoteCtx, h.owner, q) }()

	if !localAnswers(h.inner) {
		if o := <-remote; o.answer != "" {
			return o.answer, nil
		}
		return h.inner.Ask(ctx, q)
	}

	type localResult struct {
		text string
		err  error
	}
	localCtx, cancelLocal := context.WithCancel(ctx)
	defer cancelLocal()
	local := make(chan localResult, 1)
	go func() {
		text, err := h.inner.Ask(localCtx, q)
		local <- localResult{text, err}
	}()
	select {
	case r := <-local:
		cancelRemote()
		return r.text, r.err
	case o := <-remote:
		if o.answer != "" {
			cancelLocal()
			return o.answer, nil
		}
		r := <-local
		return r.text, r.err
	}
}

// Progress passes the line to the local host and, throttled, to the phone.
func (h *Host) Progress(callID, text string) {
	h.inner.Progress(callID, text)
	h.bot.Progress(text)
}

// localAnswers reports whether the local host can answer a prompt now. A
// host that does not say is treated as interactive.
func localAnswers(h tools.Host) bool {
	if i, ok := h.(interface{ Interactive() bool }); ok {
		return i.Interactive()
	}
	return true
}
