package helpers

// mcp-install is internal/agents' interactiveMCPInstallScript: it creates
// one private MCP runtime file under the target user's state directory and
// prints its absolute path. Every step below the state root works through
// directory descriptors that refuse symlinks, and the file is published
// with link(2), which never replaces an existing name.
//
//	lectern helper mcp-install -- lectern/mcp/NAME/mcp.json BASE64

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

func init() { Register("mcp-install", mcpInstall) }

// pyUncaught reports an exception the Python script did not catch: its
// traceback went to stderr and the exit status was 1.
func pyUncaught(stderr io.Writer, kind string, err error) int {
	fmt.Fprintf(stderr, "Traceback (most recent call last):\n  File \"<lectern helper>\"\n%s: %s\n", kind, err)
	return 1
}

// pyErrKind names the exception class Python would have raised for err.
func pyErrKind(err error) string {
	if errno, ok := pyErrno(err); ok {
		switch errno {
		case 2:
			return "FileNotFoundError"
		case 17:
			return "FileExistsError"
		case 13, 1:
			return "PermissionError"
		case 20:
			return "NotADirectoryError"
		case 21:
			return "IsADirectoryError"
		}
		return "OSError"
	}
	return "OSError"
}

var base64Strict = regexp.MustCompile(`^[A-Za-z0-9+/]*={0,2}$`)

// pyB64decode is base64.b64decode(s, validate=True).
func pyB64decode(s string) ([]byte, error) {
	if !base64Strict.MatchString(s) {
		return nil, errors.New("Non-base64 digit found")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("Incorrect padding")
	}
	return data, nil
}

func mcpInstall(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 3 {
		return pyUncaught(stderr, "ValueError", fmt.Errorf("expected 2 values to unpack (got %d)", max(0, len(args)-1)))
	}
	rel, encoded := args[1], args[2]
	parts := strings.Split(rel, "/")
	bad := len(parts) != 4 || parts[0] != "lectern" || parts[1] != "mcp" || parts[3] != "mcp.json" || parts[2] == ""
	for _, p := range parts {
		bad = bad || p == "" || p == "." || p == ".."
	}
	if bad {
		return pyUncaught(stderr, "RuntimeError", errors.New("invalid interactive MCP path"))
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return pyUncaught(stderr, "RuntimeError", errors.New("target HOME is unavailable"))
		}
		state = home + "/.local/state"
	}
	if !strings.HasPrefix(state, "/") {
		return pyUncaught(stderr, "RuntimeError", errors.New("target XDG_STATE_HOME must be absolute"))
	}
	kind, err := mcpInstallAt(state, parts, encoded)
	if err != nil {
		if kind == "" {
			kind = pyErrKind(err)
		}
		return pyUncaught(stderr, kind, err)
	}
	fmt.Fprintln(stdout, state+"/"+strings.Join(parts, "/"))
	return 0
}

// openOrMakeDir is mkdir-if-missing then open, both relative to parent.
func openOrMakeDir(parent *dirFD, name string, mode uint32) (*dirFD, error) {
	if err := parent.mkdir(name, mode); err != nil && pyErrKind(err) != "FileExistsError" {
		return nil, err
	}
	return parent.openDir(name)
}

// mcpInstallAt does the work; kind names a RuntimeError the script raised.
func mcpInstallAt(state string, parts []string, encoded string) (kind string, err error) {
	root, err := openDirPath("/")
	if err != nil {
		return "", err
	}
	for _, part := range strings.Split(state, "/")[1:] {
		if part == "" || part == "." || part == ".." {
			root.close()
			return "RuntimeError", errors.New("invalid state path")
		}
		next, err := openOrMakeDir(root, part, 0o700)
		root.close()
		if err != nil {
			return "", err
		}
		root = next
	}
	defer root.close()
	lec, err := openOrMakeDir(root, "lectern", 0o700)
	if err != nil {
		return "", err
	}
	defer lec.close()
	interactive, err := openOrMakeDir(lec, "mcp", 0o700)
	if err != nil {
		return "", err
	}
	defer interactive.close()
	if err := interactive.mkdir(parts[2], 0o700); err != nil {
		if pyErrKind(err) == "FileExistsError" {
			return "RuntimeError", errors.New("interactive MCP runtime already exists")
		}
		return "", err
	}
	// Compare the opened leaf with the directory just made: a concurrent
	// replacement before this point fails, and after it the descriptor holds.
	expected, err := interactive.lstat(parts[2])
	if err != nil {
		return "", err
	}
	leaf, err := interactive.openDir(parts[2])
	if err != nil {
		return "", err
	}
	defer leaf.close()
	actual, err := leaf.stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(expected, actual) || !actual.IsDir() {
		return "RuntimeError", errors.New("interactive MCP runtime was replaced")
	}
	tmp, err := leaf.open(".mcp.tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600, true)
	if err != nil {
		return "", err
	}
	payload, err := pyB64decode(encoded)
	if err != nil {
		tmp.Close()
		return "binascii.Error", err
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return "", pyErr(err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", pyErr(err)
	}
	if err := tmp.Close(); err != nil {
		return "", pyErr(err)
	}
	if err := leaf.link(".mcp.tmp", "mcp.json"); err != nil {
		return "", err
	}
	return "", leaf.unlink(".mcp.tmp")
}
