package executor

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSSH is an ssh(1) stand-in: it records its arguments, then runs the
// last argument locally, the way sshd would run the command. -W and -O are
// answered like ssh does.
func fakeSSH(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	script := `#!/bin/bash
printf '%s\n' "$*" >> ` + log + `
for a in "$@"; do
  if [ "$prev" = "-W" ]; then exec cat; fi
  if [ "$prev" = "-O" ]; then exit 0; fi
  prev=$a
done
cmd="${@: -1}"
case "$cmd" in unreachable*) echo "ssh: connect to host box port 22: Connection refused" >&2; exit 255;; esac
# A multiplexed client that lost the exit message: the command ran, ssh says 255.
case "$cmd" in lostexit*) bash -c "$cmd"; exit 255;; esac
exec bash -c "$cmd"
`
	bin := filepath.Join(dir, "ssh")
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestOpenSSHTransportUsesAliasOptionsAndOneMaster(t *testing.T) {
	bin, log := fakeSSH(t)
	o := NewOpenSSH("10.0.0.5", "dev", 22, "", "", SSHOptions{Alias: "build-box", ProxyJump: "bastion",
		ForwardAgent: true, Options: []string{"GSSAPIAuthentication=yes"}})
	o.Binary = bin
	defer o.Close()
	r, err := o.Run(context.Background(), "echo hello $((1+1))", RunOpts{Cwd: "/tmp", Timeout: 10})
	if err != nil || r.Stdout != "hello 2\n" {
		t.Fatalf("run: %+v %v", r, err)
	}
	args, _ := os.ReadFile(log)
	line := string(args)
	for _, want := range []string{"-o BatchMode=yes", "-o ControlMaster=auto", "ControlPersist=10m", "-J bastion", "-A",
		"-o GSSAPIAuthentication=yes", "-l dev", "-- build-box cd /tmp && echo hello"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in %s", want, line)
		}
	}
	if strings.Contains(line, "-p 22") || strings.Contains(line, "10.0.0.5") {
		t.Errorf("an alias must decide the host and default port: %s", line)
	}
	if st := o.ConnStatus(); st.State != "connected" || st.Transport != "openssh" {
		t.Fatalf("status %+v", st)
	}
}

func TestOpenSSHWritesReadsStreamsAndDials(t *testing.T) {
	bin, _ := fakeSSH(t)
	o := NewOpenSSH("box", "u", 2222, "", "", SSHOptions{})
	o.Binary = bin
	file := filepath.Join(t.TempDir(), "sub", "f.txt")
	if err := o.WriteFile(context.Background(), file, []byte("line one\nline two\n")); err != nil {
		t.Fatal(err)
	}
	got, err := o.ReadFile(context.Background(), file, 9)
	if err != nil || string(got) != "line two\n" {
		t.Fatalf("read %q %v", got, err)
	}
	var sb strings.Builder
	if r, err := o.Stream(context.Background(), "cat "+ShellQuote(file), &sb, 10); err != nil || !r.OK() || sb.String() != "line one\nline two\n" {
		t.Fatalf("stream %q %+v %v", sb.String(), r, err)
	}
	conn, err := o.DialTarget(context.Background(), "127.0.0.1:5173")
	if err != nil {
		t.Fatal(err)
	}
	conn.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("-W stream: %q %v", buf, err)
	}
	conn.Close()
	if _, err := o.DialTarget(context.Background(), "10.1.1.1:80"); err == nil {
		t.Fatal("forwarding must stay on the target's loopback")
	}
}

func TestOpenSSHConnectionFailureIsAnError(t *testing.T) {
	bin, _ := fakeSSH(t)
	o := NewOpenSSH("box", "u", 22, "", "", SSHOptions{})
	o.Binary = bin
	if _, err := o.Run(context.Background(), "unreachable", RunOpts{Timeout: 5}); err == nil || !strings.Contains(err.Error(), "Connection refused") {
		t.Fatalf("got %v", err)
	}
	if st := o.ConnStatus(); st.State != "down" {
		t.Fatalf("status %+v", st)
	}
	// The command's own status wins over a lost exit message.
	if r, err := o.Run(context.Background(), "lostexit=1; echo done", RunOpts{Timeout: 5}); err != nil || r.RC != 0 || r.Stdout != "done\n" || r.Stderr != "" {
		t.Fatalf("lost exit: %+v %v", r, err)
	}
	// A remote command that exits 255 on its own is just a result.
	r, err := o.Run(context.Background(), "exit 255", RunOpts{Timeout: 5})
	if err != nil || r.RC != 255 {
		t.Fatalf("%+v %v", r, err)
	}
}
