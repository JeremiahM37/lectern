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
	"unicode"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/redact"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const (
	// maxMessage is Telegram's limit on message text, with some headroom.
	maxMessage = 4000
	// maxInput is the most characters one inbound message may carry.
	maxInput = 4096
	// maxCommandWords caps the words in one slash command.
	maxCommandWords = 64
	// maxAttachment caps a downloaded image (checked on the update, the
	// getFile reply and the bytes actually read).
	maxAttachment = 10 << 20
	// staleAfter drops messages older than this, so a replayed or delayed
	// update cannot answer a question that has moved on.
	staleAfter = 60 * time.Second
	// minSendGap is the least time between messages to one chat.
	minSendGap = time.Second
)

// Note kinds decide which notify mode lets a message through. Approvals and
// questions are not notes: mute and notify never hold them back.
const (
	kindProgress = "progress"
	kindDone     = "done"
	kindError    = "error"
)

// Notify modes for a chat (set by /notify).
const (
	notifyOn     = "on"
	notifyOff    = "off"
	notifyErrors = "errors"
)

// Messages the bot sends when a phone request cannot be answered.
const (
	unreachableText = "Telegram could not be reached, so the request was not answered."
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
	// Deliver takes a plain message (and any downloaded images) from an
	// allowed chat that is not an answer to a waiting question. It submits
	// the message to Scribe (or steers the running turn) and returns the
	// short reply for the chat, or "". Nil means messages are not taken.
	Deliver func(text string, images []llm.Part) string
	// Stop aborts the running turn of the bound tab for /stop. It returns the
	// reply for the chat. Nil means /stop is not taken.
	Stop func() string
}

// outcome is how a phone request ended. An answer is the person's choice
// ("approve", "session", "deny" or an option text), with the reason they
// typed, if any. Failed is set when the request ended without an answer for
// a reason the agent should hear (the phone was unreachable, the wait ran
// out, the tab closed). NotAsked means no chat is paired, so nobody was
// asked and the local host decides alone.
type outcome struct {
	answer   string
	reason   string
	failed   string
	notAsked bool
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
	deliver  func(string, []llm.Part) string
	stop     func() string
	logf     func(string, ...any)
	sendGap  time.Duration
	now      func() time.Time

	startOnce sync.Once
	outbox    chan note

	mu      sync.Mutex
	seq     int
	pending map[string]*request
	prefs   map[string]*chatPrefs

	sendMu   sync.Mutex
	lastSend map[string]time.Time
}

// note is one message for the notes outbox.
type note struct {
	text string
	kind string
}

// chatPrefs is what one chat asked for with /mute and /notify.
type chatPrefs struct {
	muted  bool
	notify string // notifyOn (default), notifyOff or notifyErrors
}

// request is one approval or question waiting for a tap or a reply.
type request struct {
	approval  bool
	options   []string
	allowFree bool
	seq       int
	owner     any
	reply     chan outcome
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
		outbox:   make(chan note, 64),
		pending:  map[string]*request{},
		prefs:    map[string]*chatPrefs{},
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
	if text == "" {
		text = strings.TrimSpace(m.Caption)
	}
	chat := strconv.FormatInt(m.Chat.ID, 10)
	if !b.allow.Allowed(chat) {
		b.tryPair(ctx, m.Chat.ID, chat, text)
		return
	}
	if b.replyTo(ctx, m.Chat.ID, text) {
		return
	}
	images, notes := b.attachments(ctx, m)
	if len(notes) > 0 {
		text = strings.TrimSpace(strings.Join(append([]string{text}, notes...), "\n"))
	}
	if text == "" && len(images) == 0 {
		return
	}
	b.forward(ctx, m.Chat.ID, text, images)
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
// Data is "apr:<id>:a|s|d" for approvals and "cb:<id>:<index>" for options.
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
		case "s":
			answer = "session"
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
	b.resolve(parts[1], outcome{answer: answer})
	b.answerCallback(ctx, cq.ID, "Recorded")
}

// replyTo handles a plain message while an approval or question waits. It
// returns true when the message was taken as a reply, even a refused one.
//
// An approval takes "approve <reason>", "deny <reason>" or "session <reason>"
// (the last approves for the rest of this tab's session). A question takes
// a number (1-based), "/ans <option or text>", or, for a question that allows
// it, free text. When no question is waiting, a plain message is a new
// message for Scribe (see forward).
func (b *Bot) replyTo(ctx context.Context, chatID int64, text string) bool {
	isAns := false
	arg := text
	if low := strings.ToLower(text); low == "/ans" || strings.HasPrefix(low, "/ans ") {
		isAns = true
		arg = strings.TrimSpace(text[len("/ans"):])
	}
	b.mu.Lock()
	aid, ar := b.oldestApprovalLocked()
	qid, q := b.oldestQuestionLocked()
	b.mu.Unlock()

	if ar != nil {
		if ans, reason, ok := parseApprovalReply(text); ok {
			b.resolve(aid, outcome{answer: ans, reason: reason})
			return true
		}
	}
	if q == nil {
		if isAns {
			b.reply(ctx, chatID, "Nothing is waiting for an answer.")
			return true
		}
		return false
	}
	n, numErr := strconv.Atoi(arg)
	answer := ""
	switch {
	case numErr == nil && n >= 1 && n <= len(q.options):
		answer = q.options[n-1]
	case isAns && optionNamed(q.options, arg) != "":
		answer = optionNamed(q.options, arg)
	case q.allowFree && arg != "":
		answer = arg
	case isAns || numErr == nil:
		b.reply(ctx, chatID, "Reply with an option number, or /ans followed by one of the options.")
		return true
	default:
		return true
	}
	b.resolve(qid, outcome{answer: answer})
	return true
}

// parseApprovalReply reads "approve|deny|session [reason]" (with an optional
// leading slash). ok is false for any other text.
func parseApprovalReply(text string) (answer, reason string, ok bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", "", false
	}
	switch strings.ToLower(strings.TrimPrefix(fields[0], "/")) {
	case "approve":
		answer = "approve"
	case "deny":
		answer = "deny"
	case "session":
		answer = "session"
	default:
		return "", "", false
	}
	reason = strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	if len(reason) > 500 {
		reason = reason[:500]
	}
	return answer, reason, true
}

// helpText is the reply to /start and /help from a paired chat.
const helpText = "Send a message to give Scribe a task. If it is working, your message steers the turn. " +
	"When Scribe asks a question, reply with the option number, or /ans followed by the answer. " +
	"Approvals come with Approve, Approve for session and Deny buttons; you can also type " +
	"\"approve <reason>\" or \"deny <reason>\" and the reason goes to Scribe.\n\n" +
	"Commands: /stop, /mute, /unmute, /notify on|off|errors, /say <text>, /unpair, /help."

// forward handles a message from a paired chat that is not an answer. Plain
// text goes to Scribe. Slash commands are for the phone: /start, /help,
// /stop, /mute, /unmute, /notify, /say and /unpair. Other slash commands
// are host-only.
func (b *Bot) forward(ctx context.Context, chatID int64, text string, images []llm.Part) {
	chat := strconv.FormatInt(chatID, 10)
	if strings.HasPrefix(text, "/") && len(images) == 0 {
		fields := strings.Fields(text)
		if len(fields) > maxCommandWords {
			b.reply(ctx, chatID, fmt.Sprintf("That command has more than %d words. Shorten it.", maxCommandWords))
			return
		}
		cmd := strings.ToLower(fields[0])
		if i := strings.Index(cmd, "@"); i > 0 {
			cmd = cmd[:i]
		}
		arg := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
		switch cmd {
		case "/start", "/help":
			b.reply(ctx, chatID, helpText)
			return
		case "/stop":
			if b.stop == nil {
				b.reply(ctx, chatID, "Stopping from Telegram is not available right now.")
				return
			}
			b.reply(ctx, chatID, b.stop())
			return
		case "/mute", "/unmute":
			b.setPrefs(chat, func(p *chatPrefs) { p.muted = cmd == "/mute" })
			if cmd == "/mute" {
				b.reply(ctx, chatID, "Muted. Scribe will not post progress or turn notes to this chat. Approvals and questions still come here.")
			} else {
				b.reply(ctx, chatID, "Unmuted. Progress and turn notes come back, as the notify setting allows.")
			}
			return
		case "/notify":
			mode := strings.ToLower(arg)
			switch mode {
			case notifyOn, notifyOff, notifyErrors:
				b.setPrefs(chat, func(p *chatPrefs) { p.notify = mode })
				b.reply(ctx, chatID, "Notify is "+mode+" for this chat.")
			default:
				b.reply(ctx, chatID, "Usage: /notify on, /notify off or /notify errors. Now: "+b.notifyMode(chat)+".")
			}
			return
		case "/say":
			if arg == "" {
				b.reply(ctx, chatID, "Usage: /say <text>. The text goes to Scribe as a message, even if it starts with a slash.")
				return
			}
			text = arg
		case "/unpair":
			b.unpair(ctx, chatID, chat)
			return
		default:
			b.reply(ctx, chatID, "Scribe's slash commands are not available from Telegram. Send plain text, or /help.")
			return
		}
	}
	if len(text) > maxInput {
		b.reply(ctx, chatID, fmt.Sprintf("That message is too long: over %d characters. Send it in shorter parts.", maxInput))
		return
	}
	if b.deliver == nil {
		b.reply(ctx, chatID, "Scribe is not taking messages from Telegram right now.")
		return
	}
	if reply := b.deliver(text, images); reply != "" {
		b.reply(ctx, chatID, reply)
	}
}

// unpair removes this chat from the paired chats. A chat listed in the
// config stays paired, and the reply says so.
func (b *Bot) unpair(ctx context.Context, chatID int64, chat string) {
	ok, err := b.allow.Remove(chat)
	switch {
	case errors.Is(err, remote.ErrStaticChat):
		b.reply(ctx, chatID, err.Error())
	case err != nil:
		b.logf("telegram: unpair not saved: %v", err)
		b.reply(ctx, chatID, "Could not save the change. Check the host log.")
	case !ok:
		b.reply(ctx, chatID, "This chat is not paired.")
	default:
		b.reply(ctx, chatID, "Unpaired. This chat can no longer send Scribe messages. Send /pair with a new code to pair it again.")
	}
}

func (b *Bot) setPrefs(chat string, f func(*chatPrefs)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.prefs[chat]
	if p == nil {
		p = &chatPrefs{}
		b.prefs[chat] = p
	}
	f(p)
}

func (b *Bot) notifyMode(chat string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p := b.prefs[chat]; p != nil && p.notify != "" {
		return p.notify
	}
	return notifyOn
}

// wantsNote reports whether a note of kind may reach chat under its mute and
// notify settings.
func (b *Bot) wantsNote(chat, kind string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.prefs[chat]
	if p == nil {
		return true
	}
	if p.muted {
		return false
	}
	switch p.notify {
	case notifyOff:
		return false
	case notifyErrors:
		return kind == kindError
	}
	return true
}

// attachments downloads the images in an inbound message and describes the
// other documents. Only paired chats reach this, so strangers cost nothing.
func (b *Bot) attachments(ctx context.Context, m *Message) ([]llm.Part, []string) {
	var images []llm.Part
	var notes []string
	if n := len(m.Photo); n > 0 {
		largest := m.Photo[n-1]
		if part, note := b.fetchImage(ctx, largest.FileID, largest.FileSize, "image/jpeg"); note != "" {
			notes = append(notes, note)
		} else {
			images = append(images, part)
		}
	}
	if d := m.Document; d != nil {
		name := sanitizeName(d.FileName)
		mime := d.MimeType
		if mime == "" {
			mime = "application/octet-stream"
		}
		if isImageType(mime) {
			if part, note := b.fetchImage(ctx, d.FileID, d.FileSize, mime); note != "" {
				notes = append(notes, note)
			} else {
				images = append(images, part)
			}
		} else {
			notes = append(notes, fmt.Sprintf("[attachment: %s · %s · %d bytes — binary not loaded]", name, mime, d.FileSize))
		}
	}
	return images, notes
}

// fetchImage downloads one image. On failure it returns a note for the
// prompt instead of a part.
func (b *Bot) fetchImage(ctx context.Context, fileID string, size int64, mime string) (llm.Part, string) {
	const tooBig = "[image not loaded: larger than 10 MiB]"
	if size > maxAttachment {
		return llm.Part{}, tooBig
	}
	path, fsize, err := b.client.GetFile(ctx, fileID)
	if err != nil {
		b.logf("telegram: %v", err)
		return llm.Part{}, "[image not loaded: Telegram did not return the file]"
	}
	if fsize > maxAttachment {
		return llm.Part{}, tooBig
	}
	data, err := b.client.Download(ctx, path, maxAttachment)
	if err != nil {
		b.logf("telegram: %v", err)
		if errors.Is(err, errTooLarge) {
			return llm.Part{}, tooBig
		}
		return llm.Part{}, "[image not loaded: the download failed]"
	}
	return llm.Part{Type: llm.PartImage, MediaType: mime, Data: data}, ""
}

func isImageType(mime string) bool {
	switch strings.ToLower(mime) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	return false
}

// sanitizeName drops control characters and caps the length of a filename
// that comes from the phone.
func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "file"
	}
	if utf8.RuneCountInString(out) > 120 {
		out = string([]rune(out)[:120])
	}
	return out
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

// oldestApprovalLocked returns the earliest approval still waiting. The
// caller holds b.mu.
func (b *Bot) oldestApprovalLocked() (string, *request) {
	return b.oldestLocked(func(r *request) bool { return r.approval })
}

// oldestQuestionLocked returns the earliest question still waiting. The
// caller holds b.mu.
func (b *Bot) oldestQuestionLocked() (string, *request) {
	return b.oldestLocked(func(r *request) bool { return !r.approval })
}

func (b *Bot) oldestLocked(match func(*request) bool) (string, *request) {
	var id string
	var best *request
	for pid, r := range b.pending {
		if !match(r) {
			continue
		}
		if best == nil || r.seq < best.seq {
			id, best = pid, r
		}
	}
	return id, best
}

// resolve delivers an outcome to a waiting request. Only the first outcome
// counts, because the request leaves the pending set.
func (b *Bot) resolve(id string, o outcome) bool {
	b.mu.Lock()
	r := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()
	if r == nil {
		return false
	}
	r.reply <- o
	return true
}

// FailPending ends every waiting request that belongs to owner, or every
// waiting request when owner is nil, without an answer. reason is what the
// agent is told. It returns how many requests ended.
func (b *Bot) FailPending(owner any, reason string) int {
	b.mu.Lock()
	var ended []*request
	for id, r := range b.pending {
		if owner == nil || r.owner == owner {
			ended = append(ended, r)
			delete(b.pending, id)
		}
	}
	b.mu.Unlock()
	for _, r := range ended {
		r.reply <- outcome{failed: reason}
	}
	return len(ended)
}

// ask sends text (with buttons for approvals and options) to every paired
// chat and waits for the first answer. A request with no paired chat comes
// back notAsked. A request the phone could not deliver, or that the wait or
// FailPending ended, comes back failed. A request whose ctx was cancelled
// (the local host answered first) comes back empty.
func (b *Bot) ask(ctx context.Context, owner any, text string, r *request) outcome {
	members := b.allow.Members()
	if len(members) == 0 {
		return outcome{notAsked: true}
	}
	id, err := newID()
	if err != nil {
		b.logf("telegram: %v", err)
		return outcome{failed: unreachableText}
	}
	r.reply = make(chan outcome, 1)
	r.owner = owner
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
				return outcome{}
			}
			b.logf("telegram: %v", err)
			continue
		}
		sent++
	}
	if sent == 0 {
		return outcome{failed: unreachableText}
	}
	select {
	case o := <-r.reply:
		return o
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return outcome{failed: "No answer came on Telegram in time."}
		}
		return outcome{}
	}
}

// keyboardFor builds the buttons: Approve, Approve for session and Deny, or
// one button per option. A question that takes free text with no options
// gets no keyboard.
func keyboardFor(id string, r *request) *InlineKeyboard {
	if r.approval {
		return &InlineKeyboard{Rows: [][]Button{{
			{Text: "Approve", CallbackData: "apr:" + id + ":a"},
			{Text: "Approve for session", CallbackData: "apr:" + id + ":s"},
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
	b.enqueue(note{text: text, kind: kindProgress})
}

// OnEvent is an agent sink. It posts a note when a main-agent turn ends,
// or when it stops with an error. Subagent activity is not sent.
func (b *Bot) OnEvent(e agent.Event) {
	if e.AgentID != "" || e.Kind != agent.EvTurnDone {
		return
	}
	if e.Stats != nil && e.Stats.Error != "" && !e.Stats.Aborted {
		// Errors are never throttled: a quiet turn right before must not hide one.
		b.enqueue(note{text: "Scribe stopped with an error: " + clip(e.Stats.Error, 500), kind: kindError})
		return
	}
	text := "Scribe finished this turn."
	if t := strings.TrimSpace(e.Text); t != "" {
		text += "\n\n" + clip(t, 1500)
	}
	b.enqueueDone(note{text: text, kind: kindDone})
}

func (b *Bot) enqueueDone(n note) {
	if b.done.Allow("done") {
		b.enqueue(n)
	}
}

func (b *Bot) enqueue(n note) {
	select {
	case b.outbox <- n:
	default:
		// A full outbox drops the note rather than stalling the agent.
	}
}

// drain sends queued notes to every paired chat that its mute and notify
// settings allow, until ctx ends.
func (b *Bot) drain(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case n := <-b.outbox:
			for _, chat := range b.allow.Members() {
				if !b.wantsNote(chat, n.kind) {
					continue
				}
				chatID, err := strconv.ParseInt(chat, 10, 64)
				if err != nil {
					continue
				}
				if err := b.send(ctx, chatID, n.text, nil); err != nil && ctx.Err() == nil {
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

// askApproval sends an approval and returns how it ended.
func (b *Bot) askApproval(ctx context.Context, owner any, ap tools.Approval) outcome {
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
	text.WriteString("\n\nTap a button, or type \"approve <reason>\" or \"deny <reason>\".")
	return b.ask(ctx, owner, text.String(), &request{approval: true})
}

// askQuestion sends a question or plan review and returns how it ended.
func (b *Bot) askQuestion(ctx context.Context, owner any, q tools.Question) outcome {
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
	return b.ask(ctx, owner, text.String(), &request{options: q.Options, allowFree: allowFree})
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
