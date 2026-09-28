package helpers

// Port of internal/api/scripts/native_search_read.py: open a verified
// indexed match with neighbouring visible messages, re-reading every shown
// message from the transcript and checking it against the index.

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// nativeSearchReadMain is `native-search-read [--] AGENT DOCUMENT CID CWD
// OFFSET FINGERPRINT PROFILE_KEY QUERY [MODE [ANCHOR]]`.
func nativeSearchReadMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	const prog = "native-search-read {codex,claude} document cid cwd offset fingerprint profile_key query [mode] [anchor]"
	var positional []string
	for i, a := range args {
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" && !isNegativeNumber(a) {
			return argparseError(stderr, prog, "unrecognized arguments: "+a)
		}
		positional = append(positional, a)
	}
	if len(positional) < 8 || len(positional) > 10 {
		return argparseError(stderr, prog, "wrong number of arguments")
	}
	agent := positional[0]
	if agent != "codex" && agent != "claude" {
		return argparseError(stderr, prog, "argument agent: invalid choice: "+strRepr(agent)+" (choose from 'codex', 'claude')")
	}
	document, err := pyIntParse(positional[1])
	if err != nil {
		return argparseError(stderr, prog, "argument document: invalid int value: "+strRepr(positional[1]))
	}
	offset, err := pyIntParse(positional[4])
	if err != nil {
		return argparseError(stderr, prog, "argument offset: invalid int value: "+strRepr(positional[4]))
	}
	mode := "match"
	if len(positional) > 8 {
		mode = positional[8]
	}
	var anchor *int64
	if len(positional) > 9 {
		n, err := pyIntParse(positional[9])
		if err != nil {
			return argparseError(stderr, prog, "argument anchor: invalid int value: "+strRepr(positional[9]))
		}
		anchor = &n
	}
	cid, cwd, fingerprint, profile, query := positional[2], positional[3], positional[5], positional[6], positional[7]
	home, cache := searchHomeAndCache(agent)
	out, err := func() (*pyDict, error) {
		ix, err := openSearchIndex(cache, home, agent)
		if err != nil {
			return nil, err
		}
		defer ix.Close()
		if ix.key != profile {
			return nil, pyError("ValueError", "Native profile changed; run the search again")
		}
		return ix.readMatch(document, cid, cwd, offset, fingerprint, query, mode, anchor)
	}()
	if err != nil {
		return printError(stdout, stderr, err)
	}
	fmt.Fprintln(stdout, jsonDumps(out, false))
	return 0
}

func isNegativeNumber(s string) bool {
	_, err := pyIntParse(s)
	return err == nil
}

// readMatch reads inside one read transaction, always rolled back, so every
// query sees the same index state.
func (ix *searchIndex) readMatch(document int64, cid, cwd string, offset int64, fingerprint, query, mode string, anchor *int64) (*pyDict, error) {
	if _, err := ix.db.exec("BEGIN"); err != nil {
		return nil, err
	}
	defer func() { _, _ = ix.db.exec("ROLLBACK") }()
	return ix.readMatchIn(document, cid, cwd, offset, fingerprint, query, mode, anchor)
}

func (ix *searchIndex) readMatchIn(document int64, cid, cwd string, offset int64, fingerprint, query, mode string, anchor *int64) (*pyDict, error) {
	doc, err := ix.db.queryRow("SELECT * FROM documents WHERE id=? AND cid=? AND cwd=?", document, cid, cwd)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, pyError("ValueError", "Search result is stale; run the search again")
	}
	path, err := realpath(pathStr(asString(doc["path"])), true)
	if err != nil {
		return nil, err
	}
	if !isUnder(realpathLoose(ix.base()), path) || suffix(path) != ".jsonl" {
		return nil, pyError("ValueError", "Conversation is outside this native profile")
	}
	selected, err := ix.db.queryRow("SELECT * FROM messages WHERE doc=? AND offset=? AND fingerprint=?", document, offset, fingerprint)
	if err != nil {
		return nil, err
	}
	if selected == nil {
		return nil, pyError("ValueError", "Matched message changed; run the search again")
	}
	previous, err := ix.db.queryAll("SELECT offset,end_offset,fingerprint FROM messages WHERE doc=? AND offset<? ORDER BY offset DESC LIMIT 5", -1, document, offset)
	if err != nil {
		return nil, err
	}
	following, err := ix.db.queryAll("SELECT offset,end_offset,fingerprint FROM messages WHERE doc=? AND offset>? ORDER BY offset LIMIT 5", -1, document, offset)
	if err != nil {
		return nil, err
	}
	if mode != "match" && mode != "before" && mode != "after" && mode != "latest" {
		return nil, pyError("ValueError", "Invalid conversation page")
	}
	if mode == "before" || mode == "after" {
		if anchor == nil || *anchor < 0 {
			return nil, pyError("ValueError", "Invalid page boundary")
		}
		hit, err := ix.db.queryRow("SELECT 1 AS hit FROM messages WHERE doc=? AND offset=?", document, *anchor)
		if err != nil {
			return nil, err
		}
		if hit == nil {
			return nil, pyError("ValueError", "Page boundary changed; run the search again")
		}
	}
	var rows []map[string]any
	switch mode {
	case "before":
		rows, err = ix.db.queryAll("SELECT * FROM messages WHERE doc=? AND offset<? ORDER BY offset DESC LIMIT 11", -1, document, *anchor)
		reverseRows(rows)
	case "after":
		rows, err = ix.db.queryAll("SELECT * FROM messages WHERE doc=? AND offset>? ORDER BY offset LIMIT 11", -1, document, *anchor)
	case "latest":
		rows, err = ix.db.queryAll("SELECT * FROM messages WHERE doc=? ORDER BY offset DESC LIMIT 11", -1, document)
		reverseRows(rows)
	default:
		rows = append(rows, previous...)
		reverseRows(rows)
		rows = append(rows, selected)
		rows = append(rows, following...)
	}
	if err != nil {
		return nil, err
	}
	page := map[int64]bool{}
	for _, r := range rows {
		page[asInt64(r["offset"])] = true
	}
	// Revalidate the original match on every page, including pages not
	// displaying it.
	checks := rows
	if !page[offset] {
		checks = append([]map[string]any{selected}, rows...)
	}
	f, err := openPy(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.f.Stat()
	if err != nil {
		return nil, osError(err, "")
	}
	st := statOf(fi)
	if !fi.Mode().IsRegular() || st.dev != asInt64(doc["device"]) || st.ino != asInt64(doc["inode"]) {
		return nil, pyError("ValueError", "Conversation file changed; run the search again")
	}
	info, err := nativeMetadata(path, ix.agent, &cwd, searchLineLimit)
	if err != nil {
		return nil, err
	}
	if info == nil || info.vals["id"] != cid {
		return nil, pyError("ValueError", "Conversation identity changed; run the search again")
	}
	messages := []any{}
	changed := 0
	before, after := 0, 0
	for _, row := range checks {
		rowOffset := asInt64(row["offset"])
		if rowOffset != 0 {
			f.pos = rowOffset - 1
			b, err := f.read(1)
			if err != nil {
				return nil, err
			}
			if string(b) != "\n" {
				return nil, pyError("ValueError", "Conversation layout changed; run the search again")
			}
		}
		f.pos = rowOffset
		line, err := f.readline(searchLineLimit + 1)
		if err != nil {
			return nil, err
		}
		var message *pyDict
		if len(line) > 0 && line[len(line)-1] == '\n' && len(line) <= searchLineLimit {
			if v, err := jsonLoadsBytes(line); err == nil {
				if raw, ok := v.(*pyDict); ok {
					message = nativeRecord(raw, ix.agent, noLimit)
				}
			}
		}
		if message == nil || messageFingerprint(message.vals["role"].(string), message.vals["text"].(string)) != asString(row["fingerprint"]) {
			if rowOffset == offset {
				return nil, pyError("ValueError", "Matched message changed; run the search again")
			}
			changed++
			continue
		}
		if !page[rowOffset] {
			continue
		}
		matched := rowOffset == offset
		text := message.vals["text"].(string)
		truncated := runeLen(text) > 64000
		if matched && truncated {
			opening, closing := searchMarkers()
			hl, err := ix.db.queryRow("SELECT highlight(message_search,0,?,?) AS h FROM message_search WHERE rowid=? AND message_search MATCH ?",
				opening, closing, selected["id"], searchMatchExpr(query))
			if err != nil {
				return nil, err
			}
			if hl == nil {
				return nil, pyError("ValueError", "Search text changed; run the search again")
			}
			text, truncated = markedExcerpt(asString(hl["h"]), opening, closing, 64000)
		} else {
			text = runePrefix(text, 64000)
		}
		message.set("text", text)
		message.set("offset", rowOffset)
		message.set("matched", matched)
		message.set("truncated", truncated)
		messages = append(messages, message)
		if rowOffset < offset {
			before++
		} else if rowOffset > offset {
			after++
		}
	}
	current, err := os.Stat(path)
	if err != nil {
		return nil, osError(err, path)
	}
	cur := statOf(current)
	if cur.dev != st.dev || cur.ino != st.ino {
		return nil, pyError("ValueError", "Conversation file changed; run the search again")
	}
	var pageBefore, pageAfter any
	if len(rows) > 0 {
		low, high := asInt64(rows[0]["offset"]), asInt64(rows[len(rows)-1]["offset"])
		if hit, err := ix.db.queryRow("SELECT 1 AS hit FROM messages WHERE doc=? AND offset<? LIMIT 1", document, low); err != nil {
			return nil, err
		} else if hit != nil {
			pageBefore = low
		}
		if hit, err := ix.db.queryRow("SELECT 1 AS hit FROM messages WHERE doc=? AND offset>? LIMIT 1", document, high); err != nil {
			return nil, err
		} else if hit != nil {
			pageAfter = high
		}
	}
	complete := asInt64(doc["pending"]) == 0 && cur.size == asInt64(doc["observed"]) && cur.mtimeNs == asInt64(doc["stamp"])
	return dict("conversation", info, "messages", messages, "changed_neighbors", changed, "before", pageBefore, "after", pageAfter,
		"page_mode", mode, "context_before_count", before, "context_after_count", after, "index_complete", complete), nil
}

func reverseRows(rows []map[string]any) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}
