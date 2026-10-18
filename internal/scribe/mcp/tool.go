package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/web"
)

// maxOutput caps one tool result so a chatty server cannot flood the context.
const maxOutput = 100_000

// remoteTool exposes one MCP tool to the agent.
type remoteTool struct {
	m        *Manager
	server   string
	remote   string // the tool's name on the server
	name     string // mcp__<server>__<tool>
	desc     string
	schema   json.RawMessage
	readOnly bool
}

var _ tools.Tool = (*remoteTool)(nil)

func (t *remoteTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: t.name, Description: t.desc, Schema: t.schema}
}

// ReadOnly is true only when the server marks the tool readOnlyHint.
func (t *remoteTool) ReadOnly() bool { return t.readOnly }

// Run asks the user before calling a tool that is not marked read-only,
// unless the session is in auto mode or the user already allowed this tool.
func (t *remoteTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	needsAsk := !t.readOnly && (env == nil || env.Mode != "auto")
	if needsAsk && !t.m.sessionAllowed(t.name) {
		if env == nil || env.Host == nil {
			return tools.Errorf("%s needs the user's approval, but no way to ask is available.", t.name)
		}
		rctx, reason := tools.WithReasonSlot(ctx)
		d := env.Host.Approve(rctx, tools.Approval{
			Tool:   t.name,
			Kind:   "mcp",
			Title:  fmt.Sprintf("Use %s tool %s", t.server, t.remote),
			Detail: truncate(string(args), 400),
		})
		if d != tools.Deny {
			tools.NoteApproval(ctx, reason())
		}
		switch d {
		case tools.AllowSession:
			t.m.allowSession(t.name)
		case tools.AllowOnce:
		default:
			return tools.Errorf("%s", web.UserDeclined(fmt.Sprintf("use the %s tool %s", t.server, t.remote), reason()))
		}
	}
	return t.m.call(ctx, t.server, t.remote, args)
}

// formatContent turns a tools/call content array into text the model reads.
func formatContent(items []contentItem) string {
	var parts []string
	for _, it := range items {
		switch it.Type {
		case "text":
			parts = append(parts, it.Text)
		case "image":
			parts = append(parts, fmt.Sprintf("[image: %s, about %d bytes]", orDefault(it.MimeType, "unknown type"), len(it.Data)*3/4))
		case "audio":
			parts = append(parts, fmt.Sprintf("[audio: %s, about %d bytes]", orDefault(it.MimeType, "unknown type"), len(it.Data)*3/4))
		case "resource":
			if it.Resource != nil && it.Resource.Text != "" {
				parts = append(parts, it.Resource.Text)
			} else if it.Resource != nil {
				parts = append(parts, fmt.Sprintf("[resource: %s]", it.Resource.URI))
			}
		case "resource_link":
			parts = append(parts, fmt.Sprintf("[resource link: %s]", it.URI))
		default:
			parts = append(parts, fmt.Sprintf("[unsupported content type %q]", it.Type))
		}
	}
	return truncate(strings.Join(parts, "\n"), maxOutput)
}

// contentItem is one entry of a tools/call result.
type contentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
	URI      string `json:"uri"`
	Resource *struct {
		URI  string `json:"uri"`
		Text string `json:"text"`
	} `json:"resource"`
}

// toolName builds mcp__<server>__<tool>, sanitised to [A-Za-z0-9_-] and
// capped at 64 characters, the limit most model APIs enforce.
func toolName(server, tool string) string {
	n := "mcp__" + sanitize(server) + "__" + sanitize(tool)
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n[output truncated: %d bytes total]", len(s))
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
