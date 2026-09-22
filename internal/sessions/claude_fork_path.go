package sessions

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

//go:embed claude_fork_path.py
var claudeForkPathScript string

func claudeFileFork(args []string) bool {
	resume, fork := false, false
	for i, arg := range args {
		fork = fork || arg == "--fork-session"
		if i > 0 && arg == "{id}" && (args[i-1] == "--resume" || args[i-1] == "-r") {
			resume = true
		}
	}
	return resume && fork
}

func claudeForkPath(ctx context.Context, ex executor.Executor, prefix, workspace, cid string) (string, error) {
	result, err := ex.Run(ctx, prefix+"python3 -c "+shellq.Quote(claudeForkPathScript)+" "+shellq.Quote(workspace)+" "+shellq.Quote(cid), executor.RunOpts{Timeout: 30})
	var out struct{ Path, Error string }
	if err != nil || json.Unmarshal([]byte(result.Stdout), &out) != nil {
		return "", fmt.Errorf("could not locate Claude conversation on target")
	}
	if !result.OK() || out.Path == "" {
		return "", fmt.Errorf("could not locate Claude conversation: %s", out.Error)
	}
	return out.Path, nil
}
