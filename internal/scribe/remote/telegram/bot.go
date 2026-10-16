package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/redact"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const (
	// maxMessage is Telegram's limit on message text, with some headroom.
	maxMessage = 4000
	// staleAfter drops messages older than this, so a replayed or delayed
	// update cannot answer a question that has moved on.
	staleAfter = 60 * time.Second
	// minSendGap is the least time between messages to one chat.
	minSendGap = time.Second
)

// Options configures a Bot.
type Options struct {
	// Token is the bot token. Config reads it from SCRIBE_TELEGRAM_TOKEN or
	// the key store (config.Secret).
	Token   string
	APIBase string
	HTTP    *http.Client
	// Allow is the per-chat allowlist. Required.
	Allow *remote.Allowlist
	// Pairing mints and redeems pairing codes. Nil gets the default.
	Pairing *remote.Pairing
	// ProgressEvery is the least time between progress lines (default 20s).
	ProgressEvery time.Duration
	// SendGap is the least time between messages to one chat (default 1s).
	SendGap time.Duration
	// Logf receives poll and send problems. Nil discards them.
	Logf func(format string, args ...any)
	// Deliver takes a plain message from an allowed chat that is not an answer
	// to a waiting question. It submits the message to Scribe (or steers the
	// running turn) and returns the short reply for the chat, or "". Nil
	// means messages are not taken.
	Deliver func(text string) string
	// Stop aborts the running turn of the bound tab for /stop. It returns the
	// reply for the chat. Nil means /stop is not taken.
	Stop func() string
}

// Bot is the Telegram surface. It long-polls for taps and replies, resolves
// the questions and approvals a Host has sent, and posts notes. Create it
// with NewBot, start it with Run or Start, and wrap an app's host with
// NewHost.
type Bot struct {
	client   *Client
	allow    *remote.Allowlist
	pairing  *remote.Pairing
	progress *remote.Throttle
	done     *remote.Throttle
	redact   *redact.Redactor
	deliver  func(string) string
	stop     func() string
	logf     func(string, ...any)
	sendGap  time.Duration
	now      func() time.Time

	startOnce sync.Once
	outbox    chan string

	mu      sync.Mutex
	seq     int
	pending map[string]*request

	sendMu   sync.Mutex
	lastSend map[string]time.Time
}

// request is one approval or question waiting for a tap or a reply.
type request struct {
	approval  bool
	options   []string
	allowFree bool
	seq       int
	reply     chan string
}

// NewBot validates opts and returns a Bot. It does not touch the network.
func NewBot(opts Options) (*Bot, error) {
	if strings.TrimSpace(opts.Token) == "" {
		return nil, errors.New("telegram: no bot token (set SCRIBE_TELEGRAM_TOKEN or the telegram key)")
	}
	if opts.Allow == nil {
		return nil, errors.New("telegram: no chat allowlist")
	}
	pairing := opts.Pairing
	if pairing == nil {
		pairing = remote.NewPairing(0)
	}
	every := opts.ProgressEvery
	if every <= 0 {
		every = 20 * time.Second
	}
	gap := opts.SendGap
	if gap <= 0 {
		gap = minSendGap
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Bot{
		client:   NewClient(opts.APIBase, opts.Token, opts.HTTP),
		allow:    opts.Allow,
		pairing:  pairing,
		progress: remote.NewThrottle(every),
		done:     remote.NewThrottle(2 * time.Second),
		redact:   redact.New(opts.Token),
		deliver:  opts.Deliver,
		stop:     opts.Stop,
		logf:     logf,
		sendGap:  gap,
		now:      time.Now,
		outbox:   make(chan string, 64),
		pending:  map[string]*request{},
		lastSend: map[string]time.Time{},
	}, nil
}

// MintPairCode returns a one-time code to show on the host. The person
// sends it to the bot from the chat they want paired. Minting replaces any
// earlier code.
func (b *Bot) MintPairCode() (string, time.Time, error) { return b.pairing.Mint() }

// Start runs the poll loop in the background. Only the first call starts it.
func (b *Bot) Start(ctx context.Context) {
	b.startOnce.Do(func() { go func() { _ = b.Run(ctx) }() })
}

// Run long-polls until ctx is done. It clears any webhook first, because a
// leftover webhook makes every poll fail with 409. Failures back off: 409
// doubles from 2s to a 30s cap, 429 waits retry_after, and anything else
// waits 3s.
func (b *Bot) Run(ctx context.Context) error {
	go b.drain(ctx)
	backoff := 2 * time.Second
	for {
		err := b.client.DeleteWebhook(ctx)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		b.logf("telegram: %v", err)
		if !sleepCtx(ctx, pollWait(err, &backoff)) {
			return ctx.Err()
		}
	}

	var offset int64
	streak := 0
	for {
		updates, err := b.client.GetUpdates(ctx, offset, 25)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			streak++
			if streak == 1 || streak == 2 || streak == 5 || streak == 10 || streak == 20 || streak == 50 {
				b.logf("telegram poll failing (%d in a row): %v", streak, err)
			}
			if !sleepCtx(ctx, pollWait(err, &backoff)) {
				return ctx.Err()
			}
			continue
		}
		streak = 0
		backoff = 2 * time.Second
		for _, u := range updates {
			offset = u.UpdateID + 1
			b.handle(ctx, u)
		}
	}
}

func pollWait(err error, backoff *time.Duration) time.Duration {
	var ae *APIError
	if errors.As(err, &ae) {
		switch ae.Code {
		case http.StatusConflict:
			w := *backoff
			*backoff = min(*backoff*2, 30*time.Second)
			return w
		case http.StatusTooManyRequests:
			if ae.RetryAfter > 0 {
				return ae.RetryAfter
			}
			return 5 * time.Second
		}
	}
	return 3 * time.Second
}

// handle applies the inbound rules: drop edited messages and channel posts
// (replay), forwards and via-bot messages (spoofed origin), and stale
// messages. Callback taps and messages from chats outside the allowlist
// get no reply, except a pairing attempt.
func (b *Bot) handle(ctx context.Context, u Update) {
	if u.EditedMessage != nil || u.ChannelPost != nil {
		return
	}
	if cq := u.CallbackQuery; cq != nil {
		b.handleCallback(ctx, cq)
		return
	}
	m := u.Message
	if m == nil || m.Chat.ID == 0 {
		return
	}
	if m.ForwardOrigin != nil || m.ViaBot != nil || m.SenderChat != nil {
		return
	}
	if b.now().Sub(time.Unix(m.Date, 0)) > staleAfter {
		return
	}
	text := strings.TrimSpace(m.Text)
	chat := strconv.FormatInt(m.Chat.ID, 10)
	if !b.allow.Allowed(chat) {
		b.tryPair(ctx, m.Chat.ID, chat, text)
		return
	}
	if text != "" {
		b.answerText(ctx, m.Chat.ID, text)
	}
}

// tryPair handles a message from a chat that is not allowed yet. "/pair
// CODE" always gets a reply. A bare six-character code is accepted quietly,
// so a stranger's chatter gets no reply at all.
func (b *Bot) tryPair(ctx context.Context, chatID int64, chat, text string) {
	fields := strings.Fields(text)
	command := len(fields) > 0 && strings.EqualFold(fields[0], "/pair")
	code := ""
	switch {
	case command:
		if len(fields) < 2 {
			b.reply(ctx, chatID, "Send /pair followed by the code shown on the host.")
			return
		}
		code = fields[1]
	case len(fields) == 1 && len(fields[0]) == 6:
		code = fields[0]
	default:
		return
	}
	if err := b.pairing.Redeem(chat, code); err != nil {
		if command {
			b.reply(ctx, chatID, err.Error())
		}
		return
	}
	if err := b.allow.Add(chat); err != nil {
		b.logf("telegram: paired chat not saved: %v", err)
		b.reply(ctx, chatID, "Paired for this session, but the pairing could not be saved. Check the host log.")
		return
	}
	b.reply(ctx, chatID, "Paired. This chat can now send Scribe messages, and answer its questions and approvals.")
}

// handleCallback resolves a tap on an approval or option button.
// Data is "apr:<id>:a|d" for approvals and "cb:<id>:<index>" for options.
func (b *Bot) handleCallback(ctx context.Context, cq *CallbackQuery) {
	if cq.Message == nil {
		return
	}
	if !b.allow.Allowed(strconv.FormatInt(cq.Message.Chat.ID, 10)) {
		return
	}
	parts := strings.SplitN(cq.Data, ":", 3)
	if len(parts) != 3 {
		b.answerCallback(ctx, cq.ID, "Unknown button")
		return
	}
	b.mu.Lock()
	r := b.pending[parts[1]]
	b.mu.Unlock()
	if r == nil {
		b.answerCallback(ctx, cq.ID, "This is no longer waiting for an answer")
		return
	}
	answer := ""
	switch {
	case parts[0] == "apr" && r.approval:
		switch parts[2] {
		case "a":
			answer = "approve"
		case "d":
			answer = "deny"
		}
	case parts[0] == "cb" && !r.approval:
		if i, err := strconv.Atoi(parts[2]); err == nil && i >= 0 && i < len(r.options) {
			answer = r.options[i]
		}
	}
	if answer == "" {
		b.answerCallback(ctx, cq.ID, "Unknown button")
		return
	}
	b.resolve(parts[1], answer)
	b.answerCallback(ctx, cq.ID, "Recorded")
}

// answerText treats a plain message as an answer when a question is waiting.
// Accepted forms: a number (1-based), "/ans <option or text>", or, for a
// question that allows it, free text. When no question is waiting, a plain
// message is a new message for Scribe (see forward). Other chat text while a
// question waits is ignored.
func (b *Bot) answerText(ctx context.Context, chatID int64, text string) {
	isAns := false
	arg := text
	if low := strings.ToLower(text); low == "/ans" || strings.HasPrefix(low, "/ans ") {
		isAns = true
		arg = strings.TrimSpace(text[len("/ans"):])
	}
	b.mu.Lock()
	id, r := b.oldestQuestionLocked()
	b.mu.Unlock()
	if r == nil {
		if isAns {
			b.reply(ctx, chatID, "Nothing is waiting for an answer.")
			return
		}
		b.forward(ctx, chatID, text)
		return
	}
	n, numErr := strconv.Atoi(arg)
	answer := ""
	switch {
	case numErr == nil && n >= 1 && n <= len(r.options):
		answer = r.options[n-1]
	case isAns && optionNamed(r.options, arg) != "":
		answer = optionNamed(r.options, arg)
	case r.allowFree && arg != "":
		answer = arg
	case isAns || numErr == nil:
		b.reply(ctx, chatID, "Reply with an option number, or /ans followed by one of the options.")
		return
	default:
		return
	}
	b.resolve(id, answer)
}

// helpText is the reply to /start and /help from a paired chat.
const helpText = "Send a message to give Scribe a task. If it is working, your message steers the turn. " +
	"When Scribe asks a question, reply with the option number, or /ans followed by the answer. " +
	"Approvals come with Approve and Deny buttons."

// forward hands a plain message from a paired chat to Scribe. Slash commands
// are for the host, not the phone, except /start and /help, which get the
// help text.
func (b *Bot) forward(ctx context.Context, chatID int64, text string) {
	if strings.HasPrefix(text, "/") {
		cmd := strings.ToLower(strings.Fields(text)[0])
		if cmd == "/start" || cmd == "/help" {
			b.reply(ctx, chatID, helpText)
			return
		}
		if cmd == "/stop" {
			if b.stop == nil {
				b.reply(ctx, chatID, "Stopping from Telegram is not available right now.")
				return
			}
			b.reply(ctx, chatID, b.stop())
			return
		}
		b.reply(ctx, chatID, "Scribe's slash commands are not available from Telegram. Send plain text, or /help.")
		return
	}
	if len(text) > maxMessage {
		b.reply(ctx, chatID, "That message is too long for one turn. Send it in shorter parts.")
		return
	}
	if b.deliver == nil {
		b.reply(ctx, chatID, "Scribe is not taking messages from Telegram right now.")
		return
	}
	if reply := b.deliver(text); reply != "" {
		b.reply(ctx, chatID, reply)
	}
}

// Chats returns the ids of every chat allowed on this surface, sorted.
func (b *Bot) Chats() []string { return b.allow.Members() }

func optionNamed(options []string, name string) string {
	for _, o := range options {
		if strings.EqualFold(o, name) {
			return o
		}
	}
	return ""
}

// oldestQuestionLocked returns the earliest question still waiting. Approvals
// are answered only by their buttons. The caller holds b.mu.
func (b *Bot) oldestQuestionLocked() (string, *request) {
	var id string
	var best *request
	for pid, r := range b.pending {
		if r.approval {
			continue
		}
		if best == nil || r.seq < best.seq {
			id, best = pid, r
		}
	}
	return id, best
}

// resolve delivers an answer to a waiting request. Only the first answer
// counts, because the request leaves the pending set.
func (b *Bot) resolve(id, answer string) bool {
	b.mu.Lock()
	r := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()
	if r == nil {
		return false
	}
	r.reply <- answer
	return true
}

// ask sends text (with buttons for approvals and options) to every paired
// chat and waits for the first answer. ok is false when no chat is paired,
// when ctx ends first, or when sending failed everywhere.
func (b *Bot) ask(ctx context.Context, text string, r *request) (string, bool) {
	members := b.allow.Members()
	if len(members) == 0 {
		return "", false
	}
	id, err := newID()
	if err != nil {
		b.logf("telegram: %v", err)
		return "", false
	}
	r.reply = make(chan string, 1)
	b.mu.Lock()
	b.seq++
	r.seq = b.seq
	b.pending[id] = r
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	kb := keyboardFor(id, r)
	sent := 0
	for _, chat := range members {
		chatID, err := strconv.ParseInt(chat, 10, 64)
		if err != nil {
			b.logf("telegram: allowlist entry %q is not a chat id", chat)
			continue
		}
		if err := b.send(ctx, chatID, text, kb); err != nil {
			if ctx.Err() != nil {
				return "", false
			}
			b.logf("telegram: %v", err)
			continue
		}
		sent++
	}
	if sent == 0 {
		return "", false
	}
	select {
	case ans := <-r.reply:
		return ans, true
	case <-ctx.Done():
		return "", false
	}
}

// keyboardFor builds the buttons: Approve and Deny, or one button per option.
// A question that takes free text with no options gets no keyboard.
func keyboardFor(id string, r *request) *InlineKeyboard {
	if r.approval {
		return &InlineKeyboard{Rows: [][]Button{{
			{Text: "Approve", CallbackData: "apr:" + id + ":a"},
			{Text: "Deny", CallbackData: "apr:" + id + ":d"},
		}}}
	}
	if len(r.options) == 0 {
		return nil
	}
	kb := &InlineKeyboard{}
	for i, o := range r.options {
		kb.Rows = append(kb.Rows, []Button{{Text: clip(o, 60), CallbackData: fmt.Sprintf("cb:%s:%d", id, i)}})
	}
	return kb
}

// Progress posts a progress line, at most once per ProgressEvery. It never
// blocks the caller.
func (b *Bot) Progress(text string) {
	if !b.progress.Allow("progress") {
		return
	}
	b.enqueue(text)
}

// OnEvent is an agent sink. It posts a note when a main-agent turn ends,
// or when it stops with an error. Subagent activity is not sent.
func (b *Bot) OnEvent(e agent.Event) {
	if e.AgentID != "" || e.Kind != agent.EvTurnDone {
		return
	}
	if e.Stats != nil && e.Stats.Error != "" && !e.Stats.Aborted {
		b.enqueueDone("Scribe stopped with an error: " + clip(e.Stats.Error, 500))
		return
	}
	text := "Scribe finished this turn."
	if t := strings.TrimSpace(e.Text); t != "" {
		text += "\n\n" + clip(t, 1500)
	}
	b.enqueueDone(text)
}

func (b *Bot) enqueueDone(text string) {
	if b.done.Allow("done") {
		b.enqueue(text)
	}
}

func (b *Bot) enqueue(text string) {
	select {
	case b.outbox <- text:
	default:
		// A full outbox drops the note rather than stalling the agent.
	}
}

// drain sends queued notes to every paired chat until ctx ends.
func (b *Bot) drain(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case text := <-b.outbox:
			for _, chat := range b.allow.Members() {
				chatID, err := strconv.ParseInt(chat, 10, 64)
				if err != nil {
					continue
				}
				if err := b.send(ctx, chatID, text, nil); err != nil && ctx.Err() == nil {
					b.logf("telegram: %v", err)
				}
			}
		}
	}
}

// send posts text to one chat, split to the message limit, with kb on the
// last part. Chats are paced one message per sendGap, and a 429 is retried
// once after retry_after. Every part is redacted first.
func (b *Bot) send(ctx context.Context, chat int64, text string, kb *InlineKeyboard) error {
	parts := remote.Chunk(redact.Default().Redact(b.redact.Redact(text)), maxMessage)
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	key := strconv.FormatInt(chat, 10)
	for i, part := range parts {
		if last, ok := b.lastSend[key]; ok {
			if wait := b.sendGap - b.now().Sub(last); wait > 0 && !sleepCtx(ctx, wait) {
				return ctx.Err()
			}
		}
		var markup *InlineKeyboard
		if i == len(parts)-1 {
			markup = kb
		}
		err := b.client.SendMessage(ctx, chat, part, markup)
		var ae *APIError
		if errors.As(err, &ae) && ae.Code == http.StatusTooManyRequests {
			wait := ae.RetryAfter
			if wait <= 0 {
				wait = 5 * time.Second
			}
			if !sleepCtx(ctx, wait) {
				return ctx.Err()
			}
			err = b.client.SendMessage(ctx, chat, part, markup)
		}
		b.lastSend[key] = b.now()
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	if err := b.send(ctx, chatID, text, nil); err != nil && ctx.Err() == nil {
		b.logf("telegram: %v", err)
	}
}

func (b *Bot) answerCallback(ctx context.Context, id, text string) {
	if err := b.client.AnswerCallbackQuery(ctx, id, text); err != nil && ctx.Err() == nil {
		b.logf("telegram: %v", err)
	}
}

// askApproval sends an approval and returns the decision. ok is false when
// no answer arrived.
func (b *Bot) askApproval(ctx context.Context, ap tools.Approval) (tools.Decision, bool) {
	var text strings.Builder
	text.WriteString("Scribe asks permission")
	if ap.Danger {
		text.WriteString(" (destructive action)")
	}
	text.WriteString(".\n\n")
	if ap.Title != "" {
		text.WriteString(ap.Title)
		text.WriteString("\n")
	}
	if ap.Detail != "" {
		text.WriteString(clip(ap.Detail, 1500))
	}
	ans, ok := b.ask(ctx, text.String(), &request{approval: true})
	if !ok {
		return tools.Deny, false
	}
	if ans == "approve" {
		return tools.AllowOnce, true
	}
	return tools.Deny, true
}

// askQuestion sends a question or plan review and returns the answer. ok is
// false when no answer arrived.
func (b *Bot) askQuestion(ctx context.Context, q tools.Question) (string, bool) {
	var text strings.Builder
	if q.Kind == "plan_review" {
		text.WriteString("Plan review: ")
	}
	text.WriteString(q.Question)
	if q.Body != "" {
		text.WriteString("\n\n")
		text.WriteString(clip(q.Body, 2500))
	}
	allowFree := q.AllowFreeText || len(q.Options) == 0
	switch {
	case len(q.Options) > 0 && allowFree:
		text.WriteString("\n\nTap an option, or reply with its number or your own answer.")
	case len(q.Options) > 0:
		text.WriteString("\n\nTap an option.")
	default:
		text.WriteString("\n\nReply with your answer.")
	}
	return b.ask(ctx, text.String(), &request{options: q.Options, allowFree: allowFree})
}

// sleepCtx waits d or until ctx ends. It reports whether the wait finished.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func newID() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// clip shortens s to about n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
