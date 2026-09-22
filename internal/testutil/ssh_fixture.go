package testutil

// This is a loopback SSH target for real executor tests.  It keeps the tests
// independent of the host sshd and of the operator's private keys: the key and
// listener are generated for one test and the server executes commands in the
// same isolated namespace as its caller.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

type SSHFixture struct {
	Port     int
	KeyPath  string
	listener net.Listener
	done     chan struct{}
}

// NewSSHFixture starts a key-authenticated shell server on an ephemeral
// loopback port.  It is deliberately scoped to the test process and has no
// access to host network namespaces when invoked by the reviewed runner.
func NewSSHFixture(t *testing.T) *SSHFixture {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.MarshalPrivateKey(private, "lectern-test")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(key), 0600); err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	host := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	hostSigner, err := ssh.NewSignerFromKey(host)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, got ssh.PublicKey) (*ssh.Permissions, error) {
		if ssh.FingerprintSHA256(got) != ssh.FingerprintSHA256(public) {
			return nil, fmt.Errorf("unauthorized Lectern SSH fixture key")
		}
		return nil, nil
	}}
	config.AddHostKey(hostSigner)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &SSHFixture{Port: listener.Addr().(*net.TCPAddr).Port, KeyPath: keyPath, listener: listener, done: make(chan struct{})}
	var workers sync.WaitGroup
	var connectionsMu sync.Mutex
	connections := make(map[net.Conn]struct{})
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			raw, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			connectionsMu.Lock()
			connections[raw] = struct{}{}
			connectionsMu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() {
					_ = raw.Close()
					connectionsMu.Lock()
					delete(connections, raw)
					connectionsMu.Unlock()
				}()
				conn, chans, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range chans {
					channel, reqs, err := incoming.Accept()
					if err != nil {
						continue
					}
					workers.Add(1)
					go func() {
						defer workers.Done()
						defer channel.Close()
						for req := range reqs {
							if req.Type != "exec" {
								_ = req.Reply(false, nil)
								continue
							}
							var payload struct{ Command string }
							if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
								_ = req.Reply(false, nil)
								return
							}
							_ = req.Reply(true, nil)
							cmd := exec.Command("bash", "-c", payload.Command)
							cmd.Stdin, cmd.Stdout, cmd.Stderr = channel, channel, channel.Stderr()
							rc := uint32(0)
							if err := cmd.Run(); err != nil {
								rc = 1
								if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() >= 0 {
									rc = uint32(exit.ExitCode())
								}
							}
							_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ RC uint32 }{rc}))
							return
						}
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		connectionsMu.Lock()
		active := make([]net.Conn, 0, len(connections))
		for raw := range connections {
			active = append(active, raw)
		}
		connectionsMu.Unlock()
		for _, raw := range active {
			_ = raw.Close()
		}
		workers.Wait()
		close(f.done)
	})
	return f
}
