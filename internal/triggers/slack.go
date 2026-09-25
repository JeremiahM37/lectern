package triggers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Slack uses Socket Mode: an outbound websocket the app opens to Slack, which
// then carries events, slash commands and interactivity — no inbound webhook,
// no public URL, works from behind the tailnet exactly like GitHub/Linear
// polling does. See the package doc and docs/triggers.md.

// slackConn is one live Socket Mode connection this process holds open for
// one enabled Slack source. configKey lets syncSlack notice a config/secrets
// edit and restart the connection instead of running on stale credentials
// until the next disconnect.
type slackConn struct {
	cancel    context.CancelFunc
	done      chan struct{}
	configKey string
}

// syncSlack reconciles the live socket set against the enabled Slack sources
// found this tick: starts one for each newly enabled source, stops one whose
// source was disabled or deleted, and restarts one whose config or secrets
// changed under it.
func (m *Manager) syncSlack(ctx context.Context, sources []*store.TriggerSource) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[int64]*store.TriggerSource{}
	for _, s := range sources {
		if Kind(s.Kind) == KindSlack && s.Enabled {
			want[s.ID] = s
		}
	}
	for id, conn := range m.slack {
		src, ok := want[id]
		if !ok || slackConfigKey(src) != conn.configKey {
			conn.cancel()
			<-conn.done
			delete(m.slack, id)
		}
	}
	for id, src := range want {
		if _, ok := m.slack[id]; ok {
			continue
		}
		cctx, cancel := context.WithCancel(context.Background())
		conn := &slackConn{cancel: cancel, done: make(chan struct{}), configKey: slackConfigKey(src)}
		m.slack[id] = conn
		go m.runSlackConn(cctx, conn, src)
	}
}

func slackConfigKey(src *store.TriggerSource) string {
	return src.ConfigJSON + "\x00" + src.SecretsJSON
}

// StopSlack closes every live socket — called from App.Close so a shutdown
// does not leave a websocket goroutine racing the process exit.
func (m *Manager) StopSlack() {
	m.mu.Lock()
	conns := make([]*slackConn, 0, len(m.slack))
	for _, c := range m.slack {
		conns = append(conns, c)
	}
	m.slack = map[int64]*slackConn{}
	m.mu.Unlock()
	for _, c := range conns {
		c.cancel()
		<-c.done
	}
}

// runSlackConn holds one source's connection up with exponential backoff on
// failure — a network blip or a Slack-side restart must not need an operator
// to notice and re-save the source.
func (m *Manager) runSlackConn(ctx context.Context, conn *slackConn, src *store.TriggerSource) {
	defer close(conn.done)
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := m.slackSession(ctx, src)
		if err != nil && ctx.Err() == nil {
			m.Log.Warn("triggers: slack socket disconnected", "source", src.ID, "err", err)
			_ = m.DB.RecordPoll(src.ID, src.CursorJSON, "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// slackSession opens one connection and serves it until it drops or ctx is
// cancelled.
func (m *Manager) slackSession(ctx context.Context, src *store.TriggerSource) error {
	secrets, err := ParseSlackSecrets(src.SecretsJSON)
	if err != nil {
		return err
	}
	if err := secrets.validate(); err != nil {
		return err
	}
	wsURL, err := slackOpenConnection(ctx, m, secrets.AppToken)
	if err != nil {
		return err
	}
	conn, err := wsDial(ctx, wsURL)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := m.DB.RecordPoll(src.ID, src.CursorJSON, "ok", ""); err != nil {
		m.Log.Error("triggers: recording slack connect failed", "source", src.ID, "err", err)
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = conn.SetReadDeadline(time.Now().Add(75 * time.Second))
		op, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if op != wsOpText {
			continue
		}
		m.handleSlackEnvelope(ctx, src, secrets, conn, payload)
	}
}

// slackOpenConnection calls apps.connections.open with the app-level token
// and returns the one-shot websocket URL Slack hands back.
func slackOpenConnection(ctx context.Context, m *Manager, appToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/apps.connections.open", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+appToken)
	resp, err := linearHTTPClient(m).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("slack apps.connections.open: %s", firstNonEmpty(out.Error, "unknown error"))
	}
	return out.URL, nil
}

type slackEnvelope struct {
	Type       string          `json:"type"`
	EnvelopeID string          `json:"envelope_id"`
	Payload    json.RawMessage `json:"payload"`
}

// handleSlackEnvelope acknowledges every envelope immediately (required
// within 3s or Slack redelivers) and, for the two envelope types that mean
// work, builds a candidate and hands it to intake.
func (m *Manager) handleSlackEnvelope(ctx context.Context, src *store.TriggerSource, secrets SlackSecrets, conn *wsConn, raw []byte) {
	var env slackEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		m.Log.Warn("triggers: unreadable slack envelope", "source", src.ID, "err", err)
		return
	}
	if env.EnvelopeID != "" {
		ack, _ := json.Marshal(map[string]string{"envelope_id": env.EnvelopeID})
		if err := conn.WriteText(string(ack)); err != nil {
			m.Log.Warn("triggers: acking slack envelope failed", "source", src.ID, "err", err)
		}
	}
	switch env.Type {
	case "hello", "disconnect":
		return
	case "events_api":
		m.handleSlackEvent(ctx, src, secrets, env)
	case "slash_commands":
		m.handleSlackCommand(ctx, src, secrets, env)
	}
}

type slackEventPayload struct {
	EventID string `json:"event_id"`
	Event   struct {
		Type     string `json:"type"`
		User     string `json:"user"`
		Text     string `json:"text"`
		Channel  string `json:"channel"`
		Ts       string `json:"ts"`
		ThreadTs string `json:"thread_ts"`
		BotID    string `json:"bot_id"`
	} `json:"event"`
}

var slackMentionRe = regexp.MustCompile(`<@[A-Z0-9]+>`)

func (m *Manager) handleSlackEvent(ctx context.Context, src *store.TriggerSource, secrets SlackSecrets, env slackEnvelope) {
	var p slackEventPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		m.Log.Warn("triggers: unreadable slack event payload", "source", src.ID, "err", err)
		return
	}
	// app_mention is already "the bot was mentioned"; a bare "message" event
	// would need the bot's own user id to detect a mention and risks an echo
	// loop on the bot's own posts, so only app_mention is handled.
	if p.Event.Type != "app_mention" || p.Event.BotID != "" {
		return
	}
	project, cfg, err := m.slackTarget(src)
	if err != nil {
		m.Log.Warn("triggers: slack source misconfigured", "source", src.ID, "err", err)
		return
	}
	if cfg.Channel != "" && !strings.EqualFold(cfg.Channel, p.Event.Channel) {
		return
	}
	text := strings.TrimSpace(slackMentionRe.ReplaceAllString(p.Event.Text, ""))
	threadTs := firstNonEmpty(p.Event.ThreadTs, p.Event.Ts)
	extID := env.EnvelopeID
	if p.EventID != "" {
		extID = p.EventID
	}
	ev := m.intake(project, src, cfg.AllowedUsers, cfg.Agent, cfg.Model, "", cfg.MaxPerHour, candidate{
		ExternalID: extID, Kind: "slack_message", Author: p.Event.User,
		Summary: text, Title: "Slack: " + truncate(text, 80), Prompt: text,
		Labels: []string{"slack"},
		Raw:    map[string]any{"channel": p.Event.Channel, "thread_ts": threadTs},
	})
	m.slackAcknowledge(ctx, secrets, ev, p.Event.Channel, threadTs)
}

type slackCommandPayload struct {
	Command   string `json:"command"`
	Text      string `json:"text"`
	UserID    string `json:"user_id"`
	ChannelID string `json:"channel_id"`
}

func (m *Manager) handleSlackCommand(ctx context.Context, src *store.TriggerSource, secrets SlackSecrets, env slackEnvelope) {
	var p slackCommandPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		m.Log.Warn("triggers: unreadable slack command payload", "source", src.ID, "err", err)
		return
	}
	project, cfg, err := m.slackTarget(src)
	if err != nil {
		m.Log.Warn("triggers: slack source misconfigured", "source", src.ID, "err", err)
		return
	}
	if cfg.Channel != "" && !strings.EqualFold(cfg.Channel, p.ChannelID) {
		return
	}
	text := strings.TrimSpace(p.Text)
	ev := m.intake(project, src, cfg.AllowedUsers, cfg.Agent, cfg.Model, "", cfg.MaxPerHour, candidate{
		ExternalID: env.EnvelopeID, Kind: "slack_command", Author: p.UserID,
		Summary: text, Title: "Slack " + p.Command + ": " + truncate(text, 70), Prompt: text,
		Labels: []string{"slack"},
		Raw:    map[string]any{"channel": p.ChannelID, "thread_ts": ""},
	})
	m.slackAcknowledge(ctx, secrets, ev, p.ChannelID, "")
}

// slackAcknowledge tells the channel what happened to the message that just
// arrived: a task was created (and where it will report back), or why not —
// Slack users expect *some* reply, unlike a silent GitHub label swap.
func (m *Manager) slackAcknowledge(ctx context.Context, secrets SlackSecrets, ev *store.TriggerEvent, channel, threadTs string) {
	if ev == nil {
		return // a redelivery already handled; do not double-post
	}
	var text string
	switch ev.Action {
	case "task_created":
		text = fmt.Sprintf("On it — created task #%d. I'll reply here when it's done.", *ev.TaskID)
	default:
		text = "I didn't act on that: " + ev.Reason
	}
	if err := slackPostMessage(ctx, m, secrets.BotToken, channel, threadTs, text); err != nil {
		m.Log.Warn("triggers: slack acknowledge failed", "err", err)
	}
}

func (m *Manager) slackTarget(src *store.TriggerSource) (*store.Project, SlackConfig, error) {
	project, err := m.DB.Project(src.ProjectID)
	if err != nil {
		return nil, SlackConfig{}, err
	}
	cfg, err := ParseSlackConfig(src.ConfigJSON)
	if err != nil {
		return nil, SlackConfig{}, err
	}
	return project, cfg, nil
}

// slackPostMessage posts (optionally threaded) via chat.postMessage — used
// both for the immediate acknowledgement and the final result postback.
func slackPostMessage(ctx context.Context, m *Manager, botToken, channel, threadTs, text string) error {
	body := map[string]any{"channel": channel, "text": text}
	if threadTs != "" {
		body["thread_ts"] = threadTs
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/chat.postMessage", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+botToken)
	resp, err := linearHTTPClient(m).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("slack chat.postMessage: %s", firstNonEmpty(out.Error, "unknown error"))
	}
	return nil
}

// postbackSlack reports a finished trigger-created task's outcome in the
// same channel/thread the event came from.
func (m *Manager) postbackSlack(ctx context.Context, src *store.TriggerSource, ev *store.TriggerEvent, task *store.Task, att *store.Attempt) error {
	secrets, err := ParseSlackSecrets(src.SecretsJSON)
	if err != nil {
		return err
	}
	channel := rawString(ev.RawJSON, "channel")
	threadTs := rawString(ev.RawJSON, "thread_ts")
	return slackPostMessage(ctx, m, secrets.BotToken, channel, threadTs, summarize(task, att))
}
