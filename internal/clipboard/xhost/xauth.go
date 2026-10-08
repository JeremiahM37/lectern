package xhost

import (
	"crypto/rand"
	"encoding/binary"
	"os"
)

// writeXauthority writes an Xauthority file holding one MIT-MAGIC-COOKIE-1 for
// the local host's display number. Only a process that can read this file
// (mode 0600, in a 0700 directory) can talk to the clipboard's X server.
func writeXauthority(path, hostname, display string) error {
	cookie := make([]byte, 16)
	if _, err := rand.Read(cookie); err != nil {
		return err
	}
	var b []byte
	u16 := func(n int) { b = binary.BigEndian.AppendUint16(b, uint16(n)) }
	str := func(s []byte) { u16(len(s)); b = append(b, s...) }
	u16(256) // FamilyLocal
	str([]byte(hostname))
	str([]byte(display))
	str([]byte("MIT-MAGIC-COOKIE-1"))
	str(cookie)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
