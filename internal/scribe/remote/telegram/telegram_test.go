package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/openai"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const (
	testToken = "123456:TEST-TOKEN-VALUE"
	ownerChat = int64(4242)
)

// fakeTG is an httptest Bot API. Tests push updates into it and read back
// what the bot sent.
type fakeTG struct {
	token   string
	srv     *httptest.Server
	updates chan Update
	sent    chan sentMsg

	mu      sync.Mutex
	answers []string
}

type sentMsg struct {
	ChatID   int64
	Text     string
	Keyboard *InlineKeyboard
}

func newFakeTG(t *testing.T) *fakeTG {
	t.Helper()
	f := &fakeTG{token: testToken, updates: make(chan Update, 64), sent: make(chan sentMsg, 256)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTG) serve(w http.ResponseWriter, r *http.Request) {
	prefix := "/bot" + f.token + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeAPI(w, http.StatusUnauthorized, map[string]any{"ok": false, "description": "Unauthorized"})
		return
	}
	raw, _ := io.ReadAll(r.Body)
	switch strings.TrimPrefix(r.URL.Path, prefix) {
	case "deleteWebhook":
		writeAPI(w, http.StatusOK, map[string]any{"ok": true, "result": true})
	case "getUpdates":
		var batch []Update
		select {
		case u := <-f.updates:
			batch = append(batch, u)
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		for len(batch) > 0 && len(f.updates) > 0 {
			batch = append(batch, <-f.updates)
		}
		if batch == nil {
			batch = []Update{}
		}
		writeAPI(w, http.StatusOK, map[string]any{"ok": true, "result": batch})
	case "sendMessage":
		var p struct {
			ChatID int64           `json:"chat_id"`
			Text   string          `json:"text"`
			Markup *InlineKeyboard `json:"reply_markup"`
		}
		_ = json.Unmarshal(raw, &p)
		select {
		case f.sent <- sentMsg{ChatID: p.ChatID, Text: p.Text, Keyboard: p.Markup}:
		default:
		}
		writeAPI(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	case "answerCallbackQuery":
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &p)
		f.mu.Lock()
		f.answers = append(f.answers, p.Text)
		f.mu.Unlock()
		writeAPI(w, http.StatusOK, map[string]any{"ok": true, "result": true})
	default:
		writeAPI(w, http.StatusNotFound, map[string]any{"ok": false, "description": "Not Found"})
	}
}

func writeAPI(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// push queues an inbound update.
func (f *fakeTG) push(u Update) { f.updates <- u }

// message is a fresh inbound text message from chat.
func message(chat int64, text string) Update {
	return Update{UpdateID: time.Now().UnixNano(), Message: &Message{
		MessageID: 1, Chat: Chat{ID: chat}, From: &User{ID: chat}, Date: time.Now().Unix(), Text: text,
	}}
}

// tap is a button press from chat.
func tap(chat int64, data string) Update {
	return Update{UpdateID: time.Now().UnixNano(), CallbackQuery: &CallbackQuery{
		ID: "cq-" + data, From: User{ID: chat}, Data: data,
		Message: &Message{Chat: Chat{ID: chat}, Date: time.Now().Unix()},
	}}
}

// next waits for the next message the bot sent.
func (f *fakeTG) next(t *testing.T) sentMsg {
	t.Helper()
	select {
	case m := <-f.sent:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a message from the bot")
		return sentMsg{}
	}
}

// quiet fails the test if the bot sends anything within d.
func (f *fakeTG) quiet(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case m := <-f.sent:
		t.Fatalf("bot sent %q to %d, expected silence", m.Text, m.ChatID)
	case <-time.After(d):
	}
}

func (f *fakeTG) answerTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.answers...)
}

// buttonData returns the callback data of the button whose text is label.
func buttonData(t *testing.T, m sentMsg, label string) string {
	t.Helper()
	if m.Keyboard == nil {
		t.Fatalf("message %q has no keyboard", m.Text)
	}
	for _, row := range m.Keyboard.Rows {
		for _, b := range row {
			if b.Text == label {
				return b.CallbackData
			}
		}
	}
	t.Fatalf("no %q button in %+v", label, m.Keyboard)
	return ""
}

// startBot builds a bot against f and runs it until the test ends.
func startBot(t *testing.T, f *fakeTG, allow *remote.Allowlist, pairing *remote.Pairing) *Bot {
	t.Helper()
	bot, err := NewBot(Options{
		Token:   testToken,
		APIBase: f.srv.URL,
		Allow:   allow,
		Pairing: pairing,
		SendGap: time.Nanosecond,
		Logf:    t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bot.Start(ctx)
	return bot
}

func staticAllow(t *testing.T, ids ...string) *remote.Allowlist {
	t.Helper()
	a, err := remote.NewAllowlist(filepath.Join(t.TempDir(), "telegram.json"), ids)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// headlessInner is a local host with no user: it denies approvals and
// answers questions with the first option, as Scribe's headless host does.
type headlessInner struct{}

func (headlessInner) Approve(context.Context, tools.Approval) tools.Decision { return tools.Deny }
func (headlessInner) Ask(_ context.Context, q tools.Question) (string, error) {
	if len(q.Options) > 0 {
		return q.Options[0], nil
	}
	return "", nil
}
func (headlessInner) Progress(string, string) {}
func (headlessInner) Interactive() bool       { return false }

// ---- scripted model ---------------------------------------------------------

// fakeModel serves an OpenAI-compatible chat endpoint. The first request
// calls ask_user, and the second gets a final answer. Every request body is
// kept for the assertions.
type fakeModel struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []string
}

func newFakeModel(t *testing.T, askArgs string) *fakeModel {
	t.Helper()
	m := &fakeModel{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		n := len(m.reqs)
		m.reqs = append(m.reqs, string(raw))
		m.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 0 {
			argsJSON, _ := json.Marshal(askArgs)
			writeSSE(w,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"ask_user","arguments":""}}]},"finish_reason":null}]}`,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":%s}}]},"finish_reason":null}]}`, argsJSON),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
			return
		}
		writeSSE(w,
			`{"choices":[{"index":0,"delta":{"content":"Using SQLite."},"finish_reason":null}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func writeSSE(w io.Writer, chunks ...string) {
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func (m *fakeModel) request(i int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i >= len(m.reqs) {
		return ""
	}
	return m.reqs[i]
}

// askUserTool is ask_user as the agent registers it: it asks the run's host.
type askUserTool struct{}

func (askUserTool) ReadOnly() bool { return true }
func (askUserTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "ask_user",
		Description: "Ask the user a question.",
		Schema: tools.Schema(map[string]any{
			"question": tools.Str("The question"),
			"options":  tools.Arr("Choices", map[string]any{"type": "string"}),
		}, "question"),
	}
}
func (askUserTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var in tools.Question
	if err := tools.Decode(args, &in); err != nil {
		return tools.Errorf("%v", err)
	}
	in.AllowFreeText = true
	ans, err := env.Host.Ask(ctx, in)
	if err != nil {
		return tools.Errorf("question was interrupted: %v", err)
	}
	return tools.Result{Output: "The user answered: " + ans, Summary: ans}
}

// ---- tests ------------------------------------------------------------------

// An ask_user raised by a headless run is answered from Telegram and the
// run resumes with that answer.
func TestAskUserFromHeadlessRunIsAnsweredFromTelegram(t *testing.T) {
	tg := newFakeTG(t)
	bot := startBot(t, tg, staticAllow(t, strconv.FormatInt(ownerChat, 10)), nil)
	model := newFakeModel(t, `{"question":"Which database?","options":["Postgres","SQLite"]}`)

	reg := tools.NewRegistry()
	reg.Register(askUserTool{})
	prov := openai.New(openai.Options{ID: "fake", BaseURL: model.srv.URL + "/v1", APIKey: "test"})
	a := agent.New(agent.Options{
		Model:  agent.Model{Ref: "fake/m", ID: "m", Provider: prov},
		Tools:  reg,
		Env:    tools.Env{Workdir: t.TempDir(), Host: NewHost(bot, headlessInner{}, 10*time.Second)},
		System: func(string) string { return "You are a test agent." },
		Mode:   "default",
	})

	type result struct {
		stats agent.TurnStats
		err   error
	}
	done := make(chan result, 1)
	go func() {
		st, err := a.Run(context.Background(), "Pick a database.", nil)
		done <- result{st, err}
	}()

	// The question reaches the paired chat with one button per option.
	q := tg.next(t)
	if q.ChatID != ownerChat || !strings.Contains(q.Text, "Which database?") {
		t.Fatalf("question went to %d as %q", q.ChatID, q.Text)
	}
	// The phone taps SQLite, the second option.
	tg.push(tap(ownerChat, buttonData(t, q, "SQLite")))

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("run failed: %v", r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not resume after the answer")
	}
	if got := model.request(1); !strings.Contains(got, "The user answered: SQLite") {
		t.Fatalf("second model call did not receive the phone answer; body = %.400s", got)
	}
	if got := tg.answerTexts(); len(got) != 1 || got[0] != "Recorded" {
		t.Fatalf("callback answers = %q, want [Recorded]", got)
	}
}

// Pairing: an unknown chat is ignored, a wrong code is refused, the right
// code pairs the chat once, and a spent code pairs no one else.
func TestPairingRejectsUnknownChats(t *testing.T) {
	tg := newFakeTG(t)
	stateFile := filepath.Join(t.TempDir(), "telegram.json")
	allow, err := remote.NewAllowlist(stateFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	pairing := remote.NewPairing(0)
	bot := startBot(t, tg, allow, pairing)
	code, _, err := bot.MintPairCode()
	if err != nil {
		t.Fatal(err)
	}

	// Plain chatter from an unpaired chat gets no reply. The wrong-code
	// command after it is the barrier: its reply proves the chatter was
	// processed first.
	tg.push(message(9001, "hello there"))
	tg.push(message(9001, "/pair WRONG1"))
	if m := tg.next(t); m.ChatID != 9001 || !strings.Contains(m.Text, "not valid") {
		t.Fatalf("wrong code reply = %q to %d", m.Text, m.ChatID)
	}
	if allow.Allowed("9001") {
		t.Fatal("a wrong code paired the chat")
	}

	// The right code pairs the chat.
	tg.push(message(9001, "/pair "+code))
	if m := tg.next(t); m.ChatID != 9001 || !strings.Contains(m.Text, "Paired") {
		t.Fatalf("pairing reply = %q", m.Text)
	}
	if !allow.Allowed("9001") {
		t.Fatal("paired chat not on the allowlist")
	}

	// The code is spent: another chat cannot use it.
	tg.push(message(9002, "/pair "+code))
	if m := tg.next(t); m.ChatID != 9002 || strings.Contains(m.Text, "Paired") {
		t.Fatalf("spent code paired another chat: %q", m.Text)
	}
	if allow.Allowed("9002") {
		t.Fatal("spent code paired 9002")
	}

	// The pairing is saved: a fresh allowlist from the same file knows it.
	reloaded, err := remote.NewAllowlist(stateFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Allowed("9001") || reloaded.Allowed("9002") {
		t.Fatalf("saved pairings = %v", reloaded.Members())
	}
}

// A tap from a chat outside the allowlist is ignored and cannot resolve an
// approval. A tap from a paired chat can.
func TestTapFromUnpairedChatCannotResolveApproval(t *testing.T) {
	tg := newFakeTG(t)
	bot := startBot(t, tg, staticAllow(t, strconv.FormatInt(ownerChat, 10)), nil)
	host := NewHost(bot, headlessInner{}, time.Minute)

	type decided struct{ d tools.Decision }
	out := make(chan decided, 1)
	go func() {
		out <- decided{host.Approve(context.Background(), tools.Approval{
			Tool: "shell", Kind: "shell", Title: "Run shell command", Detail: "go test ./...",
		})}
	}()
	req := tg.next(t)
	if req.ChatID != ownerChat || !strings.Contains(req.Text, "go test ./...") {
		t.Fatalf("approval went to %d as %q", req.ChatID, req.Text)
	}
	approve := buttonData(t, req, "Approve")

	tg.push(tap(9999, approve))
	select {
	case d := <-out:
		t.Fatalf("a tap from an unpaired chat decided the approval: %v", d.d)
	case <-time.After(300 * time.Millisecond):
	}
	if got := tg.answerTexts(); len(got) != 0 {
		t.Fatalf("unpaired tap got a reply: %q", got)
	}

	tg.push(tap(ownerChat, approve))
	select {
	case d := <-out:
		if d.d != tools.AllowOnce {
			t.Fatalf("Approve tap gave %v, want AllowOnce", d.d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approval tap from the paired chat was not applied")
	}
}

// A Deny tap from the phone decides the approval while the local host waits.
func TestDenyTapDecidesWhileLocalWaits(t *testing.T) {
	tg := newFakeTG(t)
	bot := startBot(t, tg, staticAllow(t, strconv.FormatInt(ownerChat, 10)), nil)
	host := NewHost(bot, waitingLocal{}, time.Minute)

	out := make(chan tools.Decision, 1)
	go func() {
		out <- host.Approve(context.Background(), tools.Approval{Tool: "shell", Kind: "shell", Detail: "rm x"})
	}()
	req := tg.next(t)
	tg.push(tap(ownerChat, buttonData(t, req, "Deny")))
	select {
	case d := <-out:
		if d != tools.Deny {
			t.Fatalf("Approve = %v, want Deny from the phone", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Deny tap was not applied")
	}
}

// waitingLocal is an interactive local host that never answers on its own,
// so only the phone can decide.
type waitingLocal struct{}

func (waitingLocal) Approve(ctx context.Context, _ tools.Approval) tools.Decision {
	<-ctx.Done()
	return tools.Deny
}
func (waitingLocal) Ask(ctx context.Context, _ tools.Question) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}
func (waitingLocal) Progress(string, string) {}

// With no chat paired, the local host decides at once and nothing is sent.
func TestNoPairedChatLeavesDecisionToLocalHost(t *testing.T) {
	tg := newFakeTG(t)
	bot := startBot(t, tg, staticAllow(t), nil)
	host := NewHost(bot, fixedLocal{d: tools.AllowSession}, time.Minute)

	start := time.Now()
	if d := host.Approve(context.Background(), tools.Approval{Tool: "web", Kind: "web"}); d != tools.AllowSession {
		t.Fatalf("Approve = %v, want the local AllowSession", d)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("approval waited for a phone that is not paired")
	}
	tg.quiet(t, 200*time.Millisecond)
}

type fixedLocal struct{ d tools.Decision }

func (f fixedLocal) Approve(context.Context, tools.Approval) tools.Decision { return f.d }
func (f fixedLocal) Ask(context.Context, tools.Question) (string, error)    { return "local", nil }
func (f fixedLocal) Progress(string, string)                                {}

// Forwarded and stale messages are dropped, so they cannot answer a
// question. The same text sent normally does.
func TestSpoofedAndStaleMessagesCannotAnswer(t *testing.T) {
	tg := newFakeTG(t)
	bot := startBot(t, tg, staticAllow(t, strconv.FormatInt(ownerChat, 10)), nil)
	host := NewHost(bot, headlessInner{}, time.Minute)

	out := make(chan string, 1)
	go func() {
		ans, _ := host.Ask(context.Background(), tools.Question{Question: "Pick", Options: []string{"Alpha", "Beta"}})
		out <- ans
	}()
	tg.next(t) // the question

	forwarded := message(ownerChat, "2")
	forwarded.Message.ForwardOrigin = json.RawMessage(`{"type":"user"}`)
	tg.push(forwarded)

	stale := message(ownerChat, "2")
	stale.Message.Date = time.Now().Add(-2 * time.Minute).Unix()
	tg.push(stale)

	tg.push(message(ownerChat, "/ans Beta"))
	select {
	case ans := <-out:
		if ans != "Beta" {
			t.Fatalf("answer = %q, want Beta from the normal message", ans)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the normal answer was not applied")
	}
}

// Progress is throttled per interval, and a finished turn is still posted.
func TestProgressIsThrottledAndTurnDonePosts(t *testing.T) {
	tg := newFakeTG(t)
	bot, err := NewBot(Options{
		Token: testToken, APIBase: tg.srv.URL, Allow: staticAllow(t, strconv.FormatInt(ownerChat, 10)),
		ProgressEvery: time.Hour, SendGap: time.Nanosecond, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bot.Start(ctx)

	bot.Progress("running tests")
	bot.Progress("still running")
	bot.Progress("and again")
	bot.OnEvent(agent.Event{Kind: agent.EvTurnDone, Text: "All green."})

	if m := tg.next(t); m.Text != "running tests" {
		t.Fatalf("first note = %q", m.Text)
	}
	if m := tg.next(t); !strings.Contains(m.Text, "All green.") {
		t.Fatalf("turn-done note = %q", m.Text)
	}
	tg.quiet(t, 200*time.Millisecond)
}

// Errors from the transport never carry the token, since net/http puts the
// request URL into its errors.
func TestTokenNeverAppearsInErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	c := NewClient(base, testToken, nil)
	err := c.DeleteWebhook(context.Background())
	if err == nil {
		t.Fatal("call to a closed server succeeded")
	}
	if strings.Contains(err.Error(), "TEST-TOKEN-VALUE") {
		t.Fatalf("error leaks the token: %v", err)
	}
	var ae *APIError
	if errors.As(err, &ae) {
		t.Fatalf("transport failure reported as an API error: %v", err)
	}
}

// A bad token is an API error, and it is returned as one.
func TestAPIErrorIsTyped(t *testing.T) {
	tg := newFakeTG(t)
	c := NewClient(tg.srv.URL, "999:WRONG", nil)
	err := c.DeleteWebhook(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != http.StatusUnauthorized {
		t.Fatalf("error = %v, want an APIError with 401", err)
	}
}

func TestNewBotRequiresTokenAndAllowlist(t *testing.T) {
	if _, err := NewBot(Options{Allow: staticAllow(t)}); err == nil {
		t.Fatal("bot without a token was created")
	}
	if _, err := NewBot(Options{Token: testToken}); err == nil {
		t.Fatal("bot without an allowlist was created")
	}
}
