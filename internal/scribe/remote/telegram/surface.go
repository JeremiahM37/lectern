package telegram

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// TokenEnv and TokenKey name the bot token: the environment variable first,
// then the key store entry (config.Secret).
const (
	TokenEnv = "SCRIBE_TELEGRAM_TOKEN"
	TokenKey = "telegram"
)

// StateFileName holds the chats paired from Telegram, under
// config.RemoteStateDir().
const StateFileName = "telegram-chats.json"

// ErrNoToken means no bot token is configured.
var ErrNoToken = errors.New("no Telegram bot token: set " + TokenEnv + " to the token from @BotFather")

// ErrOff means the Telegram surface is not on.
var ErrOff = errors.New("the Telegram surface is off; run /remote on first")

// Sink is a Scribe tab the phone can drive. The TUI backend satisfies it.
type Sink interface {
	Submit(prompt string, images []llm.Part)
	Steer(msg string) bool
	Running() bool
	Abort()
}

// Surface is the Telegram surface of this process. Every Scribe tab shares
// it: one bot, one set of paired chats. Approvals and questions from any tab
// go to the paired chats. Plain messages from those chats go to the tab
// bound to the surface (see Bind). Turn-end notes come only from that tab.
type Surface struct {
	mu     sync.Mutex
	bot    *Bot
	cancel context.CancelFunc
	sink   Sink
	wait   time.Duration
}

// Default is the surface the Scribe TUI and `lectern code --telegram` use.
var Default = &Surface{}

// EnableOptions configures Enable.
type EnableOptions struct {
	Token   string
	APIBase string
	// StateFile keeps pairings across restarts. Empty keeps them in memory.
	StateFile string
	// Static are chat ids from config. They are always allowed.
	Static []string
	// QuestionWait bounds a question on the phone. Zero uses the default.
	QuestionWait time.Duration
	HTTP         *http.Client
	Logf         func(format string, args ...any)
}

// StatePath is where pairings are kept.
func StatePath() string { return filepath.Join(config.RemoteStateDir(), StateFileName) }

// Enable starts the surface with opts. It does nothing when already on.
func (s *Surface) Enable(opts EnableOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bot != nil {
		return nil
	}
	allow, err := remote.NewAllowlist(opts.StateFile, opts.Static)
	if err != nil {
		return err
	}
	bot, err := NewBot(Options{
		Token: opts.Token, APIBase: opts.APIBase, HTTP: opts.HTTP,
		Allow: allow, Logf: opts.Logf, Deliver: s.deliver, Stop: s.stop,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	bot.Start(ctx)
	s.bot, s.cancel, s.wait = bot, cancel, opts.QuestionWait
	return nil
}

// EnableFromConfig turns the surface on with the "remote" settings of the
// global config and the token from the environment or the key store.
func (s *Surface) EnableFromConfig() error {
	rc, err := config.LoadRemote()
	if err != nil {
		return err
	}
	token := config.Secret(TokenEnv, TokenKey)
	if token == "" {
		return ErrNoToken
	}
	opts := EnableOptions{
		Token:        token,
		StateFile:    StatePath(),
		QuestionWait: time.Duration(rc.QuestionTimeoutSec) * time.Second,
	}
	if rc.Telegram != nil {
		opts.APIBase = rc.Telegram.APIBase
		for _, id := range rc.Telegram.ChatIDs {
			opts.Static = append(opts.Static, strconv.FormatInt(id, 10))
		}
	}
	return s.Enable(opts)
}

// TelegramEnabledInConfig reports whether the config asks for the surface to
// start with Scribe.
func TelegramEnabledInConfig() bool {
	rc, err := config.LoadRemote()
	return err == nil && rc.Telegram != nil && rc.Telegram.Enabled
}

// Disable stops the poll loop. Approvals and questions then go to the local
// host alone, and the bound tab gets no phone messages.
func (s *Surface) Disable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.bot, s.cancel = nil, nil
}

// Enabled reports whether the surface is on.
func (s *Surface) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bot != nil
}

// Bind makes sink the tab that receives plain messages from paired chats and
// whose turn ends are posted to them. Nil unbinds it.
func (s *Surface) Bind(sink Sink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sink = sink
}

// Bound reports whether a tab is bound.
func (s *Surface) Bound() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sink != nil
}

// IsBound reports whether sink is the tab bound to the surface.
func (s *Surface) IsBound(sink Sink) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sink != nil && s.sink == sink
}

// Chats returns the allowed chat ids, or nil when the surface is off.
func (s *Surface) Chats() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bot == nil {
		return nil
	}
	return s.bot.Chats()
}

// Pair mints a one-time code. The person sends it to the bot from the chat
// to pair.
func (s *Surface) Pair() (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bot == nil {
		return "", time.Time{}, ErrOff
	}
	return s.bot.MintPairCode()
}

// Notify is an agent event from the tab source. Turn-end notes go out only
// when source is the bound tab.
func (s *Surface) Notify(source Sink, e agent.Event) {
	s.mu.Lock()
	bot := s.bot
	bound := bot != nil && source != nil && s.sink == source
	s.mu.Unlock()
	if bound {
		bot.OnEvent(e)
	}
}

// Host wraps a tab's host. While the surface is on, each approval, question
// and progress line also goes to the paired chats. While it is off, calls go
// straight to inner. The check is made on every call, so turning the surface
// on or off takes effect at once.
func (s *Surface) Host(inner tools.Host) tools.Host {
	return &surfaceHost{s: s, inner: inner}
}

// deliver is the Bot's Deliver: it hands a plain message to the bound tab.
// A running turn takes it as steering, an idle tab starts a turn with it.
// This mirrors the TUI's own rule for typed input.
func (s *Surface) deliver(text string) string {
	s.mu.Lock()
	sink := s.sink
	s.mu.Unlock()
	if sink == nil {
		return "No Scribe tab is bound to Telegram. In the Scribe tab you want to drive, run /remote here, then send again."
	}
	msg := "[via telegram] " + text
	if sink.Running() && sink.Steer(msg) {
		return "Steering the running turn."
	}
	sink.Submit(msg, nil)
	return "Sent to Scribe."
}

// stop aborts the bound tab's running turn for /stop. Like the TUI's own
// Ctrl+C, it also drops prompts queued behind the turn.
func (s *Surface) stop() string {
	s.mu.Lock()
	sink := s.sink
	s.mu.Unlock()
	switch {
	case sink == nil:
		return "No Scribe tab is bound to Telegram, so there is nothing to stop."
	case !sink.Running():
		return "Nothing is running."
	}
	sink.Abort()
	return "Stopped the running turn. Messages queued behind it were dropped."
}

type surfaceHost struct {
	s     *Surface
	inner tools.Host
}

// current returns the bot and wait time, or a nil bot when the surface is off.
func (h *surfaceHost) current() (*Bot, time.Duration) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	return h.s.bot, h.s.wait
}

func (h *surfaceHost) Approve(ctx context.Context, ap tools.Approval) tools.Decision {
	bot, wait := h.current()
	if bot == nil {
		return h.inner.Approve(ctx, ap)
	}
	return NewHost(bot, h.inner, wait).Approve(ctx, ap)
}

func (h *surfaceHost) Ask(ctx context.Context, q tools.Question) (string, error) {
	bot, wait := h.current()
	if bot == nil {
		return h.inner.Ask(ctx, q)
	}
	return NewHost(bot, h.inner, wait).Ask(ctx, q)
}

func (h *surfaceHost) Progress(callID, text string) {
	bot, wait := h.current()
	if bot == nil {
		h.inner.Progress(callID, text)
		return
	}
	NewHost(bot, h.inner, wait).Progress(callID, text)
}

// Interactive reports the local host's answer, so a headless tab still lets
// the phone decide first.
func (h *surfaceHost) Interactive() bool { return localAnswers(h.inner) }
