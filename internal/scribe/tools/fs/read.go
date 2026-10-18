package fs

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/web"
)

const (
	maxReadLines = 2000
	maxReadBytes = 250 * 1024
	maxLineChars = 2000
	sniffBytes   = 8000
	// maxImageBytes is the largest image read attaches for the model.
	maxImageBytes = 10 << 20
	// defaultImageCols and maxImageCols bound the display width an image is
	// shown at, in terminal columns (soul_vision's cols).
	defaultImageCols = 120
	maxImageCols     = 200
	// minTextCols and maxTextCols bound cols for text: the longest line shown.
	minTextCols = 20
	maxTextCols = 2000
	// maxOutlineEntries caps the symbol outline a truncated read adds.
	maxOutlineEntries = 60
)

var imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}

// videoExt names video files. read does not decode them (see readOne).
var videoExt = map[string]bool{".mp4": true, ".mkv": true, ".webm": true, ".avi": true, ".mov": true, ".m4v": true}

var binaryExt = map[string]bool{
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".o": true, ".a": true,
	".class": true, ".jar": true, ".zip": true, ".gz": true, ".tgz": true, ".tar": true,
	".7z": true, ".rar": true, ".bz2": true, ".xz": true, ".zst": true, ".pdf": true,
	".mp3": true, ".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".wav": true,
	".ogg": true, ".flac": true, ".woff": true, ".woff2": true, ".ttf": true, ".otf": true,
	".wasm": true, ".bin": true, ".iso": true, ".db": true, ".sqlite": true, ".pyc": true,
}

// NewRead returns the read tool. Each call makes a fresh read-state table, so
// one registry (one session) shares one table.
func NewRead() tools.Tool {
	st := newReadState()
	return &tool{
		spec: newSpec("read",
			"Read a text file with line numbers, or an https:// page as text. Options: "+
				"lines=\"120-180\" reads that line range (1-based, inclusive); several ranges are allowed, as \"10-20,50-60\" or [\"10-20\",\"50-60\"]. "+
				"symbol=\"Name\" (or name=) returns only that definition with its line numbers; add target=\"function\" (or class, type, interface, enum, var) to pick a kind, and \"Type.Method\" to narrow. "+
				"offset and limit page through a file from the top. Pass paths, or files (per-file options), to read several files in one call. "+
				"A directory returns a listing. Read a file before editing it. "+
				"Re-reading lines that have not changed since your last read returns a short \"unchanged\" notice; pass force=true to get them again. "+
				"ifHash=<hash> skips content whose hash you already hold, using the hash a lines= or symbol= read printed. "+
				"A large full read ends with the rest of the file's symbol outline when the code map knows it. "+
				"An image is attached for viewing; cols sets its display width. A lines= or symbol= read ends with a hash line; that hash is what edit_file's ifHash expects for that exact text.",
			tools.Schema(map[string]any{
				"path":   tools.Str("File to read, relative to the project root, or an https:// URL to read as text."),
				"paths":  tools.Arr("Several files to read in one call. lines, symbol and the other options apply to each.", map[string]any{"type": "string"}),
				"files":  tools.Arr("Several files, each with its own options: {path, lines?, ranges?, symbol?, target?, name?, ifHash?, offset?, limit?, cols?}. A plain string is a path.", map[string]any{"type": "object"}),
				"offset": tools.Int("First line to show, 1-based. Default 1. Ignored when lines, ranges or symbol is set."),
				"limit":  tools.Int("Number of lines to show. Default and maximum 2000. Ignored when lines, ranges or symbol is set."),
				"lines":  tools.Str(`Line range(s) to show, e.g. "120-180" (1-based, inclusive), "120-" to the end, or "10-20,50-60" for several. An array of such strings also works.`),
				"ranges": tools.Arr(`Line ranges as objects {start, end} (end optional: to the end of the file).`, map[string]any{"type": "object"}),
				"symbol": tools.Str(`Show only this definition (function, method, type, class), with its line numbers. Use "Type.Method" to pick a method. Cannot be combined with lines.`),
				"target": tools.Str(`Kind of definition to pick with name or symbol: function, method, class, struct, type, interface, enum, trait, var or module.`),
				"name":   tools.Str("Definition name for a symbol read, used with target (the same as symbol=)."),
				"ifHash": tools.Str("Hash printed by an earlier lines= or symbol= read of the same text, or the file hash from an unchanged notice. When it matches the current content, the text is not sent again."),
				"cols":   tools.Int("Display width. For an image, the width in terminal columns (default 120, max 200). For text, the longest line shown (20 to 2000); longer lines are cut and say so."),
				"force":  tools.Bool("Send the content again even if it has not changed since your last read. Default false."),
			})),
		readOnly: true,
		run: func(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
			return runRead(ctx, env, st, args)
		},
	}
}

// readSpec is the part of a read request that can be given per file (inside
// files) or once for every file (at the top level).
type readSpec struct {
	Path   string          `json:"path"`
	Offset flexInt         `json:"offset"`
	Limit  flexInt         `json:"limit"`
	Lines  json.RawMessage `json:"lines"`
	Ranges json.RawMessage `json:"ranges"`
	Symbol json.RawMessage `json:"symbol"`
	Name   json.RawMessage `json:"name"`
	Target json.RawMessage `json:"target"`
	Kind   json.RawMessage `json:"kind"`
	IfHash json.RawMessage `json:"ifHash"`
	Cols   flexInt         `json:"cols"`
}

type readArgs struct {
	readSpec
	Paths flexStrings     `json:"paths"`
	Files json.RawMessage `json:"files"`
	Force flexBool        `json:"force"`
}

// window is a 1-based inclusive line range. hi is math.MaxInt for "to the end".
type window struct{ lo, hi int }

// readReq is what one file read asks for, after the arguments are checked.
type readReq struct {
	// windows are the lines to show, sorted and merged. A full read has one
	// window (offset to offset+limit-1); a lines=, ranges= or symbol= read
	// has the requested ranges.
	windows []window
	ranged  bool // lines, ranges or symbol given: the reply ends with a hash line
	symbol  string
	kind    string
	ifHash  string
	force   bool
	cols    int // clamped to minTextCols..maxTextCols, or 0 for the default
}

// fileReq pairs a path with what to read from it.
type fileReq struct {
	path string
	rq   readReq
}

// readOut is one file's reply.
type readOut struct {
	body    string
	summary string
	image   string
	cols    int // image display width
}

func runRead(ctx context.Context, env *tools.Env, st *readState, args json.RawMessage) tools.Result {
	var a readArgs
	if err := tools.Decode(args, &a); err != nil {
		return failTagged(err)
	}
	reqs, err := a.requests()
	if err != nil {
		return failTagged(err)
	}

	if len(reqs) == 1 {
		out, err := readOne(ctx, env, st, reqs[0].path, reqs[0].rq)
		if err != nil {
			return failTagged(err)
		}
		res := tools.Result{Output: out.body, Summary: out.summary}
		if out.image != "" {
			res.Meta = map[string]any{"image": out.image, "cols": out.cols}
		}
		return res
	}

	var sb strings.Builder
	failures := 0
	for _, f := range reqs {
		sb.WriteString("── " + f.path + " ──\n")
		out, err := readOne(ctx, env, st, f.path, f.rq)
		if err != nil {
			failures++
			sb.WriteString("error: " + err.Error())
		} else {
			sb.WriteString(out.body)
		}
		sb.WriteString("\n\n")
	}
	return tools.Result{
		Output:  strings.TrimRight(sb.String(), "\n"),
		IsError: failures == len(reqs),
		Summary: fmt.Sprintf("%d files, %d failed", len(reqs), failures),
	}
}

// requests turns the arguments into one request per file. Top-level options
// apply to every path in paths; an entry in files may override them.
func (a readArgs) requests() ([]fileReq, error) {
	force := bool(a.Force)
	var out []fileReq
	if p := strings.TrimSpace(a.Path); p != "" {
		rq, err := buildReq(a.readSpec, force)
		if err != nil {
			return nil, err
		}
		out = append(out, fileReq{p, rq})
	}
	for _, p := range a.Paths {
		sp := a.readSpec
		sp.Path = p
		rq, err := buildReq(sp, force)
		if err != nil {
			return nil, err
		}
		out = append(out, fileReq{strings.TrimSpace(p), rq})
	}
	if raw := strings.TrimSpace(string(a.Files)); raw != "" && raw != "null" {
		items, err := fileItems(a.Files)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			sp, err := fileSpec(it)
			if err != nil {
				return nil, err
			}
			sp = inherit(a.readSpec, sp)
			if strings.TrimSpace(sp.Path) == "" {
				return nil, fmt.Errorf("each files entry needs a path, e.g. {\"path\": \"main.go\", \"lines\": \"1-40\"}")
			}
			rq, err := buildReq(sp, force)
			if err != nil {
				return nil, err
			}
			out = append(out, fileReq{strings.TrimSpace(sp.Path), rq})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("read needs path (or paths for several files).")
	}
	return out, nil
}

// fileItems decodes files, which should be an array. A JSON string holding
// the array is accepted, and so is one object given alone.
func fileItems(raw json.RawMessage) ([]json.RawMessage, error) {
	data := []byte(strings.TrimSpace(string(raw)))
	if len(data) > 0 && data[0] == '"' {
		var inner string
		if err := json.Unmarshal(data, &inner); err == nil {
			data = []byte(strings.TrimSpace(inner))
		}
	}
	var items []json.RawMessage
	if len(data) > 0 && data[0] == '[' {
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, fmt.Errorf("files must be an array of path strings or {path, lines, ...} objects: %v", err)
		}
		return items, nil
	}
	return []json.RawMessage{data}, nil
}

// fileSpec reads one files entry: a path string or an object.
func fileSpec(it json.RawMessage) (readSpec, error) {
	var sp readSpec
	t := strings.TrimSpace(string(it))
	if strings.HasPrefix(t, `"`) {
		var p string
		if err := json.Unmarshal(it, &p); err != nil {
			return sp, err
		}
		sp.Path = p
		return sp, nil
	}
	if err := json.Unmarshal(it, &sp); err != nil {
		return sp, fmt.Errorf("each files entry must be a path string or an object {path, lines, ...}: %v", err)
	}
	return sp, nil
}

// inherit fills what a files entry leaves out from the top-level options. A
// selector (lines, ranges, symbol and their kinds) given on the entry replaces
// the top-level selector as a group, so a range and a symbol are never mixed
// by accident.
func inherit(top, sp readSpec) readSpec {
	if !hasSelector(sp) {
		sp.Lines, sp.Ranges, sp.Symbol, sp.Name, sp.Target, sp.Kind = top.Lines, top.Ranges, top.Symbol, top.Name, top.Target, top.Kind
	}
	if sp.Offset == 0 {
		sp.Offset = top.Offset
	}
	if sp.Limit == 0 {
		sp.Limit = top.Limit
	}
	if sp.Cols == 0 {
		sp.Cols = top.Cols
	}
	if len(sp.IfHash) == 0 {
		sp.IfHash = top.IfHash
	}
	return sp
}

func hasSelector(sp readSpec) bool {
	for _, raw := range []json.RawMessage{sp.Lines, sp.Ranges, sp.Symbol, sp.Name, sp.Target, sp.Kind} {
		if rawText(raw) != "" {
			return true
		}
	}
	return false
}

// buildReq checks one file's options and makes its request.
func buildReq(sp readSpec, force bool) (readReq, error) {
	rq := readReq{force: force, ifHash: rawText(sp.IfHash)}
	// lines takes the same shapes as ranges: a string, an array of strings
	// or objects, or a bare number.
	lw, err := parseRanges(sp.Lines)
	if err != nil {
		return rq, err
	}
	rw, err := parseRanges(sp.Ranges)
	if err != nil {
		return rq, err
	}
	wins := append(lw, rw...)

	symRaw := rawText(sp.Symbol)
	name := rawText(sp.Name)
	kind := rawText(sp.Target)
	if kind == "" {
		kind = rawText(sp.Kind)
	}
	if name == "" && symRaw != "" {
		if k, rest, ok := splitKindWord(symRaw); ok {
			if kind == "" {
				kind = k
			}
			name = rest
		} else {
			name = symRaw
		}
	}
	if name != "" || kind != "" {
		if len(wins) > 0 {
			return rq, fmt.Errorf("read takes lines or symbol, not both. Use symbol to get one definition, or lines to get a range.")
		}
		if name == "" {
			return rq, fmt.Errorf(`target needs a name too, e.g. {"path": "x.go", "target": "function", "name": "Run"}`)
		}
		rq.ranged = true
		rq.symbol = name
		rq.kind = kind
		return rq, nil
	}

	if len(wins) > 0 {
		rq.ranged = true
		rq.windows = normalizeWindows(wins)
	} else {
		offset, limit := int(sp.Offset), int(sp.Limit)
		if offset < 1 {
			offset = 1
		}
		if limit < 1 || limit > maxReadLines {
			limit = maxReadLines
		}
		rq.windows = []window{{offset, offset + limit - 1}}
	}
	if c := int(sp.Cols); c > 0 {
		rq.cols = min(max(c, minTextCols), maxTextCols)
	}
	return rq, nil
}

// splitKindWord splits "function Run" into its kind and name, for a symbol
// argument that merges the two.
func splitKindWord(sym string) (kind, name string, ok bool) {
	f := strings.Fields(sym)
	if len(f) == 2 && symbolKindWords[strings.ToLower(f[0])] {
		return f[0], f[1], true
	}
	return "", "", false
}

// parseRangeText reads "10-20", "7", "120-" or several of them separated by
// commas or semicolons.
func parseRangeText(s string) ([]window, error) {
	var out []window
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' }) {
		if strings.TrimSpace(part) == "" {
			continue
		}
		lo, hi, err := parseLineRange(part)
		if err != nil {
			return nil, err
		}
		if hi == 0 {
			hi = math.MaxInt
		}
		out = append(out, window{lo, hi})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf(`lines must look like "120-180" (1-based, inclusive), got %q`, s)
	}
	return out, nil
}

// parseRanges reads the ranges argument: an array of {start, end} objects or
// of range strings, one object, or a range string.
func parseRanges(raw json.RawMessage) ([]window, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil, nil
	}
	bad := fmt.Errorf(`ranges need start >= 1 and end >= start, e.g. [{"start": 120, "end": 180}]`)
	switch s[0] {
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, bad
		}
		var out []window
		for _, it := range items {
			w, err := parseRangeItem(it)
			if err != nil {
				return nil, err
			}
			out = append(out, w...)
		}
		return out, nil
	case '{':
		return parseRangeItem(raw)
	}
	return parseRangeText(rawText(raw))
}

func parseRangeItem(it json.RawMessage) ([]window, error) {
	t := strings.TrimSpace(string(it))
	if strings.HasPrefix(t, "{") {
		var o struct {
			Start flexInt `json:"start"`
			End   flexInt `json:"end"`
		}
		if err := json.Unmarshal(it, &o); err != nil || o.Start < 1 {
			return nil, fmt.Errorf(`ranges need start >= 1 and end >= start, e.g. [{"start": 120, "end": 180}]`)
		}
		hi := int(o.End)
		if hi == 0 {
			hi = math.MaxInt
		}
		if hi < int(o.Start) {
			return nil, fmt.Errorf(`ranges need start >= 1 and end >= start, e.g. [{"start": 120, "end": 180}]`)
		}
		return []window{{int(o.Start), hi}}, nil
	}
	return parseRangeText(rawText(it))
}

// normalizeWindows sorts ranges and merges the ones that overlap or touch.
func normalizeWindows(ws []window) []window {
	sort.SliceStable(ws, func(i, j int) bool { return ws[i].lo < ws[j].lo })
	out := []window{ws[0]}
	for _, w := range ws[1:] {
		last := &out[len(out)-1]
		if w.lo <= last.hi || w.lo == last.hi+1 {
			last.hi = max(last.hi, w.hi)
			continue
		}
		out = append(out, w)
	}
	return out
}

// windowLabel heads one range when a read shows several.
func windowLabel(w window) string {
	if w.hi == math.MaxInt {
		return fmt.Sprintf("── lines %d-end ──", w.lo)
	}
	return fmt.Sprintf("── lines %d-%d ──", w.lo, w.hi)
}

// rawText reads an argument that should be a string but may arrive as a bare
// number. null and absent are "".
func rawText(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		var out string
		if err := json.Unmarshal(raw, &out); err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}
	return s
}

// parseLineRange reads "A-B" (1-based, inclusive), "A" (one line) or "A-"
// (from A to the end, reported as hi 0).
func parseLineRange(s string) (lo, hi int, err error) {
	bad := fmt.Errorf(`lines must look like "120-180" (1-based, inclusive), got %q`, s)
	a, b, dashed := strings.Cut(strings.TrimSpace(s), "-")
	lo, err = strconv.Atoi(strings.TrimSpace(a))
	if err != nil || lo < 1 {
		return 0, 0, bad
	}
	if !dashed {
		return lo, lo, nil
	}
	if strings.TrimSpace(b) == "" {
		return lo, 0, nil
	}
	hi, err = strconv.Atoi(strings.TrimSpace(b))
	if err != nil || hi < lo {
		return 0, 0, bad
	}
	return lo, hi, nil
}

// source is one thing a read streams: a file, or a fetched web page.
type source struct {
	env    *tools.Env
	key    string // read-state key
	abs    string // the file's absolute path; "" for a web page
	rel    string // how the reply names it
	r      io.Reader
	header string // symbol header, when the read is a symbol read
}

// readOne reads one path. It marks the file read on success.
func readOne(ctx context.Context, env *tools.Env, st *readState, p string, rq readReq) (readOut, error) {
	if isWebPath(p) {
		return readWeb(ctx, env, st, p, rq)
	}
	t, err := resolve(ctx, env, "read", p, accessRead)
	if err != nil {
		return readOut{}, err
	}
	fi, err := statPath(t)
	if err != nil {
		return readOut{}, err
	}
	if fi.IsDir() {
		if rq.ranged {
			return readOut{}, fmt.Errorf("lines and symbol read a file, and %s is a directory. Use list_dir instead.", t.rel)
		}
		body, n, capped := renderTreeAnnotated(ctx, env.Workdir, t.abs, 1, maxListEntries, fileAnnotator(env), mapRank(env))
		if capped {
			body += fmt.Sprintf("\n(Listing capped at %d entries. Use list_dir with a deeper path.)", maxListEntries)
		}
		return readOut{body: body, summary: fmt.Sprintf("%d entries", n)}, nil
	}
	ext := strings.ToLower(filepath.Ext(t.abs))
	if imageExt[ext] {
		if fi.Size() > maxImageBytes {
			return readOut{}, fmt.Errorf("Image %s is %s. Images over %s cannot be attached; resize or crop it first.", t.rel, humanSize(fi.Size()), humanSize(maxImageBytes))
		}
		if env.Files != nil {
			env.Files.MarkRead(t.abs)
		}
		cols := defaultImageCols
		if rq.cols > 0 {
			cols = min(rq.cols, maxImageCols)
		}
		return readOut{
			body:    fmt.Sprintf("Image %s (%s, %s). The image is attached for viewing, %d columns wide.", t.rel, ext[1:], humanSize(fi.Size()), cols),
			summary: "image",
			image:   t.rel,
			cols:    cols,
		}, nil
	}
	if videoExt[ext] {
		return readOut{}, fmt.Errorf("Video %s is not read by this tool. Extract the frames you need as images, then read those.", t.rel)
	}
	if binaryExt[ext] {
		return readOut{}, fmt.Errorf("Cannot read binary file: %s (%s, %s). Use a tool that handles this format.", t.rel, ext, humanSize(fi.Size()))
	}

	s := source{env: env, key: readKey(env, t.abs), abs: t.abs, rel: t.rel}
	if rq.symbol != "" {
		if fi.Size() > maxSymbolBytes {
			return readOut{}, fmt.Errorf("%s is too large for symbol lookup (%s). Read it with lines= instead.", t.rel, humanSize(fi.Size()))
		}
		src, err := readFileForRead(t.abs)
		if err != nil {
			return readOut{}, err
		}
		sp, err := findSymbol(env, t, src, rq.symbol, rq.kind)
		if err != nil {
			return readOut{}, err
		}
		rq.windows = []window{{sp.Start, sp.End}}
		s.header = fmt.Sprintf("%s (%s), lines %d-%d:\n", sp.Name, sp.Kind, sp.Start, sp.End)
	}

	f, err := openForRead(t.abs)
	if err != nil {
		return readOut{}, err
	}
	defer f.Close()
	head := make([]byte, sniffBytes)
	n, _ := io.ReadFull(f, head)
	if isBinaryData(head[:n]) {
		return readOut{}, fmt.Errorf("Cannot read binary file: %s (%s, %s). Use a tool that handles this format.", t.rel, ext, humanSize(fi.Size()))
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return readOut{}, err
	}
	if env.Files != nil {
		env.Files.MarkRead(t.abs)
	}
	s.r = f
	return renderSource(st, s, rq)
}

// isWebPath reports whether p names an http or https address.
func isWebPath(p string) bool {
	l := strings.ToLower(strings.TrimSpace(p))
	return strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://")
}

// readWeb reads a page through the same checks as fetch_page. Its text is
// numbered like a file, so lines, symbol and ifHash work on it too. The
// reply names the URL first.
func readWeb(ctx context.Context, env *tools.Env, st *readState, raw string, rq readReq) (readOut, error) {
	if u, err := url.Parse(strings.TrimSpace(raw)); err == nil {
		if ext := strings.ToLower(path.Ext(u.Path)); imageExt[ext] || videoExt[ext] || binaryExt[ext] {
			return readOut{}, fmt.Errorf("read does not fetch %s files from a URL. Download it and read it by path, or use fetch_page for a page.", ext)
		}
	}
	src, err := web.FetchSource(ctx, env, raw)
	if err != nil {
		return readOut{}, err
	}
	s := source{env: env, key: readKey(env, src.URL), rel: src.URL, r: strings.NewReader(src.Text)}
	if rq.symbol != "" {
		sp, err := findSymbol(env, target{rel: src.URL}, []byte(src.Text), rq.symbol, rq.kind)
		if err != nil {
			return readOut{}, err
		}
		rq.windows = []window{{sp.Start, sp.End}}
		s.header = fmt.Sprintf("%s (%s), lines %d-%d:\n", sp.Name, sp.Kind, sp.Start, sp.End)
	}
	out, err := renderSource(st, s, rq)
	if err != nil {
		return readOut{}, err
	}
	if out.summary != "unchanged" {
		head := "URL: " + src.URL + "\n"
		if src.Title != "" {
			head = "Title: " + src.Title + "\n" + head
		}
		if src.Truncated {
			head += "(The page was cut at 1M characters.)\n"
		}
		out.body = head + "\n" + out.body
	}
	return out, nil
}

// renderSource streams s, keeps the lines rq asks for, and writes the reply:
// numbered lines, then the notices and the hash footer a range read needs.
func renderSource(st *readState, s source, rq readReq) (readOut, error) {
	fileSum := sha256.New()
	br := bufio.NewReaderSize(io.TeeReader(s.r, fileSum), 64*1024)
	full := !rq.ranged
	width := maxLineChars
	if rq.cols > 0 {
		width = rq.cols
	}
	wins := rq.windows

	var (
		sb                        strings.Builder
		shown                     []string
		spans                     [][2]int
		total, emitted, bytesOut  int
		clipped                   int
		cutAt                     int // first line not shown because of the size caps; 0 = none
		wi, lastWin               int
		firstEmitted, lastEmitted int
	)
	lastWin = -1
	for {
		line, rerr := br.ReadString('\n')
		if line != "" {
			total++
			n := total
			for wi < len(wins) && wins[wi].hi < n {
				wi++
			}
			if cutAt == 0 && wi < len(wins) && n >= wins[wi].lo {
				text := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
				entry := fmt.Sprintf("%6d  %s\n", n, clipLine(text, width))
				if emitted >= maxReadLines || bytesOut+len(entry) > maxReadBytes {
					cutAt = n
				} else {
					if len(wins) > 1 && wi != lastWin {
						label := windowLabel(wins[wi]) + "\n"
						sb.WriteString(label)
						bytesOut += len(label)
					}
					lastWin = wi
					if rq.cols > 0 && utf8.RuneCountInString(text) > width {
						clipped++
					}
					sb.WriteString(entry)
					bytesOut += len(entry)
					emitted++
					if emitted == 1 {
						firstEmitted = n
					}
					lastEmitted = n
					if k := len(spans); k > 0 && spans[k-1][1] == n-1 {
						spans[k-1][1] = n
					} else {
						spans = append(spans, [2]int{n, n})
					}
					if rq.ranged {
						shown = append(shown, text)
					}
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return readOut{}, rerr
		}
	}

	if total == 0 {
		return readOut{body: "(empty file)", summary: "0 lines"}, nil
	}
	if !anyStarts(wins, total) {
		return readOut{}, fmt.Errorf("offset %d is past the end of %s (%d lines).", wins[0].lo, s.rel, total)
	}
	fileHash := hex.EncodeToString(fileSum.Sum(nil))[:12]
	var windowHash string
	if rq.ranged {
		windowHash = shortHash(strings.Join(shown, "\n"))
	}

	// ifHash: the caller already holds this content. The file hash counts for
	// any read; the window hash only when every line of the window was shown.
	if rq.ifHash != "" && !rq.force && (rq.ifHash == fileHash || rq.ranged && cutAt == 0 && rq.ifHash == windowHash) {
		count := total
		if rq.ranged {
			count = emitted
		}
		return readOut{
			body:    fmt.Sprintf("unchanged:%s (%d lines) — content already in your context, not re-sent.", rq.ifHash, count),
			summary: "unchanged",
		}, nil
	}

	if emitted > 0 && st != nil && st.noteSpans(s.key, fileHash, spans, rq.force) {
		return readOut{
			body: fmt.Sprintf("%s unchanged since your last read (hash %s, %d lines). Lines %d-%d are already in your context; pass force=true to get them again.",
				s.rel, fileHash, total, firstEmitted, lastEmitted),
			summary: "unchanged",
		}, nil
	}

	body := strings.TrimSuffix(sb.String(), "\n")
	summary := fmt.Sprintf("%d lines", total)
	var cutNote string
	if clipped > 0 {
		cutNote = fmt.Sprintf("\n(%d lines were longer than cols=%d and were cut. Pass a larger cols to see them whole.)", clipped, rq.cols)
	}
	if full {
		body += cutNote
		if lastEmitted < total {
			body += fmt.Sprintf("\n… %d more lines; call read with offset=%d to continue.", total-lastEmitted, lastEmitted+1)
			if syms, known := mapSymbols(s.env, s.abs); known {
				body += outlineBelow(syms, lastEmitted, total)
			}
			summary = fmt.Sprintf("%d of %d lines", emitted, total)
		}
		// A plain read carries no hash line, but the memory hint still goes
		// on it: a full read is the usual way the model meets a file.
		return readOut{body: body + memoryFooter(s.env, s.rel), summary: summary}, nil
	}

	if cutAt > 0 {
		end := total
		for _, w := range wins {
			if w.lo <= cutAt && cutAt <= w.hi {
				end = min(w.hi, total)
			}
		}
		body += fmt.Sprintf("\n… lines %d-%d of this read were not shown; call read with lines=%d-%d to continue.", cutAt, end, cutAt, end)
	}
	body += cutNote
	if past := pastEndNote(wins, total); past != "" {
		body += past
	}
	body = s.header + body + "\nhash:" + windowHash + memoryFooter(s.env, s.rel)
	switch {
	case s.header != "":
		summary = strings.TrimSuffix(s.header, ":\n")
	case len(wins) == 1:
		summary = fmt.Sprintf("lines %d-%d", wins[0].lo, lastEmitted)
	default:
		summary = fmt.Sprintf("%d line ranges", len(wins))
	}
	return readOut{body: body, summary: summary}, nil
}

// anyStarts reports whether some window begins within a file of total lines.
func anyStarts(wins []window, total int) bool {
	for _, w := range wins {
		if w.lo <= total {
			return true
		}
	}
	return false
}

// pastEndNote names the windows of a multi-range read that begin after the
// end of the file. A single window past the end is an error instead.
func pastEndNote(wins []window, total int) string {
	if len(wins) < 2 {
		return ""
	}
	var parts []string
	for _, w := range wins {
		if w.lo > total {
			if w.hi == math.MaxInt {
				parts = append(parts, fmt.Sprintf("%d-end", w.lo))
			} else {
				parts = append(parts, fmt.Sprintf("%d-%d", w.lo, w.hi))
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("\n… lines %s are past the end of the file (%d lines) and were skipped.", strings.Join(parts, ", "), total)
}
