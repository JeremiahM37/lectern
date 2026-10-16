// Command smoke runs one headless turn against a real model (dev only).
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/app"
)

func main() {
	ctx := context.Background()
	a, err := app.Open(ctx, app.Options{Workdir: os.Args[1], Model: os.Args[2], Ephemeral: true, Mode: "auto"})
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer a.Close()
	a.Subscribe(func(e agent.Event) {
		switch e.Kind {
		case agent.EvText:
			fmt.Print(e.Text)
		case agent.EvReasoning:
			fmt.Print("·")
		case agent.EvToolRun:
			fmt.Printf("\n▸ %s %s\n", e.Tool, e.Args)
		case agent.EvToolResult:
			fmt.Printf("  ← %s err=%v %q\n", e.Result.Summary, e.Result.IsError, firstLine(e.Result.Output))
		case agent.EvRetry, agent.EvError, agent.EvWarning:
			fmt.Printf("\n[%s] %s\n", e.Kind, e.Text)
		}
	})
	st, err := a.Run(ctx, strings.Join(os.Args[3:], " "), nil)
	fmt.Printf("\n--- steps=%d tools=%v files=%v tokens=%+v err=%v dur=%s\n", st.Steps, st.ToolCalls, st.FilesEdited, st.Tokens, err, st.Duration)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
