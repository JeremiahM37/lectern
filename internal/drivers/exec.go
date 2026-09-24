package drivers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// execDriver is claude-exec / codex-exec / gemini-exec: exactly what the
// scheduler's own launch()/poll() already do for a built-in agent — tmux, a
// prompt passed once, output redirected to events.jsonl, an exit_code file —
// built from the SAME agents.Launcher.Command and parsed with the SAME
// agents.ParseStreamLines the scheduler uses, so this is genuinely the
// existing path wrapped behind the Driver interface, not a reimplementation
// that could drift from it. Not steerable: Send always errors.
type execDriver struct {
	Agent string
}

func (d execDriver) Start(ctx context.Context, ex executor.Executor, spec Spec) (Handle, error) {
	rt := agents.RuntimeDir(spec.Worktree)
	if err := ex.WriteFile(ctx, rt+"/prompt.md", []byte(spec.Prompt)); err != nil {
		return nil, err
	}
	launcher := agents.Launcher{}
	switch d.Agent {
	case "claude":
		launcher.ClaudeBin = spec.Bin
	case "codex":
		launcher.CodexBin = spec.Bin
	case "gemini":
		launcher.GeminiBin = spec.Bin
	}
	cmd, err := launcher.Command(agents.LaunchSpec{
		Agent: d.Agent, Worktree: spec.Worktree, TmuxSession: spec.TmuxSession,
		PermissionMode: spec.PermissionMode, Model: spec.Model, ResumeSession: spec.ResumeSession,
		Sandbox: spec.Sandbox, Env: spec.Env, SettingsPath: spec.SettingsPath,
		MCPConfig: spec.MCPConfig, StrictMCP: spec.StrictMCP,
	})
	if err != nil {
		return nil, err
	}
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 60})
	if err != nil {
		return nil, err
	}
	if !r.OK() {
		return nil, executor.Errf("tmux launch failed: %s", strings.TrimSpace(r.Stderr))
	}
	run := &execRun{
		ex: ex, agent: d.Agent, tmux: spec.TmuxSession,
		tail:     tailer{ex: ex, path: rt + "/events.jsonl"},
		exitPath: rt + "/exit_code",
		interval: spec.pollInterval(),
		events:   make(chan agents.Event, 64),
		done:     make(chan struct{}),
	}
	go run.loop(ctx)
	return run, nil
}

// execRun is the live handle behind execDriver. It mirrors
// scheduler.poll's own state machine (drain -> check exit_code -> ghost
// session detection) but independently, since this run is not on the
// scheduler's Tick.
type execRun struct {
	ex       executor.Executor
	agent    string
	tmux     string
	tail     tailer
	exitPath string
	interval time.Duration

	events chan agents.Event
	done   chan struct{}

	mu       sync.Mutex
	canceled bool
	result   Result
}

func (r *execRun) Events() <-chan agents.Event { return r.events }

func (r *execRun) Send(context.Context, string) error {
	return fmt.Errorf("driver %q does not accept a mid-run message: its process already "+
		"received its one prompt as an argument and exits after its single turn", r.agent)
}

func (r *execRun) Cancel(ctx context.Context) error {
	r.mu.Lock()
	r.canceled = true
	r.mu.Unlock()
	_, err := r.ex.Run(ctx, fmt.Sprintf("tmux kill-session -t =%s 2>/dev/null || true", r.tmux),
		executor.RunOpts{Timeout: 20})
	return err
}

func (r *execRun) Wait(ctx context.Context) (Result, error) {
	select {
	case <-r.done:
		return r.result, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func (r *execRun) loop(ctx context.Context) {
	defer close(r.done)
	defer close(r.events)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	ghost := 0
	for {
		select {
		case <-ctx.Done():
			r.result = Result{ExitCode: -1, Err: ctx.Err().Error()}
			return
		case <-ticker.C:
		}

		lines, err := r.tail.lines(ctx)
		if err != nil {
			continue // transient target error; the next tick retries
		}
		var lastResult map[string]any
		if lines != "" {
			ghost = 0
			events, _ := agents.ParseStreamLines(r.agent, lines)
			for _, ev := range events {
				if ev.Type == "result" {
					lastResult = ev.Payload
				}
				select {
				case r.events <- ev:
				case <-ctx.Done():
				}
			}
		}

		exitRaw, err := r.ex.ReadFile(ctx, r.exitPath, 0)
		if err == nil {
			if trimmed := strings.TrimSpace(string(exitRaw)); trimmed != "" {
				// one more drain, same as scheduler.poll: whatever the agent wrote
				// between the read above and the exit_code appearing (usually its
				// closing result) would otherwise be lost.
				if final, err := r.tail.lines(ctx); err == nil && final != "" {
					events, _ := agents.ParseStreamLines(r.agent, final)
					for _, ev := range events {
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
				r.result = resultFromPayload(rc, lastResult)
				return
			}
		}

		if lines == "" {
			r.mu.Lock()
			canceled := r.canceled
			r.mu.Unlock()
			if canceled {
				r.result = Result{ExitCode: -1, Err: "cancelled"}
				return
			}
			alive, err := r.ex.Run(ctx, fmt.Sprintf("tmux has-session -t =%s 2>/dev/null", r.tmux),
				executor.RunOpts{Timeout: 20})
			if err == nil && !alive.OK() {
				ghost++
				if ghost >= 2 {
					r.result = Result{ExitCode: -1, Err: "tmux session disappeared without exit code"}
					return
				}
			} else if err == nil {
				ghost = 0
			}
		}
	}
}

// resultFromPayload builds a Result from the last normalised "result" event
// payload seen (NormalizeClaude / normalizeCodex already shape cost/tokens
// consistently; see agents.NormalizeClaude and the codex "turn.completed"
// case), so callers get the same usage numbers whichever agent ran.
func resultFromPayload(rc int, payload map[string]any) Result {
	res := Result{ExitCode: rc, Raw: payload}
	if payload == nil {
		return res
	}
	if sid, ok := payload["session_id"].(string); ok {
		res.SessionID = sid
	}
	if cost, ok := payload["cost_usd"].(float64); ok {
		res.CostUSD = &cost
	}
	if tokens, ok := payload["tokens"].(float64); ok {
		n := int(tokens)
		res.OutputTokens = &n
	}
	if usage, ok := payload["usage"].(map[string]any); ok {
		if v, ok := numField(usage, "input_tokens"); ok {
			res.InputTokens = &v
		}
		if v, ok := numField(usage, "output_tokens"); ok {
			res.OutputTokens = &v
		}
	}
	return res
}

func numField(m map[string]any, key string) (int, bool) {
	v, ok := m[key].(float64)
	if !ok {
		return 0, false
	}
	return int(v), true
}
