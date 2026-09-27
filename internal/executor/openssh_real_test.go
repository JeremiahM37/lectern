package executor

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Opt in with LECTERN_REAL_SSH=user@host (a machine this user can reach with
// a key): the OpenSSH transport runs, writes, streams and reconnects for real.
func TestOpenSSHAgainstARealHost(t *testing.T) {
	dest := os.Getenv("LECTERN_REAL_SSH")
	if dest == "" {
		t.Skip("set LECTERN_REAL_SSH=user@host")
	}
	user, host, _ := strings.Cut(dest, "@")
	o := NewOpenSSH(host, user, 22, "", "", SSHOptions{})
	defer o.Close()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		r, err := o.Run(ctx, "echo $USER@$(hostname)", RunOpts{Timeout: 20})
		if err != nil || !strings.HasPrefix(r.Stdout, user+"@") {
			t.Fatalf("run %d: %+v %v", i, r, err)
		}
	}
	path := "/tmp/lectern-openssh-real-test.txt"
	if err := o.WriteFile(ctx, path, []byte("real bytes\n")); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if r, err := o.Stream(ctx, "cat "+path+"; rm -f "+path, &sb, 20); err != nil || !r.OK() || sb.String() != "real bytes\n" {
		t.Fatalf("stream %q %+v %v", sb.String(), r, err)
	}
	o.Close() // ends the master; the next command must dial a new one
	if r, err := o.Run(ctx, "true", RunOpts{Timeout: 20}); err != nil || !r.OK() {
		t.Fatalf("after the master closed: %+v %v", r, err)
	}
	t.Logf("status: %+v", o.ConnStatus())
}
