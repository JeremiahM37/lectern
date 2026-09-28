package helpers

// Ports of internal/nativeidentity/conversations.py (the saved-conversation
// picker and backward history pages) and conversation_live.py (one
// conversation's structured turns, forward from a byte cursor). Paths never
// come from the caller: a conversation id is validated and the file must
// stay under the agent's own history directory.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

func init() {
	Register("native-conversations", nativeConversationsMain)
	Register("native-conversation-live", conversationLiveMain)
}

var slugUnsafe = regexp.MustCompile(`[^a-zA-Z0-9]`)

// nativeStore is where an agent keeps its transcripts: the profile home, the
// history directory and the glob that lists it.
type nativeStore struct {
	home, base, pattern string
	recursive           bool
}

func storeFor(agent, workspace string) nativeStore {
	if agent == "codex" {
		home := expanduser(envOr("CODEX_HOME", "~/.codex"))
		base := pyJoin(home, "sessions")
		return nativeStore{home, base, pyJoin(base, "**", "*.jsonl"), true}
	}
	home := expanduser(envOr("CLAUDE_CONFIG_DIR", "~/.claude"))
	base := pyJoin(home, "projects", slugUnsafe.ReplaceAllString(workspace, "-"))
	return nativeStore{home, base, pyJoin(base, "*.jsonl"), false}
}

// envOr is os.environ.get(key, fallback): a variable set to "" stays "".
func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

// printError is the scripts' error reply: {"error": ...} and exit 1, or a
// crash for an exception they did not catch.
func printError(stdout, stderr io.Writer, err error) int {
	if errors.Is(err, errCrash) {
		return crashed(stderr, err)
	}
	var pe *pyErr
	if !errors.As(err, &pe) {
		err = osError(err, "")
	}
	fmt.Fprintln(stdout, jsonDumps(dict("error", err.Error()), true))
	return 1
}

// nativeConversationsMain is `native-conversations AGENT WORKSPACE SELECTED
// BEFORE [NAME IDENTITY]`.
func nativeConversationsMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 4 {
		return crashed(stderr, fmt.Errorf("%w: not enough values to unpack", errCrash))
	}
	out, err := nativeConversations(args)
	if err != nil {
		return printError(stdout, stderr, err)
	}
	fmt.Fprintln(stdout, jsonDumps(out, true))
	return 0
}

func nativeConversations(args []string) (*pyDict, error) {
	agent, workspace, selected, before := args[0], realpathLoose(args[1]), args[2], args[3]
	if agent != "codex" && agent != "claude" {
		return nil, pyError("ValueError", "Native history is available for Claude and Codex; use terminal history for this agent")
	}
	if selected != "" && !nativeUUID.MatchString(selected) {
		return nil, pyError("ValueError", "Choose a saved conversation ID")
	}
	store := storeFor(agent, workspace)
	base := realpathLoose(store.base)
	var paths []string
	for _, file := range pyGlob(store.pattern, store.recursive) {
		if len(paths) >= 10000 {
			return nil, pyError("ValueError", "History store exceeds the discovery limit")
		}
		real := realpathLoose(file)
		if !commonpathIs(base, real) || !isFile(real) {
			continue
		}
		if selected != "" && !strings.Contains(basename(file), selected) {
			continue
		}
		paths = append(paths, real)
	}
	var statErr error
	sortByDesc(paths, func(p string) float64 {
		m, err := getmtime(p)
		if err != nil && statErr == nil {
			statErr = err
		}
		return m
	})
	if statErr != nil {
		return nil, statErr
	}
	scanLimited := len(paths) > 500
	titles := map[string]string{}
	if agent == "codex" {
		readCodexTitles(pyJoin(store.home, "session_index.jsonl"), titles)
	}
	current := dict("state", "unavailable")
	if selected == "" && len(args) >= 6 {
		identity, err := nativeIdentity(agent, workspace, store.home, args[4], args[5], false)
		if err != nil {
			return nil, err
		}
		current = identity
	}
	if id, ok := current.getOr("id", nil).(string); ok && id != "" {
		var with, without []string
		for _, p := range paths {
			if strings.Contains(basename(p), id) {
				with = append(with, p)
			} else {
				without = append(without, p)
			}
		}
		paths = append(with, without...)
	}
	conversations := []any{}
	seen := map[string]bool{}
	var chosen *pyDict
	chosenFile := ""
	if len(paths) > 500 {
		paths = paths[:500]
	}
	for _, file := range paths {
		info, err := nativeMetadata(file, agent, &workspace, nativeLimit)
		if err != nil || info == nil {
			continue
		}
		info.del("cwd")
		id := info.vals["id"].(string)
		if title, ok := titles[id]; ok {
			info.set("title", title)
		}
		if selected != "" && id == selected {
			chosen, chosenFile = info, file
			break
		}
		if selected == "" && !seen[id] {
			seen[id] = true
			conversations = append(conversations, info)
		}
	}
	if selected == "" {
		id, hasID := current.getOr("id", nil).(string)
		current.set("saved", hasID && seen[id])
		return dict("conversations", conversations, "scan_limited", scanLimited, "current", current), nil
	}
	if chosen == nil {
		return nil, pyError("ValueError", "Conversation not found in this workspace on this target")
	}
	fi, err := os.Stat(chosenFile)
	if err != nil {
		return nil, osError(err, chosenFile)
	}
	size := fi.Size()
	end := size
	if before != "" {
		n, err := pyIntParse(before)
		if err != nil {
			return nil, err
		}
		end = min(size, n)
	}
	if end < 0 {
		return nil, pyError("ValueError", "Invalid history cursor")
	}
	start := max(0, end-nativeLimit)
	var items []*pyDict
	f, err := openPy(chosenFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	f.pos = start
	if start != 0 {
		if _, err := f.readline(nativeLimit); err != nil {
			return nil, err
		}
		start = f.pos
	}
	for f.pos < end {
		offset := f.pos
		line, err := f.readline(min(nativeLimit+1, end-offset))
		if err != nil {
			return nil, err
		}
		if len(line) == 0 {
			break
		}
		if line[len(line)-1] != '\n' {
			continue // trailing in-progress write
		}
		v, err := jsonLoadsBytes(line)
		if err != nil {
			continue
		}
		row, ok := v.(*pyDict)
		if !ok {
			continue
		}
		if item := nativeRecord(row, agent, 64000); item != nil {
			item.set("offset", offset)
			items = append(items, item)
			if len(items) > 200 {
				items = items[1:]
			}
		}
	}
	first := start
	if len(items) > 0 {
		first = items[0].vals["offset"].(int64)
	}
	older := first
	if first >= end {
		older = max(0, end-nativeLimit)
	}
	var beforeOut any
	if older > 0 && (start != 0 || len(items) == 200) {
		beforeOut = older
	}
	messages := make([]any, len(items))
	for i, item := range items {
		messages[i] = item
	}
	return dict("conversation", chosen, "messages", messages, "before", beforeOut, "window_bytes", nativeLimit), nil
}

// readCodexTitles reads thread names from the tail of session_index.jsonl;
// any read failure keeps what was read so far.
func readCodexTitles(path string, titles map[string]string) {
	f, err := openPy(path)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.f.Stat()
	if err != nil {
		return
	}
	size := fi.Size()
	f.pos = max(0, size-4*nativeLimit)
	if size > 4*nativeLimit {
		if _, err := f.readline(nativeLimit); err != nil {
			return
		}
	}
	for {
		line, err := f.readline(-1)
		if err != nil || len(line) == 0 {
			return
		}
		v, err := jsonLoadsBytes(line)
		if err != nil {
			continue
		}
		entry, ok := v.(*pyDict)
		if !ok {
			continue
		}
		name, ok := entry.getOr("thread_name", nil).(string)
		if !ok {
			continue
		}
		if id, ok := entry.getOr("id", nil).(string); ok {
			titles[id] = runePrefix(name, 160)
		}
	}
}

// conversationLiveMain is `native-conversation-live AGENT WORKSPACE CID SINCE`.
func conversationLiveMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 4 {
		return crashed(stderr, fmt.Errorf("%w: not enough values to unpack", errCrash))
	}
	out, err := conversationLive(args[0], realpathLoose(args[1]), args[2], args[3])
	if err != nil {
		return printError(stdout, stderr, err)
	}
	fmt.Fprintln(stdout, jsonDumps(out, true))
	return 0
}

// livePage caps items per poll so a huge backlog cannot stall a request.
const livePage = 500

func conversationLive(agent, workspace, cid, since string) (*pyDict, error) {
	if agent != "codex" && agent != "claude" {
		return nil, pyError("ValueError", "Structured chat is available for Claude and Codex; use terminal text for this agent")
	}
	if !nativeUUID.MatchString(cid) {
		return nil, pyError("ValueError", "Choose a saved conversation ID")
	}
	store := storeFor(agent, workspace)
	base := realpathLoose(store.base)
	found := ""
	for _, file := range pyGlob(store.pattern, store.recursive) {
		real := realpathLoose(file)
		if !commonpathIs(base, real) || !isFile(real) || !strings.Contains(basename(file), cid) {
			continue
		}
		info, err := nativeMetadata(real, agent, &workspace, nativeLimit)
		if err != nil {
			continue
		}
		if info != nil && info.vals["id"] == cid {
			found = real
			break
		}
	}
	if found == "" {
		return nil, pyError("ValueError", "Conversation not found in this workspace on this target")
	}
	fi, err := os.Stat(found)
	if err != nil {
		return nil, osError(err, found)
	}
	size := fi.Size()
	// No cursor yet: start from a recent tail window rather than the whole
	// history, exactly like the history picker's first page.
	start := max(0, size-nativeLimit)
	if since != "" {
		if start, err = pyIntParse(since); err != nil {
			return nil, err
		}
	}
	if start < 0 || start > size {
		start = 0
	}
	items := []any{}
	cursor := start
	f, err := openPy(found)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	f.pos = start
	for f.pos < size && len(items) < livePage {
		pos := f.pos
		line, err := f.readline(min(nativeLimit+1, size-pos))
		if err != nil {
			return nil, err
		}
		if len(line) == 0 || line[len(line)-1] != '\n' {
			break // nothing more, or a trailing in-progress write
		}
		cursor = f.pos
		v, err := jsonLoadsBytes(line)
		if err != nil {
			continue
		}
		row, ok := v.(*pyDict)
		if !ok {
			continue
		}
		for i, block := range structuredRecords(row, agent, 64000) {
			block.set("id", fmt.Sprintf("%d-%d", pos, i))
			items = append(items, block)
		}
	}
	return dict("items", items, "cursor", cursor, "truncated", len(items) >= livePage), nil
}
