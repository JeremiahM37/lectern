package webterm

// Conn is one terminal opened for an attachment's command, the same way the
// browser terminal opens it: a `lectern pty …` attachment straight from the
// PTY host, anything else on a pseudo-terminal of its own. Lectern's own
// native attach client (cmd/lectern native_bare.go) draws it.
type Conn interface {
	// Read returns output; io.EOF once the session or program has ended.
	Read() ([]byte, error)
	Write(b []byte) error
	Resize(cols, rows int) error
	// Close leaves: a PTY-host attachment detaches, a program is hung up.
	Close()
}

// Open starts argv at the given size. self is this lectern binary.
func Open(argv []string, self string, cols, rows int) (Conn, error) {
	t, err := open(Options{Argv: argv, Self: self}, cols, rows)
	if err != nil {
		return nil, err
	}
	return conn{t}, nil
}

type conn struct{ t terminal }

func (c conn) Read() ([]byte, error)       { return c.t.read() }
func (c conn) Write(b []byte) error        { return c.t.write(b) }
func (c conn) Resize(cols, rows int) error { return c.t.resize(cols, rows) }
func (c conn) Close()                      { c.t.close() }
