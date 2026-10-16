package telegram

import (
	"context"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// approvalWait is how long a Telegram approval waits before it stops
// counting. It matches the 5 minute approval timeout of the spec. Expiry
// never allows.
const approvalWait = 5 * time.Minute

// DefaultQuestionWait is how long a question waits for a phone answer.
const DefaultQuestionWait = 5 * time.Minute

// Host is a tools.Host that puts each approval, question and plan review to
// the local host and to the paired Telegram chats at once. The first real
// answer wins and the other prompt is cancelled. When no chat is paired, or
// none answers in time, the local host decides alone. A headless local host
// then gets its usual answer.
type Host struct {
	bot          *Bot
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
	type answer struct {
		d  tools.Decision
		ok bool
	}
	remoteCtx, cancelRemote := context.WithTimeout(ctx, approvalWait)
	defer cancelRemote()
	remote := make(chan answer, 1)
	go func() {
		d, ok := h.bot.askApproval(remoteCtx, ap)
		remote <- answer{d, ok}
	}()

	if !localAnswers(h.inner) {
		if a := <-remote; a.ok {
			return a.d
		}
		return h.inner.Approve(ctx, ap)
	}

	localCtx, cancelLocal := context.WithCancel(ctx)
	defer cancelLocal()
	local := make(chan tools.Decision, 1)
	go func() { local <- h.inner.Approve(localCtx, ap) }()
	select {
	case d := <-local:
		cancelRemote()
		return d
	case a := <-remote:
		if a.ok {
			cancelLocal()
			return a.d
		}
		return <-local
	}
}

// Ask races the local host against the phone for a question or plan review.
func (h *Host) Ask(ctx context.Context, q tools.Question) (string, error) {
	type answer struct {
		text string
		ok   bool
	}
	remoteCtx, cancelRemote := context.WithTimeout(ctx, h.questionWait)
	defer cancelRemote()
	remote := make(chan answer, 1)
	go func() {
		text, ok := h.bot.askQuestion(remoteCtx, q)
		remote <- answer{text, ok}
	}()

	if !localAnswers(h.inner) {
		if a := <-remote; a.ok {
			return a.text, nil
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
	case a := <-remote:
		if a.ok {
			cancelLocal()
			return a.text, nil
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
