package executor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// testServer is a minimal sshd: public-key auth for one key, exec answers
// with what it saw, direct-tcpip dials out (so it can be a jump host), and
// an agent-forwarding request lets exec list the forwarded agent's keys.
type testServer struct {
	addr     string
	mu       sync.Mutex
	logins   []string
	forwards int
}

func startTestServer(t *testing.T, allowed ssh.PublicKey) *testServer {
	t.Helper()
	_, hostKey, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(hostKey)
	srv := &testServer{}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(key.Marshal(), allowed.Marshal()) {
			srv.mu.Lock()
			srv.logins = append(srv.logins, meta.User())
			srv.mu.Unlock()
			return nil, nil
		}
		return nil, fmt.Errorf("unknown key")
	}}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.addr = ln.Addr().String()
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.serve(raw, cfg)
		}
	}()
	return srv
}

func (srv *testServer) serve(raw net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(raw, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		switch nc.ChannelType() {
		case "direct-tcpip":
			var p struct {
				Host  string
				Port  uint32
				OHost string
				OPort uint32
			}
			_ = ssh.Unmarshal(nc.ExtraData(), &p)
			out, err := net.Dial("tcp", net.JoinHostPort(p.Host, fmt.Sprint(p.Port)))
			if err != nil {
				nc.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			ch, creqs, err := nc.Accept()
			if err != nil {
				out.Close()
				continue
			}
			go ssh.DiscardRequests(creqs)
			go func() { io.Copy(ch, out); ch.CloseWrite() }()
			go func() { io.Copy(out, ch); out.Close() }()
		case "session":
			ch, creqs, err := nc.Accept()
			if err != nil {
				continue
			}
			go func() {
				forwarded := false
				for req := range creqs {
					switch req.Type {
					case "auth-agent-req@openssh.com":
						forwarded = true
						srv.mu.Lock()
						srv.forwards++
						srv.mu.Unlock()
						req.Reply(true, nil)
					case "exec":
						var cmd struct{ Command string }
						_ = ssh.Unmarshal(req.Payload, &cmd)
						req.Reply(true, nil)
						reply := "ran: " + cmd.Command
						if forwarded && cmd.Command == "agent-keys" {
							// Open the agent channel back to the client, as sshd does.
							if ac, areqs, err := conn.OpenChannel("auth-agent@openssh.com", nil); err == nil {
								go ssh.DiscardRequests(areqs)
								keys, err := agent.NewClient(ac).List()
								reply = fmt.Sprintf("agent keys: %d %v", len(keys), err)
								ac.Close()
							} else {
								reply = "agent channel refused: " + err.Error()
							}
						}
						ch.Write([]byte(reply))
						ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						ch.Close()
					default:
						req.Reply(false, nil)
					}
				}
			}()
		default:
			nc.Reject(ssh.UnknownChannelType, "no")
		}
	}
}

// agentWithKey serves an in-memory ssh-agent holding one key on a socket.
func agentWithKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "lagent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go agent.ServeAgent(ring, c)
		}
	}()
	signers, _ := ring.Signers()
	return sock, signers[0].PublicKey()
}

func portOf(addr string) int {
	_, p, _ := net.SplitHostPort(addr)
	var n int
	fmt.Sscan(p, &n)
	return n
}

// The key lives only in ssh-agent (as a security key's handle would): the
// command reaches the target through a jump host, both authenticated by the
// agent, and forwarding hands the agent to the target.
func TestSSHAgentAuthProxyJumpAndForwarding(t *testing.T) {
	sock, pub := agentWithKey(t)
	jump := startTestServer(t, pub)
	target := startTestServer(t, pub)
	ex := NewSSH("127.0.0.1", "dev", portOf(target.addr), filepath.Join(t.TempDir(), "no-key"), "").
		WithOptions(SSHOptions{IdentityAgent: sock, ProxyJump: "hopper@" + jump.addr, ForwardAgent: true})
	defer ex.Close()
	if st := ex.ConnStatus(); st.State != "idle" {
		t.Fatalf("before first use: %+v", st)
	}
	r, err := ex.Run(context.Background(), "hostname", RunOpts{Timeout: 5})
	if err != nil || r.Stdout != "ran: hostname" {
		t.Fatalf("run through jump: %+v %v", r, err)
	}
	if len(jump.logins) != 1 || jump.logins[0] != "hopper" || len(target.logins) != 1 || target.logins[0] != "dev" {
		t.Fatalf("logins: jump %v target %v", jump.logins, target.logins)
	}
	r, err = ex.Run(context.Background(), "agent-keys", RunOpts{Timeout: 5})
	if err != nil || r.Stdout != "agent keys: 1 <nil>" {
		t.Fatalf("forwarded agent: %+v %v", r, err)
	}
	st := ex.ConnStatus()
	if st.State != "connected" || st.Via != "hopper@"+jump.addr || st.Transport != "builtin" {
		t.Fatalf("status: %+v", st)
	}
	// A dropped connection is reported, then healed by the next command.
	ex.mu.Lock()
	conn := ex.conn
	ex.mu.Unlock()
	ex.dropClient(conn)
	if st := ex.ConnStatus(); st.State != "reconnecting" {
		t.Fatalf("after a drop: %+v", st)
	}
	if _, err := ex.Run(context.Background(), "again", RunOpts{Timeout: 5}); err != nil {
		t.Fatal(err)
	}
	if st := ex.ConnStatus(); st.State != "connected" || st.Reconnects != 1 {
		t.Fatalf("after reconnect: %+v", st)
	}
}

func TestSSHWithoutAgentNamesTheSecurityKeyProblem(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519_sk")
	os.WriteFile(key, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nbm90IGEga2V5\n-----END OPENSSH PRIVATE KEY-----\n"), 0600)
	os.WriteFile(key+".pub", []byte("sk-ssh-ed25519@openssh.com AAAA test\n"), 0600)
	ex := NewSSH("127.0.0.1", "dev", 1, key, "").WithOptions(SSHOptions{NoAgent: true})
	_, err := ex.Run(context.Background(), "true", RunOpts{Timeout: 2})
	if err == nil || !strings.Contains(err.Error(), "security-key") {
		t.Fatalf("got %v", err)
	}
	if st := ex.ConnStatus(); st.State != "down" || st.LastError == "" {
		t.Fatalf("status: %+v", st)
	}
}

func TestSSHOptionsAllowlistAndArgs(t *testing.T) {
	ok := SSHOptions{Alias: "box", ProxyJump: "a@b:2200,c", ForwardAgent: true,
		Options: []string{"GSSAPIAuthentication=yes", "GSSAPIDelegateCredentials yes"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(ok.OpenSSHArgs(), " ")
	if got != "-J a@b:2200,c -A -o GSSAPIAuthentication=yes -o GSSAPIDelegateCredentials=yes" {
		t.Fatalf("args: %s", got)
	}
	for _, bad := range []SSHOptions{
		{Options: []string{"ProxyCommand=nc %h %p"}},
		{Options: []string{"LocalCommand=touch /tmp/x"}},
		{Options: []string{"PermitLocalCommand=yes"}},
		{Options: []string{"KnownHostsCommand=/bin/x"}},
		{Options: []string{"ControlPath=/tmp/x"}},
		{Options: []string{"novalue"}},
		{Alias: "-oProxyCommand=x"},
		{ProxyJump: "a;b"},
		{Transport: "telnet"},
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	hops, err := parseJumps("u@h1:2200, h2 ,ssh://x@[::1]:22", "me")
	if err != nil || len(hops) != 3 || hops[0] != (jumpHop{"u", "h1", 2200}) || hops[1] != (jumpHop{"me", "h2", 22}) || hops[2].Host != "::1" {
		t.Fatalf("hops %+v %v", hops, err)
	}
}
