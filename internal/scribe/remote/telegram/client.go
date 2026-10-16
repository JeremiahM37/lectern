// Package telegram is Scribe's Telegram surface. It forwards approvals,
// questions and plan reviews to paired chats as messages with inline
// keyboards, reads the taps back with long polling, and posts progress and
// turn-done notes. It uses the Bot API over net/http and has no dependencies.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/redact"
)

const defaultAPI = "https://api.telegram.org"

// APIError is a Bot API reply with ok=false.
type APIError struct {
	Method      string
	Code        int
	Description string
	// RetryAfter is set on 429 replies (the API's retry_after).
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: HTTP %d: %s", e.Method, e.Code, e.Description)
}

// Client is a minimal Bot API client. Every error it returns has the token
// removed, since net/http puts the request URL, and so the token, into its
// errors.
type Client struct {
	token  string
	base   string
	http   *http.Client
	redact *redact.Redactor
}

// NewClient returns a client for apiBase (https://api.telegram.org when
// empty). hc may be nil.
func NewClient(apiBase, token string, hc *http.Client) *Client {
	if apiBase == "" {
		apiBase = defaultAPI
	}
	if hc == nil {
		// No overall timeout: a long poll holds the request for up to 25s,
		// and each call carries its own context.
		hc = &http.Client{}
	}
	return &Client{
		token:  token,
		base:   strings.TrimRight(apiBase, "/") + "/bot" + token,
		http:   hc,
		redact: redact.New(token),
	}
}

// Update is one item from getUpdates. Only the fields Scribe reads are
// declared. Edited messages and channel posts are declared so they can be
// dropped explicitly.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	EditedMessage *Message       `json:"edited_message,omitempty"`
	ChannelPost   *Message       `json:"channel_post,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// Message is the subset of a Telegram message Scribe reads.
type Message struct {
	MessageID     int64           `json:"message_id"`
	From          *User           `json:"from,omitempty"`
	Chat          Chat            `json:"chat"`
	Date          int64           `json:"date"`
	Text          string          `json:"text,omitempty"`
	ForwardOrigin json.RawMessage `json:"forward_origin,omitempty"`
	ViaBot        *User           `json:"via_bot,omitempty"`
	SenderChat    *Chat           `json:"sender_chat,omitempty"`
}

// CallbackQuery is a tap on an inline keyboard button.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// Chat identifies the chat a message came from.
type Chat struct {
	ID int64 `json:"id"`
}

// User identifies a sender.
type User struct {
	ID    int64 `json:"id"`
	IsBot bool  `json:"is_bot,omitempty"`
}

// InlineKeyboard is the reply_markup for buttons under a message.
type InlineKeyboard struct {
	Rows [][]Button `json:"inline_keyboard"`
}

// Button is one inline button. Telegram limits callback_data to 64 bytes.
type Button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// call posts one Bot API method and decodes its result into out (may be nil).
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+method, bytes.NewReader(body))
	if err != nil {
		return c.clean(method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return c.clean(method, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d with an unreadable reply", method, resp.StatusCode)
	}
	if !env.OK {
		return &APIError{
			Method:      method,
			Code:        resp.StatusCode,
			Description: c.redact.Redact(env.Description),
			RetryAfter:  time.Duration(env.Parameters.RetryAfter) * time.Second,
		}
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// clean strips the token from a transport error.
func (c *Client) clean(method string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("telegram %s: %s", method, c.redact.Redact(err.Error()))
}

// DeleteWebhook clears any webhook. A leftover webhook makes every getUpdates
// fail with 409, so polling starts with this.
func (c *Client) DeleteWebhook(ctx context.Context) error {
	return c.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

// GetUpdates long-polls for updates after offset.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	var out []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message", "callback_query"},
	}, &out)
	return out, err
}

// SendMessage posts plain text (no parse mode, so nothing needs escaping),
// with link previews off. kb may be nil.
func (c *Client) SendMessage(ctx context.Context, chat int64, text string, kb *InlineKeyboard) error {
	params := map[string]any{
		"chat_id":                  chat,
		"text":                     text,
		"disable_web_page_preview": true,
	}
	if kb != nil {
		params["reply_markup"] = kb
	}
	return c.call(ctx, "sendMessage", params, nil)
}

// AnswerCallbackQuery stops the button spinner on the phone and can show a
// short note.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}
