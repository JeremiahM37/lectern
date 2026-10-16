package fs

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const (
	maxReadLines = 2000
	maxReadBytes = 250 * 1024
	maxLineChars = 2000
	sniffBytes   = 8000
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

// NewRead returns the read tool.
func NewRead() tools.Tool {
	return &tool{
		spec: newSpec("read",
			"Read a text file with line numbers. Use offset (1-based line) and limit to page through big files. "+
				"Pass paths to read several files in one call. A directory returns a listing. "+
				"Read a file before editing it.",
			tools.Schema(map[string]any{
				"path":   tools.Str("File to read, relative to the project root."),
				"paths":  tools.Arr("Several files to read in one call.", map[string]any{"type": "string"}),
				"offset": tools.Int("First line to show, 1-based. Default 1."),
				"limit":  tools.Int("Number of lines to show. Default and maximum 2000."),
			})),
		readOnly: true,
		run:      runRead,
	}
}

type readArgs struct {
	Path   string      `json:"path"`
	Paths  flexStrings `json:"paths"`
	Offset flexInt     `json:"offset"`
	Limit  flexInt     `json:"limit"`
}

type readOut struct {
	body    string
	summary string
	image   string
}

func runRead(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var a readArgs
	if err := tools.Decode(args, &a); err != nil {
		return fail(err)
	}
	var paths []string
	if a.Path != "" {
		paths = append(paths, a.Path)
	}
	paths = append(paths, a.Paths...)
	if len(paths) == 0 {
		return tools.Errorf("read needs path (or paths for several files).")
	}
	offset := int(a.Offset)
	if offset < 1 {
		offset = 1
	}
	limit := int(a.Limit)
	if limit < 1 || limit > maxReadLines {
		limit = maxReadLines
	}

	if len(paths) == 1 {
		out, err := readOne(ctx, env, paths[0], offset, limit)
		if err != nil {
			return fail(err)
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
		out, err := readOne(ctx, env, p, offset, limit)
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

// readOne reads one path. It marks the file read on success.
func readOne(ctx context.Context, env *tools.Env, p string, offset, limit int) (readOut, error) {
	t, err := resolve(ctx, env, "read", p, accessRead)
	if err != nil {
		return readOut{}, err
	}
	fi, err := statPath(t)
	if err != nil {
		return readOut{}, err
	}
	if fi.IsDir() {
		body, n, capped := renderTree(ctx, env.Workdir, t.abs, 1)
		if capped {
			body += fmt.Sprintf("\n(Listing capped at %d entries. Use list_dir with a deeper path.)", maxListEntries)
		}
		return readOut{body: body, summary: fmt.Sprintf("%d entries", n)}, nil
	}
	ext := strings.ToLower(filepath.Ext(t.abs))
	if imageExt[ext] {
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

	br := bufio.NewReaderSize(f, 64*1024)
	var sb strings.Builder
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
	body := strings.TrimSuffix(sb.String(), "\n")
	if lastEmitted < total {
		body += fmt.Sprintf("\n… %d more lines; call read with offset=%d to continue.", total-lastEmitted, lastEmitted+1)
		return readOut{body: body, summary: fmt.Sprintf("%d of %d lines", emitted, total)}, nil
	}
	return readOut{body: body, summary: fmt.Sprintf("%d lines", total)}, nil
}
