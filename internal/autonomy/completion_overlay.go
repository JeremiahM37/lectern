package autonomy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

const CompletionSupplementDir = ".lectern-completion"
const CompletionOriginalWorkshop = CompletionSupplementDir + "/original-WORKSHOP.md"
const CompletionOverlayPolicy = "documentary-v1"
const completionMaxDocument = 1 << 20
const completionMaxDocuments = 64
const completionMaxTotal = 8 << 20

type CompletionOverlayReceipt struct {
	Policy         string   `json:"policy"`
	BaselineSHA256 string   `json:"baseline_sha256"`
	OverlaySHA256  string   `json:"overlay_sha256"`
	DerivedSHA256  string   `json:"derived_sha256"`
	Documents      []string `json:"documents"`
}

type completionEntry struct {
	Mode fs.FileMode
	Size int64
	Hash string
}

// ReconstructCompletionOverlay validates an exact candidate tree, then copies the
// trusted baseline and only validated documentary bytes into a new output tree.
// Inputs must be frozen controller-owned snapshots; output's parent must likewise
// be controller-owned. It never executes candidate content. Reviewers must not
// execute fenced examples or use documentation-driven test discovery. Only flat
// .md/.txt supplements are allowed (no scripts, configs, dependencies or fixtures).
// Output root is always private 0700 and is excluded from tree receipts.
// Generated descendants have exact policy modes independent of process umask.
// A changed-back production file cannot affect output: production is read only
// from baseline and checked against its initial digest while copying.
func ReconstructCompletionOverlay(baselineDir, candidateDir, outputDir string) (receipt CompletionOverlayReceipt, err error) {
	receipt.Policy = CompletionOverlayPolicy
	dirs := []*string{&baselineDir, &candidateDir, &outputDir}
	for _, d := range dirs {
		*d, err = filepath.Abs(*d)
		if err != nil {
			return receipt, err
		}
		if err = completionNoLinkedAncestors(*d); err != nil {
			return receipt, err
		}
	}
	for i := range dirs {
		for j := i + 1; j < len(dirs); j++ {
			if completionContains(*dirs[i], *dirs[j]) || completionContains(*dirs[j], *dirs[i]) {
				return receipt, fmt.Errorf("completion trees must be disjoint")
			}
		}
	}
	baseline, err := os.OpenRoot(baselineDir)
	if err != nil {
		return receipt, err
	}
	defer baseline.Close()
	candidate, err := os.OpenRoot(candidateDir)
	if err != nil {
		return receipt, err
	}
	defer candidate.Close()
	baselineInfo, err := baseline.Stat(".")
	if err != nil {
		return receipt, err
	}
	candidateInfo, err := candidate.Stat(".")
	if err != nil {
		return receipt, err
	}
	if baselineInfo.Mode() != candidateInfo.Mode() {
		return receipt, fmt.Errorf("inherited root mode changed")
	}
	base, err := completionScan(baseline)
	if err != nil {
		return receipt, fmt.Errorf("baseline: %w", err)
	}
	cand, err := completionScan(candidate)
	if err != nil {
		return receipt, fmt.Errorf("candidate: %w", err)
	}
	if _, ok := base[CompletionSupplementDir]; ok {
		return receipt, fmt.Errorf("baseline already has supplement directory")
	}
	original, ok := base["WORKSHOP.md"]
	if !ok || !original.Mode.IsRegular() {
		return receipt, fmt.Errorf("baseline requires regular WORKSHOP.md")
	}
	documents := map[string][]byte{}
	total := 0
	addDocument := func(name string, entry completionEntry) error {
		if len(documents) >= completionMaxDocuments || entry.Size > int64(completionMaxTotal-total) {
			return fmt.Errorf("documentary overlay exceeds limits")
		}
		data, e := completionDocument(candidate, name, entry)
		if e != nil {
			return e
		}
		documents[name] = data
		total += len(data)
		return nil
	}
	for name, entry := range base {
		other, exists := cand[name]
		if !exists {
			return receipt, fmt.Errorf("inherited path deleted: %s", name)
		}
		if name == "WORKSHOP.md" && entry.Mode == other.Mode && entry.Size != -1 {
			if entry.Hash != other.Hash {
				err = addDocument(name, other)
				if err != nil {
					return receipt, err
				}
			}
			continue
		}
		if entry != other {
			return receipt, fmt.Errorf("inherited path changed: %s", name)
		}
	}
	for name, entry := range cand {
		if _, exists := base[name]; exists {
			continue
		}
		if name == CompletionSupplementDir && entry.Mode == fs.ModeDir|0755 {
			continue
		}
		if path.Dir(name) != CompletionSupplementDir || name == CompletionOriginalWorkshop || !entry.Mode.IsRegular() || entry.Mode.Perm() != 0644 || (path.Ext(name) != ".md" && path.Ext(name) != ".txt") {
			return receipt, fmt.Errorf("forbidden supplementary path or mode: %s", name)
		}
		stem := strings.TrimSuffix(path.Base(name), path.Ext(name))
		if stem == "" || strings.ContainsAny(stem, ".\\\n\r") {
			return receipt, fmt.Errorf("invalid document name: %s", name)
		}
		err = addDocument(name, entry)
		if err != nil {
			return receipt, err
		}
	}
	if len(documents) == 0 || len(documents) > completionMaxDocuments || total > completionMaxTotal {
		return receipt, fmt.Errorf("documentary overlay empty or exceeds limits")
	}
	receipt.BaselineSHA256 = completionTreeHash(base)
	if err = os.Mkdir(outputDir, 0700); err != nil {
		return receipt, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(outputDir)
		}
	}()
	// Output is deliberately created before copying, never merged with existing data.
	output, err := os.OpenRoot(outputDir)
	if err != nil {
		return receipt, err
	}
	defer output.Close()
	if err = output.Chmod(".", 0700); err != nil {
		return receipt, err
	}
	names := completionNames(base)
	for _, name := range names {
		entry := base[name]
		if entry.Mode.IsDir() {
			if err = output.Mkdir(name, 0755); err != nil {
				return receipt, err
			}
			continue
		}
		if err = completionCopy(baseline, output, name, entry); err != nil {
			return receipt, err
		}
	}
	if err = output.Mkdir(CompletionSupplementDir, 0755); err != nil {
		return receipt, err
	}
	if err = output.Chmod(CompletionSupplementDir, 0755); err != nil {
		return receipt, err
	}
	originalBytes, err := completionRead(baseline, "WORKSHOP.md", original, completionMaxDocument)
	if err != nil {
		return receipt, err
	}
	if err = output.WriteFile(CompletionOriginalWorkshop, originalBytes, 0444); err != nil {
		return receipt, err
	}
	if err = output.Chmod(CompletionOriginalWorkshop, 0444); err != nil {
		return receipt, err
	}
	for name, data := range documents {
		if name == "WORKSHOP.md" {
			if err = output.Chmod(name, 0600); err != nil {
				return receipt, err
			}
		}
		if err = output.WriteFile(name, data, 0644); err != nil {
			return receipt, err
		}
		if err = output.Chmod(name, 0644); err != nil {
			return receipt, err
		}
	}
	if err = output.Chmod("WORKSHOP.md", original.Mode.Perm()); err != nil {
		return receipt, err
	}
	// Restore original directory permissions only after populating their contents.
	for i := len(names) - 1; i >= 0; i-- {
		name := names[i]
		if base[name].Mode.IsDir() {
			if err = output.Chmod(name, base[name].Mode.Perm()); err != nil {
				return receipt, err
			}
		}
	}
	derived, err := completionScan(output)
	if err != nil {
		return receipt, err
	}
	overlay := map[string]completionEntry{}
	for name := range documents {
		overlay[name] = derived[name]
		receipt.Documents = append(receipt.Documents, name)
	}
	sort.Strings(receipt.Documents)
	overlay[CompletionOriginalWorkshop] = derived[CompletionOriginalWorkshop]
	receipt.OverlaySHA256 = completionTreeHash(overlay)
	receipt.DerivedSHA256 = completionTreeHash(derived)
	return receipt, nil
}

func completionContains(a, b string) bool {
	rel, err := filepath.Rel(a, b)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}
func completionNoLinkedAncestors(p string) error {
	for {
		info, err := os.Lstat(p)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("linked path: %s", p)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
		p = parent
	}
}
func completionLinks(info fs.FileInfo) bool {
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.IsValid() && v.Kind() == reflect.Struct {
		n := v.FieldByName("Nlink")
		if n.IsValid() && n.CanUint() {
			return n.Uint() > 1
		}
	}
	// An unsupported filesystem/platform cannot prove the absence of hardlinks.
	// Fail closed rather than silently weakening the documentary boundary.
	return true
}
func completionOpen(root *os.Root, name string) (*os.File, fs.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || completionLinks(info) {
		return nil, nil, fmt.Errorf("non-regular or hardlinked file: %s", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) || !actual.Mode().IsRegular() || completionLinks(actual) {
		f.Close()
		return nil, nil, fmt.Errorf("file changed while opening: %s", name)
	}
	return f, actual, nil
}
func completionScan(root *os.Root) (map[string]completionEntry, error) {
	entries := map[string]completionEntry{}
	err := fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if !fs.ValidPath(name) || strings.Contains(name, "\\") {
			return fmt.Errorf("invalid path: %s", name)
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		mode := info.Mode()
		if mode&^(fs.ModeDir|0777) != 0 {
			return fmt.Errorf("special file or mode: %s", name)
		}
		if info.IsDir() {
			entries[name] = completionEntry{Mode: mode, Size: -1}
			return nil
		}
		f, actual, err := completionOpen(root, name)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		if n != actual.Size() {
			return fmt.Errorf("file size changed: %s", name)
		}
		entries[name] = completionEntry{Mode: actual.Mode(), Size: n, Hash: hex.EncodeToString(h.Sum(nil))}
		return nil
	})
	return entries, err
}
func completionRead(root *os.Root, name string, entry completionEntry, limit int64) ([]byte, error) {
	if entry.Size > limit {
		return nil, fmt.Errorf("document too large: %s", name)
	}
	f, info, err := completionOpen(root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != entry.Size || info.Mode() != entry.Mode || hex.EncodeToString(sum[:]) != entry.Hash {
		return nil, fmt.Errorf("file changed: %s", name)
	}
	return data, nil
}
func completionDocument(root *os.Root, name string, entry completionEntry) ([]byte, error) {
	if entry.Mode.Perm()&0111 != 0 {
		return nil, fmt.Errorf("executable document: %s", name)
	}
	data, err := completionRead(root, name, entry, completionMaxDocument)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return nil, fmt.Errorf("document must be UTF-8 text: %s", name)
	}
	return data, nil
}
func completionCopy(source, dest *os.Root, name string, entry completionEntry) error {
	in, info, err := completionOpen(source, name)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := dest.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n != entry.Size || info.Mode() != entry.Mode || hex.EncodeToString(h.Sum(nil)) != entry.Hash {
		return fmt.Errorf("baseline changed while copying: %s", name)
	}
	return dest.Chmod(name, entry.Mode.Perm())
}
func completionNames(entries map[string]completionEntry) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func completionTreeHash(entries map[string]completionEntry) string {
	h := sha256.New()
	for _, name := range completionNames(entries) {
		e := entries[name]
		fmt.Fprintf(h, "%d:%s:%o:%d:%s\n", len(name), name, e.Mode, e.Size, e.Hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}
