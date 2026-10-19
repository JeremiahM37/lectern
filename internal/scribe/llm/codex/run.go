package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// stderrTailBytes is how much of codex's stderr is kept for error text. The
// end of stderr is where the reason is, so older output is dropped.
const stderrTailBytes = 4 << 10

// killWaitDelay bounds how long Wait waits for the output pipes after the
// process is killed. A grandchild that kept a pipe open would otherwise hang
// the call even though codex itself is dead.
const killWaitDelay = 5 * time.Second

// errNotInstalled is returned when the codex binary cannot be found. The text
// is what the user needs to read, so it names the install command.
var errNotInstalled = errors.New("Codex CLI is not installed. Install it with `npm i -g @openai/codex`, then run /codex login.")

// execArgs is the argv of one model call. It matches the flags measured against
// codex-cli 0.163.0. Change one only after checking it again on the real CLI.
// The model flag is left out when no model was asked for, so codex uses its own
// default rather than failing on an empty value.
// isolationArgs make codex a plain model behind Lectern's own tools. Without
// them codex exec is an agent of its own: it reads files and runs commands
// with its built-in shell (past Lectern's approvals), loads the user's MCP
// servers, plugins and AGENTS.md, and its usage then sums several hidden model
// calls (measured: 87k input tokens for a one-line question, 38k with these).
// Features are switched off with -c features.<name>=false rather than
// --disable, because --disable rejects a name the installed codex does not
// know and fails the call, while an unknown -c key is only a warning.
var isolationArgs = func() []string {
	args := []string{
		"--ignore-user-config", "--ignore-rules",
		"-c", `web_search="disabled"`,
		"-c", "project_doc_max_bytes=0",
	}
	for _, f := range disabledFeatures {
		args = append(args, "-c", "features."+f+"=false")
	}
	return args
}()

// disabledFeatures are the codex features that act on the machine or reach
// outside the request (codex-cli 0.163.0 names).
var disabledFeatures = []string{
	"shell_tool", "unified_exec", "apps", "plugins", "remote_plugin",
	"browser_use", "browser_use_external", "in_app_browser", "computer_use",
	"tool_suggest", "skill_search", "sleep_tool", "goals", "code_mode_host",
}

func execArgs(model, cwd, schemaPath string) []string {
	args := []string{
		"exec", "--json", "--ephemeral", "--skip-git-repo-check",
		"--sandbox", "read-only",
		"-c", `approval_policy="never"`,
	}
	args = append(args, isolationArgs...)
	args = append(args,
		"--cd", cwd,
		"--output-schema", schemaPath,
	)
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, "-")
}

// writeOutputSchema writes outputSchema to a fresh temp dir. The caller runs
// cleanup when the codex run is over, so the file does not outlive the call.
func writeOutputSchema() (string, func(), error) {
	dir, err := os.MkdirTemp("", "lectern-codex-schema-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "output-schema.json")
	if err := os.WriteFile(path, []byte(outputSchema), 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// execRun is what one codex process left behind once it ended.
type execRun struct {
	stdout  []byte
	stderr  string
	waitErr error // nil on exit 0; *exec.ExitError or a kill error otherwise
}

// runExec starts codex with the prompt on stdin and waits for it to end. The
// returned error is only a start failure (no binary, no permission). An exit
// that is not 0 is reported in waitErr so the caller can read the output first.
func runExec(ctx context.Context, binary string, args []string, stdin string) (execRun, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout bytes.Buffer
	stderr := &execStderrTail{max: stderrTailBytes}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = killWaitDelay
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return execRun{}, err
	}
	waitErr := cmd.Wait()
	return execRun{stdout: stdout.Bytes(), stderr: stderr.String(), waitErr: waitErr}, nil
}

// execStderrTail keeps the last max bytes written to it.
type execStderrTail struct {
	max int
	buf []byte
}

func (t *execStderrTail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *execStderrTail) String() string { return strings.TrimSpace(string(t.buf)) }

// execLine is one JSONL event from `codex exec --json`. Only the fields read
// here are declared. The other fields on each line are ignored, and so are
// event types this package does not know.
type execLine struct {
	Type    string        `json:"type"`
	Message string        `json:"message"`
	Item    *execItem     `json:"item"`
	Usage   *execUsage    `json:"usage"`
	Error   *execErrorMsg `json:"error"`
}

type execItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type execErrorMsg struct {
	Message string `json:"message"`
}

// execUsage is the usage block of turn.completed. input_tokens includes the
// cached input, which is reported separately, as the OpenAI adapter expects.
type execUsage struct {
	Input     int `json:"input_tokens"`
	Cached    int `json:"cached_input_tokens"`
	Output    int `json:"output_tokens"`
	Reasoning int `json:"reasoning_output_tokens"`
}

// toLLM converts codex usage to llm.Usage. Cached tokens are taken out of the
// input count, so each token is billed once, as cache read.
func (u *execUsage) toLLM() llm.Usage {
	if u == nil {
		return llm.Usage{}
	}
	in := u.Input - u.Cached
	if in < 0 {
		in = 0
	}
	return llm.Usage{
		InputTokens:     in,
		OutputTokens:    u.Output,
		CacheReadTokens: u.Cached,
		ReasoningTokens: u.Reasoning,
	}
}

// execResult is the part of a codex run the events are built from.
type execResult struct {
	reasoning  []string
	finalText  string // text of the last agent_message
	hasFinal   bool
	usage      *execUsage
	lastError  string // the last "error" event; codex may retry past these
	turnFailed string // set by turn.failed
}

// parseExecOutput reads the JSONL from codex. Lines that are not JSON, or not
// an event this package knows, are skipped, because codex can print warnings
// next to the stream.
func parseExecOutput(out []byte) execResult {
	var res execResult
	for _, raw := range bytes.Split(out, []byte("\n")) {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		var line execLine
		if json.Unmarshal(raw, &line) != nil {
			continue
		}
		switch line.Type {
		case "item.completed":
			if line.Item == nil {
				continue
			}
			switch line.Item.Type {
			case "agent_message":
				// codex sends intermediate messages too. Only the last one is the
				// answer, because that is the one --output-schema constrains.
				res.finalText = line.Item.Text
				res.hasFinal = true
			case "reasoning":
				if strings.TrimSpace(line.Item.Text) != "" {
					res.reasoning = append(res.reasoning, line.Item.Text)
				}
			}
		case "turn.completed":
			res.usage = line.Usage
		case "turn.failed":
			res.turnFailed = "codex turn failed"
			if line.Error != nil && line.Error.Message != "" {
				res.turnFailed = line.Error.Message
			}
		case "error":
			if line.Message != "" {
				res.lastError = line.Message
			}
		}
	}
	return res
}

// replyJSON is the schema-shaped answer codex writes as its final message.
type replyJSON struct {
	FinishReason string          `json:"finishReason"`
	Text         string          `json:"text"`
	ToolCalls    []replyToolCall `json:"toolCalls"`
}

type replyToolCall struct {
	ToolName string `json:"toolName"`
	Input    string `json:"input"`
}

// parseReply decodes the final message as the schema object. A message that is
// not that object is plain text, and the second return is false for it.
func parseReply(text string) (replyJSON, bool) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "{") {
		return replyJSON{}, false
	}
	var r replyJSON
	if json.Unmarshal([]byte(t), &r) != nil {
		return replyJSON{}, false
	}
	if r.FinishReason != "stop" && r.FinishReason != "tool-calls" {
		return replyJSON{}, false
	}
	return r, true
}

// failure turns the text codex reported into the error the agent shows. A
// usage limit is not retryable, since retrying will not lift it. Sign-in
// problems are not retryable either. Transient network or stream text is.
func failure(msg string) error {
	msg = innerMessage(msg)
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "usage limit"), strings.Contains(low, "rate limit"), strings.Contains(low, "429"):
		return &llm.APIError{
			Provider:   ProviderID,
			StatusCode: http.StatusPaymentRequired,
			Type:       "usage_limit",
			Message:    "Codex usage limit reached: " + msg,
		}
	case strings.Contains(low, "not logged in"), strings.Contains(low, "not signed in"),
		strings.Contains(low, "login"), strings.Contains(low, "401"), strings.Contains(low, "unauthorized"):
		return &llm.APIError{
			Provider:   ProviderID,
			StatusCode: http.StatusUnauthorized,
			Message:    "Codex is not signed in. Run /codex login.",
		}
	}
	if _, retry := llm.Classify(errors.New(msg)); retry {
		return &llm.APIError{Provider: ProviderID, Type: llm.StreamErrorType, Message: msg}
	}
	return errors.New(msg)
}

// innerMessage returns the message inside an error codex passed through as
// JSON, e.g. {"type":"error","status":400,"error":{"message":"..."}}, and msg
// unchanged otherwise.
func innerMessage(msg string) string {
	t := strings.TrimSpace(msg)
	if !strings.HasPrefix(t, "{") {
		return msg
	}
	var v struct {
		Message string `json:"message"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(t), &v) != nil {
		return msg
	}
	if v.Error != nil && v.Error.Message != "" {
		return v.Error.Message
	}
	if v.Message != "" {
		return v.Message
	}
	return msg
}
