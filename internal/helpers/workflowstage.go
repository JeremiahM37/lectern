package helpers

// workflow-stage is internal/workflows' stageScript: it writes one file of a
// pinned workflow or skill version under root on the target, verifying (never
// replacing) a file already there. Directories are walked with descriptors
// that refuse symlinks, and the file is published with link(2).
//
//	lectern helper workflow-stage '{"root":…,"parts":[…],"data":BASE64,"mode":420}'

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

func init() { Register("workflow-stage", workflowStage) }

func workflowStage(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		return pyUncaught(stderr, "IndexError", errors.New("list index out of range"))
	}
	raw, err := pyLoads(args[0])
	if err != nil {
		return pyUncaught(stderr, "json.decoder.JSONDecodeError", err)
	}
	out, err := stageMain(raw)
	if err != nil {
		return pyUncaught(stderr, pyErrKind(err), err)
	}
	fmt.Fprintln(stdout, pyDumpsCompact(out))
	return 0
}

// wPyInt is int(v) for a JSON value.
func wPyInt(v any) (int64, error) {
	if n, ok := pyIntValue(v); ok {
		return n, nil
	}
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, fmt.Errorf("cannot convert float %s to integer", wPyFloatRepr(x))
		}
		return int64(x), nil
	case string:
		n, err := strconv.ParseInt(strings.ReplaceAll(wPyStrip(x), "_", ""), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid literal for int() with base 10: %s", pyReprString(x))
		}
		return n, nil
	}
	return 0, fmt.Errorf("int() argument must be a string, a bytes-like object or a real number, not '%s'", pyTypeName(v))
}

// ensureDirAt opens path from /, component by component, creating missing
// directories (0755) and refusing symlinks.
func ensureDirAt(path string) (*dirFD, error) {
	path = pyAbspath(path)
	fd, err := openDirPath("/")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		if part == "" {
			continue
		}
		next, err := fd.openDir(part)
		if err != nil && pyErrKind(err) == "FileNotFoundError" {
			if merr := fd.mkdir(part, 0o755); merr != nil && pyErrKind(merr) != "FileExistsError" {
				fd.close()
				return nil, merr
			}
			next, err = fd.openDir(part)
		}
		fd.close()
		if err != nil {
			return nil, err
		}
		fd = next
	}
	return fd, nil
}

func stageMain(raw any) (*pyObj, error) {
	rootVal, err := pyIndex(raw, "root")
	if err != nil {
		return nil, err
	}
	root, ok := rootVal.(string)
	if !ok {
		return nil, fmt.Errorf("expected str, bytes or os.PathLike object, not %s", pyTypeName(rootVal))
	}
	root = pyAbspath(root)
	a := raw.(*pyObj)
	partsVal, err := pyIndex(a, "parts")
	if err != nil {
		return nil, err
	}
	dataVal, err := pyIndex(a, "data")
	if err != nil {
		return nil, err
	}
	encoded, ok := dataVal.(string)
	if !ok {
		return nil, fmt.Errorf("argument should be a bytes-like object or ASCII string, not '%s'", pyTypeName(dataVal))
	}
	data, err := pyB64decode(encoded)
	if err != nil {
		return nil, err
	}
	partsAny, err := pyIter(partsVal)
	if err != nil {
		return nil, err
	}
	var parts []string
	unsafe := len(partsAny) == 0
	for _, p := range partsAny {
		s, ok := p.(string)
		if !ok || s == "" || s == "." || s == ".." || strings.ContainsAny(s, `/\`) {
			unsafe = true
			break
		}
		parts = append(parts, s)
	}
	if unsafe {
		return nil, errors.New("unsafe bundled path")
	}
	parent, err := ensureDirAt(wPyJoin(root, parts[:len(parts)-1]...))
	if err != nil {
		return nil, err
	}
	defer parent.close()
	name := parts[len(parts)-1]
	st, err := parent.lstat(name)
	if err != nil && pyErrKind(err) != "FileNotFoundError" {
		return nil, err
	}
	mode := int64(0o644)
	if v, ok := a.Get("mode"); ok {
		if mode, err = wPyInt(v); err != nil {
			return nil, err
		}
	}
	// sameExisting verifies a file already at name holds exactly data and mode.
	sameExisting := func() error {
		current, err := parent.lstat(name)
		if err != nil {
			if pyErrKind(err) == "FileNotFoundError" {
				return nil
			}
			return err
		}
		if !current.Mode().IsRegular() {
			return errors.New("immutable source path is not a regular file")
		}
		f, err := parent.open(name, os.O_RDONLY, 0, true)
		if err != nil {
			return err
		}
		old := make([]byte, current.Size()+1)
		n, rerr := f.Read(old)
		f.Close()
		if rerr != nil && rerr != io.EOF {
			return wPyErr(rerr)
		}
		if !bytes.Equal(old[:n], data) || int64(pyImode(current.Mode())) != mode {
			return errors.New("immutable workflow source differs from pinned content")
		}
		return nil
	}
	if st != nil {
		if err := sameExisting(); err != nil {
			return nil, err
		}
		return newObj("existing", true), nil
	}
	tmp := ".lectern-stage-" + tempName()
	f, err := parent.open(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, uint32(mode), true)
	if err != nil {
		return nil, err
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if werr == nil {
		// The open honoured the umask; verification uses the pinned mode.
		werr = f.Chmod(pyFileMode(mode))
	}
	f.Close()
	if werr != nil {
		return nil, wPyErr(werr)
	}
	// link(2) publishes without clobbering: rename would replace a foreign
	// file created after the lstat above.
	if err := parent.link(tmp, name); err != nil {
		if pyErrKind(err) != "FileExistsError" {
			return nil, err
		}
		if err := parent.unlink(tmp); err != nil {
			return nil, err
		}
		if err := sameExisting(); err != nil {
			return nil, err
		}
		return newObj("existing", true), nil
	}
	if err := parent.unlink(tmp); err != nil {
		return nil, err
	}
	return newObj("created", true), nil
}

// pyImode is stat.S_IMODE: permission, setuid, setgid and sticky bits.
func pyImode(m os.FileMode) uint32 {
	out := uint32(m.Perm())
	if m&os.ModeSetuid != 0 {
		out |= 0o4000
	}
	if m&os.ModeSetgid != 0 {
		out |= 0o2000
	}
	if m&os.ModeSticky != 0 {
		out |= 0o1000
	}
	return out
}

// pyFileMode turns a numeric mode into Go's FileMode for chmod.
func pyFileMode(mode int64) os.FileMode {
	m := os.FileMode(mode & 0o777)
	if mode&0o4000 != 0 {
		m |= os.ModeSetuid
	}
	if mode&0o2000 != 0 {
		m |= os.ModeSetgid
	}
	if mode&0o1000 != 0 {
		m |= os.ModeSticky
	}
	return m
}

// tempName is next(tempfile._get_candidate_names()): eight characters.
func tempName() string {
	var raw [8]byte
	rand.Read(raw[:])
	name := make([]byte, 8)
	for i, b := range raw {
		name[i] = tempChars[int(b)%len(tempChars)]
	}
	return string(name)
}
