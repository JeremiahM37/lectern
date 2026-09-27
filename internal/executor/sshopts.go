package executor

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// SSHOptions are a target's SSH settings beyond host, user, port and key
// (docs/ssh.md). They live in targets.ssh_json.
type SSHOptions struct {
	// Alias is a Host entry in the control plane's ~/.ssh/config. The OpenSSH
	// transport and the web terminal connect to it by name, so everything the
	// entry says (Include, Match, certificates, GSSAPI...) applies.
	Alias string `json:"alias,omitempty"`
	// ProxyJump is OpenSSH's -J: [user@]host[:port], comma-separated hops.
	ProxyJump string `json:"proxy_jump,omitempty"`
	// ForwardAgent forwards the control plane's ssh-agent to the target for
	// the length of each command (git over SSH, for one).
	ForwardAgent bool `json:"forward_agent,omitempty"`
	// NoAgent stops the built-in transport offering keys from ssh-agent.
	NoAgent bool `json:"no_agent,omitempty"`
	// IdentityAgent is the agent socket; empty means $SSH_AUTH_SOCK.
	IdentityAgent string `json:"identity_agent,omitempty"`
	// Options are extra OpenSSH Key=Value options, from an allowlist.
	Options []string `json:"options,omitempty"`
	// Transport picks what runs commands: "" (built in) or "openssh".
	Transport string `json:"transport,omitempty"`
	// EditorHost is the name your own computer knows this machine by, for
	// "Open in editor" links. Empty means the alias or host.
	EditorHost string `json:"editor_host,omitempty"`
}

// ParseSSHOptions reads targets.ssh_json; anything unreadable is no options.
func ParseSSHOptions(raw string) SSHOptions {
	var o SSHOptions
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &o)
	}
	return o
}

// JSON renders the options for storage.
func (o SSHOptions) JSON() string {
	b, _ := json.Marshal(o)
	return string(b)
}

// safeSSHOptions is every OpenSSH option a target may set. Options that run a
// command or load code on the control plane (ProxyCommand, LocalCommand,
// KnownHostsCommand, PKCS11Provider, SecurityKeyProvider, Match exec...) are
// deliberately absent: put those in ~/.ssh/config and use the alias, which is
// the operator's own file, not a field an API caller can set. The connection
// multiplexing options are Lectern's to manage.
var safeSSHOptions = map[string]bool{}

func init() {
	for _, k := range []string{
		"AddKeysToAgent", "AddressFamily", "BatchMode", "BindAddress", "BindInterface",
		"CanonicalDomains", "CanonicalizeHostname", "CertificateFile", "CheckHostIP",
		"Ciphers", "Compression", "ConnectionAttempts", "ConnectTimeout",
		"ForwardAgent", "GSSAPIAuthentication", "GSSAPIClientIdentity",
		"GSSAPIDelegateCredentials", "GSSAPIKeyExchange", "GSSAPIKexAlgorithms",
		"GSSAPIRenewalForcesRekey", "GSSAPIServerIdentity", "GSSAPITrustDns",
		"HostKeyAlgorithms", "HostKeyAlias", "HostbasedAuthentication", "Hostname",
		"IdentitiesOnly", "IdentityAgent", "IdentityFile", "IPQoS",
		"KbdInteractiveAuthentication", "KexAlgorithms", "LogLevel", "MACs",
		"NoHostAuthenticationForLocalhost", "PreferredAuthentications", "Port",
		"PubkeyAcceptedAlgorithms", "PubkeyAcceptedKeyTypes", "PubkeyAuthentication",
		"RekeyLimit", "RequiredRSASize", "SendEnv", "ServerAliveCountMax",
		"ServerAliveInterval", "SetEnv", "StrictHostKeyChecking", "TCPKeepAlive",
		"UpdateHostKeys", "User", "UserKnownHostsFile", "VerifyHostKeyDNS",
	} {
		safeSSHOptions[strings.ToLower(k)] = true
	}
}

// Validate refuses options Lectern will not pass to ssh.
func (o SSHOptions) Validate() error {
	if o.Transport != "" && o.Transport != "builtin" && o.Transport != "openssh" {
		return fmt.Errorf("transport must be builtin or openssh")
	}
	for _, raw := range o.Options {
		key, value, ok := splitOption(raw)
		if !ok {
			return fmt.Errorf("option %q must look like Key=Value", raw)
		}
		if !safeSSHOptions[strings.ToLower(key)] {
			return fmt.Errorf("option %s is not allowed here; options that run commands on the Lectern server belong in ~/.ssh/config (then use the alias)", key)
		}
		if strings.ContainsAny(value, "\n\r") {
			return fmt.Errorf("option %s has a line break", key)
		}
	}
	if strings.HasPrefix(strings.TrimSpace(o.ProxyJump), "-") || strings.HasPrefix(o.Alias, "-") {
		return fmt.Errorf("alias and proxy jump cannot start with '-'")
	}
	if strings.ContainsAny(o.Alias+o.ProxyJump+o.EditorHost, " \t\n\r'\"`$;|&<>") {
		return fmt.Errorf("alias, proxy jump and editor host cannot contain spaces or shell characters")
	}
	return nil
}

func splitOption(raw string) (string, string, bool) {
	raw = strings.TrimSpace(raw)
	key, value, ok := strings.Cut(raw, "=")
	if !ok {
		key, value, ok = strings.Cut(raw, " ")
	}
	key, value = strings.TrimSpace(key), strings.TrimSpace(value)
	return key, value, ok && key != "" && value != ""
}

// ConfigFileArgs points ssh(1) at LECTERN_SSH_CONFIG when set, the same file
// hosts are imported from, so an imported alias resolves the same way.
func ConfigFileArgs() []string {
	if p := os.Getenv("LECTERN_SSH_CONFIG"); p != "" {
		return []string{"-F", p}
	}
	return nil
}

// OpenSSHArgs are the ssh(1) arguments these options add: jump hosts, agent
// forwarding and extra options. Shared by the OpenSSH transport and the web
// terminal so both reach the machine the same way.
func (o SSHOptions) OpenSSHArgs() []string {
	args := ConfigFileArgs()
	if jump := strings.TrimSpace(o.ProxyJump); jump != "" {
		args = append(args, "-J", jump)
	}
	if o.ForwardAgent {
		args = append(args, "-A")
	}
	if o.IdentityAgent != "" {
		args = append(args, "-o", "IdentityAgent="+o.IdentityAgent)
	}
	for _, raw := range o.Options {
		if key, value, ok := splitOption(raw); ok && safeSSHOptions[strings.ToLower(key)] {
			args = append(args, "-o", key+"="+value)
		}
	}
	return args
}

// agentSocket is where the agent listens, or "" when there is none to use.
func (o SSHOptions) agentSocket() string {
	if o.NoAgent {
		return ""
	}
	sock := o.IdentityAgent
	if sock == "" {
		sock = os.Getenv("SSH_AUTH_SOCK")
	}
	if strings.EqualFold(sock, "none") {
		return ""
	}
	if strings.HasPrefix(sock, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			sock = home + sock[1:]
		}
	}
	return sock
}

// jumpHop is one ProxyJump hop.
type jumpHop struct {
	User string
	Host string
	Port int
}

// parseJumps splits a ProxyJump value into hops, defaulting user and port.
func parseJumps(value, defaultUser string) ([]jumpHop, error) {
	var out []jumpHop
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "ssh://"))
		if part == "" || strings.EqualFold(part, "none") {
			continue
		}
		hop := jumpHop{User: defaultUser, Port: 22}
		if u, rest, ok := strings.Cut(part, "@"); ok {
			hop.User, part = u, rest
		}
		host, port, err := net.SplitHostPort(part)
		if err != nil {
			host = strings.Trim(part, "[]")
		} else {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return nil, fmt.Errorf("proxy jump %q has a bad port", part)
			}
			hop.Port = n
		}
		if host == "" {
			return nil, fmt.Errorf("proxy jump %q has no host", part)
		}
		hop.Host = host
		out = append(out, hop)
	}
	return out, nil
}
