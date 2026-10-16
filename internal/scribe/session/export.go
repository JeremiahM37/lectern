package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// renderMarkdown builds a readable transcript. Tool calls and results are one
// line each so the transcript stays scannable; full tool output is in the JSON
// export.
func renderMarkdown(m Meta, msgs []llm.Message) string {
	var b strings.Builder
	title := m.Title
	if title == "" {
		title = "Untitled session"
	}
	fmt.Fprintf(&b, "# %s\n\n", oneLine(title))
	fmt.Fprintf(&b, "- Session: `%s`\n", m.ID)
	if m.Model != "" {
		fmt.Fprintf(&b, "- Model: `%s`\n", m.Model)
	}
	if !m.Created.IsZero() {
		fmt.Fprintf(&b, "- Started: %s\n", m.Created.Local().Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "- Workdir: `%s`\n\n", m.Workdir)

	for _, msg := range msgs {
		switch msg.Role {
		case llm.RoleUser:
			if text := msg.Text(); text != "" {
				b.WriteString("## User\n\n")
				b.WriteString(text)
				b.WriteString("\n\n")
			}
			for _, p := range msg.Parts {
				if p.Type == llm.PartToolResult {
					writeToolResult(&b, p)
				}
			}
		case llm.RoleAssistant:
			b.WriteString("## Assistant\n\n")
			for _, p := range msg.Parts {
				switch p.Type {
				case llm.PartReasoning:
					if p.Text != "" {
						b.WriteString("<details><summary>Reasoning</summary>\n\n")
						b.WriteString(p.Text)
						b.WriteString("\n\n</details>\n\n")
					}
				case llm.PartText:
					if p.Text != "" {
						b.WriteString(p.Text)
						b.WriteString("\n\n")
					}
				case llm.PartToolCall:
					fmt.Fprintf(&b, "- tool `%s` %s\n", p.ToolName, summarizeArgs(p.Args))
				case llm.PartImage:
					fmt.Fprintf(&b, "- [image %s, %d bytes]\n", p.MediaType, len(p.Data))
				}
			}
			b.WriteString("\n")
		case llm.RoleTool:
			for _, p := range msg.Parts {
				if p.Type == llm.PartToolResult {
					writeToolResult(&b, p)
				}
			}
		}
	}
	return b.String()
}

func writeToolResult(b *strings.Builder, p llm.Part) {
	status := "ok"
	if p.IsError {
		status = "error"
	}
	first := p.Result
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	fmt.Fprintf(b, "  - result %s, %d chars: %s\n", status, len(p.Result), truncate(oneLine(first), 160))
}

// summarizeArgs renders tool arguments as compact JSON, truncated.
func summarizeArgs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "()"
	}
	var buf strings.Builder
	buf.WriteString("`")
	buf.WriteString(truncate(oneLine(string(raw)), 160))
	buf.WriteString("`")
	return buf.String()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "'")), " ")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}
