package isolation

import (
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// bwrapProxySocketPath is where the host's AllowlistProxy unix socket is
// bind-mounted inside a network=deny sandbox. AF_UNIX sockets are a
// filesystem object, not a network one, so they cross a --unshare-net mount
// boundary intact — that is the whole trick network=deny relies on.
const bwrapProxySocketPath = "/run/lectern-proxy.sock"

// proxyBridgeTCPPort is the loopback port, inside the sandbox's own private
// network namespace, that socat listens on and forwards to
// bwrapProxySocketPath. It never leaves that namespace.
const proxyBridgeTCPPort = "3128"

// WrapOpts is what Wrap needs beyond the Config to build one command.
type WrapOpts struct {
	// Agent selects AgentHomePaths.
	Agent string
	// Workdir is bind-mounted read-write and is where the sandboxed shell
	// starts. Required.
	Workdir string
	// ProxySocket is the host-side unix socket path an already-running
	// AllowlistProxy is serving on (see registry.go). Required when the
	// config's Network is deny for bwrap; ignored otherwise.
	ProxySocket string
}

// BuildBwrapArgv renders the bubblewrap invocation up to (not including) the
// trailing `-- <command>`. It is a pure function of its inputs: no file- or
// environment-probing, so it is safe to unit test without a real bwrap
// binary. $HOME-relative binds use a literal "$HOME/..." shell token,
// expanded by bash at run time on whichever machine actually executes the
// command — this package never assumes it knows the target's home directory.
//
// The system paths mirror tools/run-isolated-tests.sh's own bwrap profile
// (a reviewed, working reference for this exact repo's Debian hosts):
// --ro-bind-try tolerates a host missing /lib64 or /sbin (e.g. non-x86_64)
// rather than failing the whole launch over an optional path.
func BuildBwrapArgv(cfg Config, o WrapOpts) ([]string, error) {
	if strings.TrimSpace(o.Workdir) == "" {
		return nil, fmt.Errorf("isolation: workdir is required")
	}
	n := cfg.Normalized()
	if n.Mode != Bwrap {
		return nil, fmt.Errorf("isolation: BuildBwrapArgv called with mode %q", n.Mode)
	}
	args := []string{
		"bwrap",
		"--die-with-parent",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/etc", "/etc",
		"--ro-bind-try", "/bin", "/bin",
		"--ro-bind-try", "/lib", "/lib",
		"--ro-bind-try", "/lib64", "/lib64",
		"--ro-bind-try", "/sbin", "/sbin",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--tmpfs", homeJoin(""),
	}
	for _, rel := range HomePaths(o.Agent) {
		p := homeJoin(rel)
		args = append(args, "--bind-try", p, p)
	}
	workdir := shellq.Quote(o.Workdir)
	args = append(args, "--bind", workdir, workdir, "--chdir", workdir)
	if n.Network == NetworkDeny {
		if strings.TrimSpace(o.ProxySocket) == "" {
			return nil, fmt.Errorf("isolation: network=deny requires a proxy socket")
		}
		socket := shellq.Quote(o.ProxySocket)
		args = append(args, "--unshare-net", "--ro-bind", socket, bwrapProxySocketPath)
	}
	return args, nil
}

// bwrapDenyBootstrap is the shell fragment that runs inside the sandbox
// before the agent itself, only under network=deny: it bridges the
// bind-mounted proxy socket onto a loopback TCP port private to the
// sandbox's own network namespace (an ordinary process cannot speak
// HTTP_PROXY to a unix socket), waits for socat to be listening, then points
// every common proxy env var convention at it. A process that ignores those
// variables, or opens a raw socket directly, still cannot reach anything:
// --unshare-net left it with no route out at all except this bridge.
func bwrapDenyBootstrap(invocation string) string {
	port := proxyBridgeTCPPort
	// The listening address must come first and carry `fork`: socat only
	// reopens the OTHER address (the unix socket) for each forked child when
	// the forking address drives the loop. Reversed, the unix side is
	// dialed exactly once at startup and every TCP client after the first
	// shares — and, the moment the first one closes, breaks — that single
	// connection (every write after it fails "Broken pipe"). Confirmed by a
	// real run under tools/run-isolated-tests.sh; see internal/autonomy's
	// own bridge for the same, deliberately ordered, invocation.
	return "socat TCP-LISTEN:" + port + ",bind=127.0.0.1,reuseaddr,fork UNIX-CONNECT:" + bwrapProxySocketPath + " >/dev/null 2>&1 &\n" +
		"for _i in $(seq 1 50); do bash -c 'cat </dev/null >/dev/tcp/127.0.0.1/" + port + "' 2>/dev/null && break; sleep 0.1; done\n" +
		"export HTTP_PROXY=http://127.0.0.1:" + port + " HTTPS_PROXY=http://127.0.0.1:" + port +
		" http_proxy=http://127.0.0.1:" + port + " https_proxy=http://127.0.0.1:" + port + " NO_PROXY= no_proxy=\n" +
		invocation
}

// wrapBwrap renders the full bwrap command for one agent invocation.
func wrapBwrap(invocation string, cfg Config, o WrapOpts) (string, error) {
	argv, err := BuildBwrapArgv(cfg, o)
	if err != nil {
		return "", err
	}
	n := cfg.Normalized()
	inner := invocation
	if n.Network == NetworkDeny {
		inner = bwrapDenyBootstrap(invocation)
	}
	full := append(append([]string{}, argv...), "--", "bash", "-c", shellq.Quote(inner))
	return strings.Join(full, " "), nil
}
