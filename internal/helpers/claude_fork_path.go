package helpers

// Port of internal/sessions/claude_fork_path.py: resolve an exact Claude
// transcript on its target for a native file-path fork.

import (
	"fmt"
	"io"
	"math/big"
	"os"
	"sort"
	"strings"
)

func init() {
	Register("claude-fork-path", claudeForkPathMain)
}

// claudeForkPathMain is `claude-fork-path WORKSPACE CID`.
func claudeForkPathMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 0:
		return printError(stdout, stderr, pyError("TypeError", "resolve() missing 2 required positional arguments: 'workspace' and 'conversation_id'"))
	case len(args) == 1:
		return printError(stdout, stderr, pyError("TypeError", "resolve() missing 1 required positional argument: 'conversation_id'"))
	case len(args) > 2:
		return printError(stdout, stderr, pyError("TypeError", fmt.Sprintf("resolve() takes 3 positional arguments but %d were given", len(args)+1)))
	}
	path, err := claudeForkPath(nEnvOr("CLAUDE_CONFIG_DIR", "~/.claude"), args[0], args[1])
	if err != nil {
		return printError(stdout, stderr, err)
	}
	fmt.Fprintln(stdout, jsonDumps(dict("path", path), true))
	return 0
}

// canonicalUUID is str(uuid.UUID(text)), with uuid's error text.
func canonicalUUID(text string) (string, error) {
	h := strings.ReplaceAll(strings.ReplaceAll(text, "urn:", ""), "uuid:", "")
	h = strings.ReplaceAll(strings.Trim(h, "{}"), "-", "")
	if runeLen(h) != 32 {
		return "", pyError("ValueError", "badly formed hexadecimal UUID string")
	}
	s := nPyStrip(h)
	neg := false
	if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		neg, s = s[0] == '-', s[1:]
	}
	if strings.HasPrefix(strings.ToLower(s), "0x") {
		s = strings.TrimPrefix(s[2:], "_")
	}
	valid := s != "" && !strings.HasPrefix(s, "_") && !strings.HasSuffix(s, "_") && !strings.Contains(s, "__")
	n, ok := new(big.Int).SetString(strings.ReplaceAll(s, "_", ""), 16)
	if !valid || !ok {
		return "", pyError("ValueError", "invalid literal for int() with base 16: "+strRepr(h))
	}
	if neg && n.Sign() != 0 {
		return "", pyError("ValueError", "int is out of range (need a 128-bit value)")
	}
	if n.BitLen() > 128 {
		return "", pyError("ValueError", "int is out of range (need a 128-bit value)")
	}
	x := fmt.Sprintf("%032x", n)
	return x[:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:], nil
}

func claudeForkPath(profile, workspace, cid string) (string, error) {
	canonical, err := canonicalUUID(cid)
	if err != nil {
		return "", err
	}
	if canonical != cid {
		return "", pyError("ValueError", "Choose an exact conversation ID")
	}
	base := realpathLoose(pathStr(expanduser(pathStr(profile)), "projects"))
	workspace = realpathLoose(workspace)
	matches := map[string]bool{}
	// Scan project directories rather than deriving their names: Claude
	// supports hashed long paths and explicitly named project storage
	// directories.
	count := 0
	for _, dir := range pathlibWildcard(base, nil, true) {
		candidate := dir.path + "/" + cid + ".jsonl"
		if !lexists(candidate) {
			continue
		}
		if count >= 10000 {
			return "", pyError("ValueError", "Native history exceeds the discovery limit")
		}
		count++
		path := realpathLoose(candidate)
		if !isUnder(base, path) || !isFile(path) {
			continue
		}
		ok, err := claudeTranscriptMatches(path, cid, workspace)
		if err != nil {
			return "", err
		}
		if ok {
			matches[path] = true
		}
	}
	if len(matches) != 1 {
		return "", pyError("ValueError", "Exact conversation is missing or ambiguous in the selected native profile")
	}
	var out []string
	for p := range matches {
		out = append(out, p)
	}
	sort.Strings(out)
	return out[0], nil
}

// claudeTranscriptMatches opens path without following a final symlink and
// checks that its header names cid in workspace.
func claudeTranscriptMatches(path, cid, workspace string) (bool, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return false, osError(err, path)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return false, osError(err, "")
	}
	if !opened.Mode().IsRegular() {
		return false, nil
	}
	file := &pyFile{f: f, name: path}
	var id, cwd any = "", ""
	for n := 0; n < 80; n++ {
		if file.pos >= 2*1024*1024 {
			break
		}
		line, err := file.readline(2*1024*1024 + 1)
		if err != nil {
			return false, err
		}
		if len(line) == 0 || len(line) > 2*1024*1024 {
			break
		}
		v, err := jsonLoadsBytes(line)
		if err != nil {
			continue
		}
		row, ok := v.(*nPyDict)
		if !ok {
			continue
		}
		if !truthy(id) {
			id = row.getOr("sessionId", "")
		}
		if !truthy(cwd) {
			cwd = row.getOr("cwd", "")
		}
		if truthy(id) && truthy(cwd) {
			break
		}
	}
	current, err := os.Stat(path)
	if err != nil {
		return false, osError(err, path)
	}
	if !os.SameFile(current, opened) {
		return false, pyError("ValueError", "Conversation changed; retry the fork")
	}
	dir, ok := cwd.(string)
	return id == cid && ok && realpathLoose(dir) == workspace, nil
}
