package fs

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const (
	maxReadLines = 2000
	maxReadBytes = 250 * 1024
	maxLineChars = 2000
	sniffBytes   = 8000
	// maxImageBytes is the largest image read attaches for the model.
	maxImageBytes = 10 << 20
)

var imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}

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
			"Read a text file with line numbers. Options: lines=\"120-180\" reads that line range (1-based, inclusive); "+
				"symbol=\"Name\" returns only that function, method, type or class with its line numbers (\"Type.Method\" to narrow). "+
				"offset and limit page through a file from the top. Pass paths to read several files in one call. "+
				"A directory returns a listing. Read a file before editing it. "+
				"Re-reading lines that have not changed since your last read returns a short \"unchanged\" notice; pass force=true to get them again. "+
				"A lines= or symbol= read ends with a hash line; that hash is what edit_file's ifHash expects for that exact text.",
			tools.Schema(map[string]any{
				"path":   tools.Str("File to read, relative to the project root."),
				"paths":  tools.Arr("Several files to read in one call. lines and symbol apply to each.", map[string]any{"type": "string"}),
				"offset": tools.Int("First line to show, 1-based. Default 1. Ignored when lines or symbol is set."),
				"limit":  tools.Int("Number of lines to show. Default and maximum 2000. Ignored when lines or symbol is set."),
				"lines":  tools.Str(`Line range to show, e.g. "120-180" (1-based, inclusive). "120-" reads to the end.`),
				"symbol": tools.Str(`Show only this definition (function, method, type, class), with its line numbers. Use "Type.Method" to pick a method. Cannot be combined with lines.`),
				"force":  tools.Bool("Send the content again even if it has not changed since your last read. Default false."),
			})),
		readOnly: true,
		run: func(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
			return runRead(ctx, env, st, args)
		},
	}
}

type readArgs struct {
	Path   string          `json:"path"`
	Paths  flexStrings     `json:"paths"`
	Offset flexInt         `json:"offset"`
	Limit  flexInt         `json:"limit"`
	Lines  json.RawMessage `json:"lines"`
	Symbol json.RawMessage `json:"symbol"`
	Force  flexBool        `json:"force"`
}

type readOut struct {
	body    string
	summary string
	image   string
}

// readSel is the part of a read request that applies to one file.
type readSel struct {
	offset, limit int
	end           int // last line the caller asked for (lines= or symbol=); 0 = to the end
	symbol        string
	ranged        bool // lines or symbol given: the reply ends with a hash line
	force         bool
}

func runRead(ctx context.Context, env *tools.Env, st *readState, args json.RawMessage) tools.Result {
	var a readArgs
	if err := tools.Decode(args, &a); err != nil {
		return failTagged(err)
	}
	var paths []string
	if a.Path != "" {
		paths = append(paths, a.Path)
	}
	paths = append(paths, a.Paths...)
	if len(paths) == 0 {
		return tools.Errorf("read needs path (or paths for several files).")
	}

	sel := readSel{offset: int(a.Offset), limit: int(a.Limit), force: bool(a.Force)}
	if sel.offset < 1 {
		sel.offset = 1
	}
	if sel.limit < 1 || sel.limit > maxReadLines {
		sel.limit = maxReadLines
	}
	lines := rawText(a.Lines)
	sel.symbol = rawText(a.Symbol)
	if lines != "" && sel.symbol != "" {
		return tools.Errorf("read takes lines or symbol, not both. Use symbol to get one definition, or lines to get a range.")
	}
	if lines != "" {
		lo, hi, err := parseLineRange(lines)
		if err != nil {
			return failTagged(err)
		}
		sel.offset = lo
		sel.limit = maxReadLines
		sel.end = hi
		if hi > 0 {
			sel.limit = min(hi-lo+1, maxReadLines)
		}
		sel.ranged = true
	}
	if sel.symbol != "" {
		sel.ranged = true
	}

	if len(paths) == 1 {
		out, err := readOne(ctx, env, st, paths[0], sel)
		if err != nil {
			return failTagged(err)
		}
		res := tools.Result{Output: out.body, Summary: out.summary}
		if out.image != "" {
			res.Meta = map[string]any{"image": out.image}
		}
		return res
	}

	var sb strings.Builder
	failures := 0
	for _, p := range paths {
		sb.WriteString("── " + p + " ──\n")
		out, err := readOne(ctx, env, st, p, sel)
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
		IsError: failures == len(paths),
		Summary: fmt.Sprintf("%d files, %d failed", len(paths), failures),
	}
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

// readOne reads one path. It marks the file read on success.
func readOne(ctx context.Context, env *tools.Env, st *readState, p string, sel readSel) (readOut, error) {
	t, err := resolve(ctx, env, "read", p, accessRead)
	if err != nil {
		return readOut{}, err
	}
	fi, err := statPath(t)
	if err != nil {
		return readOut{}, err
	}
	if fi.IsDir() {
		if sel.ranged {
			return readOut{}, fmt.Errorf("lines and symbol read a file, and %s is a directory. Use list_dir instead.", t.rel)
		}
		body, n, capped := renderTree(ctx, env.Workdir, t.abs, 1)
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
		return readOut{
			body:    fmt.Sprintf("Image %s (%s, %s). The image is attached for viewing.", t.rel, ext[1:], humanSize(fi.Size())),
			summary: "image",
			image:   t.rel,
		}, nil
	}
	if binaryExt[ext] {
		return readOut{}, fmt.Errorf("Cannot read binary file: %s (%s, %s). Use a tool that handles this format.", t.rel, ext, humanSize(fi.Size()))
	}

	var header string
	if sel.symbol != "" {
		if fi.Size() > maxSymbolBytes {
			return readOut{}, fmt.Errorf("%s is too large for symbol lookup (%s). Read it with lines= instead.", t.rel, humanSize(fi.Size()))
		}
		src, err := os.ReadFile(t.abs)
		if err != nil {
			return readOut{}, err
		}
		sp, err := findSymbol(env, t, src, sel.symbol)
		if err != nil {
			return readOut{}, err
		}
		sel.offset, sel.limit, sel.end = sp.Start, sp.End-sp.Start+1, sp.End
		if sel.limit > maxReadLines {
			sel.limit = maxReadLines
		}
		header = fmt.Sprintf("%s (%s), lines %d-%d:\n", sp.Name, sp.Kind, sp.Start, sp.End)
	}

	f, err := os.Open(t.abs)
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

	// The whole file is hashed as it streams, so the hash covers lines that
	// are not shown too.
	fileSum := sha256.New()
	br := bufio.NewReaderSize(io.TeeReader(f, fileSum), 64*1024)
	offset, limit := sel.offset, sel.limit
	var sb strings.Builder
	var shown []string
	total, emitted, lastEmitted, bytesOut := 0, 0, 0, 0
	capped := false
	for {
		line, rerr := br.ReadString('\n')
		if line != "" {
			total++
			if total >= offset && !capped {
				text := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
				entry := fmt.Sprintf("%6d  %s\n", total, clipLine(text, maxLineChars))
				if emitted >= limit || bytesOut+len(entry) > maxReadBytes {
					capped = true
				} else {
					sb.WriteString(entry)
					bytesOut += len(entry)
					emitted++
					lastEmitted = total
					if sel.ranged {
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
	if offset > total {
		return readOut{}, fmt.Errorf("offset %d is past the end of %s (%d lines).", offset, t.rel, total)
	}
	fileHash := hex.EncodeToString(fileSum.Sum(nil))[:12]
	if lastEmitted > 0 && st != nil && st.note(readKey(env, t.abs), fileHash, offset, lastEmitted, sel.force) {
		return readOut{
			body: fmt.Sprintf("%s unchanged since your last read (hash %s, %d lines). Lines %d-%d are already in your context; pass force=true to get them again.",
				t.rel, fileHash, total, offset, lastEmitted),
			summary: "unchanged",
		}, nil
	}

	body := strings.TrimSuffix(sb.String(), "\n")
	summary := fmt.Sprintf("%d lines", total)
	if sel.ranged {
		// A range is cut short only when the requested end was not reached.
		// Lines past it belong to something else, so they are not offered.
		target := total
		if sel.end > 0 && sel.end < total {
			target = sel.end
		}
		if lastEmitted < target {
			body += fmt.Sprintf("\n… lines %d-%d of this read were not shown; call read with lines=%d-%d to continue.",
				lastEmitted+1, target, lastEmitted+1, target)
		}
		body = header + body + "\nhash:" + shortHash(strings.Join(shown, "\n"))
		summary = fmt.Sprintf("lines %d-%d", offset, lastEmitted)
		if sel.symbol != "" {
			summary = strings.TrimSuffix(header, ":\n")
		}
		return readOut{body: body, summary: summary}, nil
	}
	if lastEmitted < total {
		body += fmt.Sprintf("\n… %d more lines; call read with offset=%d to continue.", total-lastEmitted, lastEmitted+1)
		summary = fmt.Sprintf("%d of %d lines", emitted, total)
	}
	return readOut{body: body, summary: summary}, nil
}
