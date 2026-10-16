package nvim

import (
	"bytes"
	"io"
)

// bytesReader is a reader over b, for Unpack.
func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
