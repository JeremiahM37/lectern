package drivers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// claudeSteerDriver runs claude in Claude Code's streaming-input mode
// (`--input-format stream-json --output-format stream-json`) instead of
// `-p <prompt>`: the prompt itself becomes the first stdin message (see
// userMessageLine in fifo.go) and the process keeps running, waiting for more
// input, until the fifo pump is closed — see fifo.go's package comment for why
// a plain FIFO redirect is not enough on its own. Output events are unchanged
// from claude-exec (agents.NormalizeClaude does not care which input format
// produced them), so they flow into the same StoreEvents/timeline path.
//
// This is what "steering" is: Send appends another stream-json user message
// to the live fifo at any point, including while claude is mid-turn. Whether
// claude folds that into the current turn or queues it for the next one is
// claude's own behaviour, not something this driver controls — Lectern's job
// is only to keep the input channel open and deliver the message.
type claudeSteerDriver struct{}

func (claudeSteerDriver) Start(ctx context.Context, ex executor.Executor, spec Spec) (Handle, error) {
	rt := agents.RuntimeDir(spec.Worktree)
	if err := ex.WriteFile(ctx, rt+"/pump.py", []byte(pumpScript)); err != nil {
		return nil, err
	}
	if r, err := ex.Run(ctx, ensureFifoCommand(rt), executor.RunOpts{Timeout: 20}); err != nil {
		return nil, err
	} else if !r.OK() {
		return nil, executor.Errf("could not create the steering fifo: %s", strings.TrimSpace(r.Stderr))
	}
	bin := spec.Bin
	if bin == "" {
		bin = "claude"
	}
	settings := spec.SettingsPath
	if settings == "" {
		settings = agents.SettingsRel
	}
	parts := []string{shellq.Quote(bin),
		"--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		// "steerable" is a Lectern-level permission mode, not a claude one: it
		// means "no PreToolUse gate, trust edits" — exactly what acceptEdits
		// means to the CLI — while additionally selecting this driver.
		"--permission-mode", "acceptEdits",
		"--settings", shellq.Quote(settings),
	}
	if spec.MCPConfig != "" {
		parts = append(parts, "--mcp-config", shellq.Quote(spec.MCPConfig))
		if spec.StrictMCP {
			parts = append(parts, "--strict-mcp-config")
		}
	}
	if spec.Model != "" {
		parts = append(parts, "--model", shellq.Quote(spec.Model))
	}
	if spec.ResumeSession != "" {
		parts = append(parts, "--resume", shellq.Quote(spec.ResumeSession))
	}
	envPrefix, err := agents.EnvPrefix(spec.Env, spec.Sandbox)
	if err != nil {
		return nil, err
	}
	agentCmd := envPrefix + strings.Join(parts, " ")
	cmd := streamLaunchCommand(spec.TmuxSession, rt, spec.Worktree, agentCmd)
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 60})
	if err != nil {
		return nil, err
	}
	if !r.OK() {
		return nil, executor.Errf("tmux launch failed: %s", strings.TrimSpace(r.Stderr))
	}

	run := &streamRun{
		ex: ex, rt: rt, tmux: spec.TmuxSession,
		tail:     tailer{ex: ex, path: rt + "/events.jsonl"},
		interval: spec.pollInterval(),
		parse:    func(lines string) []agents.Event { evs, _ := agents.ParseStreamLines("claude", lines); return evs },
		events:   make(chan agents.Event, 64),
		done:     make(chan struct{}),
	}
	// the initial prompt IS the first stdin message; there is no -p argument
	// in streaming-input mode
	if _, err := ex.Run(ctx, appendCommand(rt, userMessageLine(spec.Prompt)), executor.RunOpts{Timeout: 20}); err != nil {
		run.Cancel(ctx)
		return nil, err
	}
	go run.loop(ctx)
	return run, nil
}

// streamRun is the live handle shared by claude-steer and codex-appserver's
// exec fallback shape: a fifo-backed process whose stdin stays open until
// closeCommand is sent. codex-appserver itself does NOT use this type — its
// JSON-RPC framing (request/response correlation, approval routing) is
// different enough to need its own loop — but the launch/append/close
// mechanics it shares live in fifo.go for both.
type streamRun struct {
	ex       executor.Executor
	rt       string
	tmux     string
	tail     tailer
	interval time.Duration
	parse    func(lines string) []agents.Event

	events chan agents.Event
	done   chan struct{}

	mu       sync.Mutex
	closed   bool
	canceled bool
	result   Result
}

func (r *streamRun) Events() <-chan agents.Event { return r.events }

func (r *streamRun) Send(ctx context.Context, text string) error {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return fmt.Errorf("run has already ended; dispatch a follow-up attempt instead")
	}
	res, err := r.ex.Run(ctx, appendCommand(r.rt, userMessageLine(text)), executor.RunOpts{Timeout: 20})
	if err != nil {
		return err
	}
	if !res.OK() {
		return executor.Errf("could not deliver message: %s", strings.TrimSpace(res.Stderr))
	}
	return nil
}

// Cancel asks the run to end gracefully (close the input stream so the agent
// exits on its own, finishing whatever it is doing) rather than killing the
// tmux session outright — a hard kill mid-tool-call can leave the worktree in
// a half-written state.
func (r *streamRun) Cancel(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.canceled = true
	r.mu.Unlock()
	_, err := r.ex.Run(ctx, closeCommand(r.rt), executor.RunOpts{Timeout: 20})
	return err
}

func (r *streamRun) Wait(ctx context.Context) (Result, error) {
	select {
	case <-r.done:
		return r.result, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func (r *streamRun) loop(ctx context.Context) {
	defer close(r.done)
	defer close(r.events)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	ghost := 0
	exitPath := r.rt + "/exit_code"
	var lastResult map[string]any
	for {
		select {
		case <-ctx.Done():
			r.result = Result{ExitCode: -1, Err: ctx.Err().Error()}
			return
		case <-ticker.C:
		}
		lines, err := r.tail.lines(ctx)
		if err != nil {
			continue
		}
		if lines != "" {
			ghost = 0
			for _, ev := range r.parse(lines) {
				if ev.Type == "result" {
					lastResult = ev.Payload
				}
				select {
				case r.events <- ev:
				case <-ctx.Done():
				}
			}
		}
		exitRaw, err := r.ex.ReadFile(ctx, exitPath, 0)
		if err == nil {
			if trimmed := strings.TrimSpace(string(exitRaw)); trimmed != "" {
				if final, err := r.tail.lines(ctx); err == nil && final != "" {
					for _, ev := range r.parse(final) {
						if ev.Type == "result" {
							lastResult = ev.Payload
						}
						select {
						case r.events <- ev:
						case <-ctx.Done():
						}
					}
				}
				rc := -1
				fmt.Sscanf(trimmed, "%d", &rc)
				r.mu.Lock()
				r.closed = true
				r.mu.Unlock()
				r.result = resultFromPayload(rc, lastResult)
				return
			}
		}
		if lines == "" {
			r.mu.Lock()
			canceled := r.canceled
			r.mu.Unlock()
			alive, err := r.ex.Run(ctx, fmt.Sprintf("tmux has-session -t =%s 2>/dev/null", r.tmux),
				executor.RunOpts{Timeout: 20})
			if err == nil && !alive.OK() {
				ghost++
				if ghost >= 2 {
					r.mu.Lock()
					r.closed = true
					r.mu.Unlock()
					if canceled {
						r.result = Result{ExitCode: -1, Err: "cancelled", Raw: lastResult}
					} else {
						r.result = Result{ExitCode: -1, Err: "tmux session disappeared without exit code", Raw: lastResult}
					}
					return
				}
			} else if err == nil {
				ghost = 0
			}
		}
	}
}
