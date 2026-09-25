# Autonomous job runner

`tools/autonomy-runner.py` is a Linux-specific privileged launcher, not an agent
prompt filter. Install a reviewed copy as root-owned
`/usr/local/libexec/lectern-autonomy-runner`. Only the trusted controller should
have sudo access to `probe`, `prepare`, `copy`, `report`, `selftest`, `start`, `status`, and `stop`; `_execute` is private
to the systemd service. Do not grant workers sudo access.

The host creates the controller-only jobs root once. `prepare --job UUID` creates
a 2 GiB ext4 loop-backed volume mounted at the job’s `work` directory. The
controller populates it with a **copy**, never a worktree pointing outside the
job, and creates `prompt.txt` and a dedicated mode-0755 `bridges` directory.
The required public egress proxy is `bridges/network.sock`; the optional scoped
broker is `bridges/bridge.sock`. Socket permissions
must permit UID 65534. Then:

```
sudo /usr/local/libexec/lectern-autonomy-runner probe
sudo /usr/local/libexec/lectern-autonomy-runner prepare --job UUID
# controller populates snapshot, prompt, and per-job sockets
sudo /usr/local/libexec/lectern-autonomy-runner start --job UUID --provider codex --model MODEL --prompt /mnt/bulk/lectern-autonomy/jobs/UUID/prompt.txt
sudo /usr/local/libexec/lectern-autonomy-runner status --job UUID
sudo /usr/local/libexec/lectern-autonomy-runner stop --job UUID
```

Status emits `{ "state": "running|done|failed|stopped", "exit_code": null|N }`.
The unit survives a brief controller restart, but stops after 60 seconds without
a fresh `heartbeat` mtime. The controller must touch that file at each usage
monitor tick. A stale heartbeat terminates only the matching job cgroup, even
if an agent has background children. Output remains in `output.jsonl`, errors
in `stderr.log`, and work remains on disk after completion or cancellation. Stop
affects only the exact UUID's cgroup. It never targets tmux, SSH, existing agent
sessions, or a process by executable name. A UUID cannot be reused.

## Enforced boundaries

* A private filesystem with read-only system tools, minimal DNS/NSS/CA files,
  private HOME, `/tmp`, `/run`, PID, IPC, UTS and network namespaces. Only the copied
  workspace is persistently writable. No host home directories, root directory,
  configuration files, service sockets, SSH keys or Grimoire secrets are mounted.
* Bubblewrap executes as host UID/GID 65534 with no supplementary groups,
  no capabilities, and `NoNewPrivileges`. Private user namespace privileges do
  not grant host privileges. CLI permission bypasses apply inside this boundary.
* An independently copied native ELF provider CLI and only that provider's
  current authentication file are mounted read-only. Host CLI wrappers and global
  MCP configurations are not inherited. Credential refresh cannot persist to the
  original file; expired credentials therefore require controller intervention.
* Root systemd cgroups enforce 30-minute runtime, 4 GiB RAM, no swap, 200% CPU,
  256 tasks, and a 256 MiB per-file size limit. Child processes remain in the
  same cgroup; cancellation sends TERM and then KILL after ten seconds.
* Cgroup IP filters deny RFC1918, CGNAT/Tailscale, link-local,
  multicast, IPv6 ULA, IPv4-mapped IPv6, and every currently assigned non-loopback host IP.
  Loopback is permitted only within the private network namespace. Public
  Internet access goes through a per-job Unix socket HTTP proxy. The controller
  must validate DNS destinations as globally routable, pin the resolved address
  when dialing. CONNECT is restricted to exact inference hosts on 443; all
  ordinary proxy requests are refused. Public reading goes through /research.
  Before **each** job, the trusted launcher queries the kernel for effective
  ingress and egress BPF programs on its actual cgroup. Missing support or
  permissions fails closed before running any provider CLI. `probe` creates a
  harmless temporary filtered sleep unit and checks this same kernel mechanism.
* Symlinks are copied as links, never followed during inspection, copying or
  ownership changes. Their targets resolve inside the private worker filesystem.
  Hardlinked regular files and special files are refused. Artifact collectors
  must independently reject escaping symlinks when reading on the host.

## Boundaries that are not promised

This is a process/container boundary, not a separate kernel or VM. Kernel
vulnerabilities remain out of scope. Read-only provider credentials are still
**readable by the worker**; it can use or disclose its own provider credential.
General public egress is denied. Opaque TLS tunnels are limited to chatgpt.com, api.openai.com and api.anthropic.com for inference. These provider tunnels remain a trust boundary: their encrypted application requests are not inspected by Lectern. The controller must not
include secrets or confidential material in prompts or snapshots without an
appropriate policy. No SSH, GitHub, registry or messaging credentials are supplied. The research
broker fetches only approved reading hosts, using GET without caller headers,
query strings, credentials or redirects. Missing dependencies must be reported,
not installed through an unrestricted proxy.

The worker has a private network namespace: it cannot reach host abstract Unix
sockets. A local socat listener forwards HTTP(S) proxy requests to the explicitly
mounted `network.sock`. Direct public sockets cannot route from the private
namespace. The optional `/bridge.sock` is the only direct application bridge;
its route allowlist must remain read-only/scoped. The runner cannot validate the
host proxy's implementation, so adversarial destination tests are required.

The workspace has a hard 2 GiB capacity. New jobs are refused below 20 GiB host
free space or when retained job images use 50 GiB of physical storage. Images
and loop mounts are retained deliberately, including cancelled jobs; an explicit
archive workflow is required before reclaiming them. Private temporary files
count against the memory limit. The script alone does not enforce the user's
remaining-token reserve: the controller must obtain fresh provider usage and
stop the job before allowance reaches that threshold.
Nor does it authorize publishing, deploying, spending money, sending messages,
or changing live services. Promotion of artifacts belongs to the controller's
separate backed-up and verified workflow.

## Required commissioning checks

Do not claim this runner is validated merely because its Python compiles.
Run actual harmless jobs to demonstrate host-secret and host-process isolation,
blocked local/private/IPv6 connections, public HTTPS, non-escalation, cgroup
limits, cancellation with persisted artifacts, and unknown/stale usage stops.
Use both real provider CLIs only after the no-model isolation checks pass.

Independent review uses `copy --job REVIEW_UUID --from-job BUILD_UUID` after
preparing the review volume and after the builder stops. Copy preserves symlinks without following them and refuses
hardlinked regular files, special files, running sources, nonempty destinations, more than
100,000 entries, or more than 1900 MiB. It never copies the root
`autonomy-report.json`, so a missing reviewer report cannot inherit the
builder's self-assessment. No git configuration or repository hooks execute.

`selftest --job UUID --prompt .../prompt.txt` starts an already prepared job
through the same supervisor, mounts, cgroup, private network, and proxy socket,
but runs a fixed Python isolation probe instead of any provider CLI. It copies
no provider credentials. It checks host paths/processes are hidden, effective
capabilities are empty, no-new-privileges is active, inherited host directory
FDs are closed, `/usr` is read-only, direct Internet routing is unavailable,
and a workspace artifact persists. `output.jsonl` contains a PASS object on
success. It still requires a per-job `network.sock`; separately test that proxy
against private, mapped, DNS-rebinding and public HTTPS destinations. Omitting
`--model` on a real job uses the provider's built-in default, with a clean HOME.

The job parent is root:admin 0770 so the trusted controller can restore its
per-job sockets after a restart; it remains inaccessible to the worker's host
UID 65534. Worker artifacts can be mode 0600. `report --job UUID` therefore
reads only the completed job's exact root `autonomy-report.json` through a
no-follow directory/file descriptor, refusing symlinks, hardlinks, nonregular
files, live jobs, malformed JSON, and reports above 128 KiB. It returns the
original JSON bytes. This does not make the report trustworthy; independent
review is still required.

The trusted supervisor first creates a private mount namespace and stages only
selected job assets, workspace and sockets beneath a private tmpfs `/tmp`. It
then drops to host UID 65534 before starting bubblewrap. This avoids exposing
credential directories on the host: bubblewrap canonicalizes source paths, so
passing protected host directory FDs alone is insufficient. Systemd proc-masking
settings `ProtectKernelTunables`/`ProtectKernelLogs` are intentionally absent
because their locked proc mounts prevent an unprivileged child from mounting
its private PID namespace procfs. The worker has no effective capabilities,
no-new-privileges, no host `/sys`, and no host `/proc` mount.

Add `--network-selftest` when the real controller proxy is bound: the same
sandbox verifies public HTTPS to example.com succeeds while loopback, CGNAT,
IPv6 loopback and a localhost hostname are denied with HTTP 403. Dummy socket
probes omit this option. `--hold-seconds 90` keeps a credential-free selftest
alive for a bounded cancellation/heartbeat test.

`archive --job UUID` streams a gzip tar of completed `work` only; it does not
include sibling assets, credentials, logs or metadata. Links are archived as
links, special files are refused, and privilege bits/owners are stripped. Treat
archives as untrusted and use a safe extractor. The 50 GiB retention interlock
counts physical blocks of images **and** binaries/logs/metadata, excluding the
mounted workspace to avoid counting the image twice.

After host reboot, report/copy/archive can remount the retained image. The
runner requires a root-owned, singly-linked, fixed 2 GiB image, an empty real
mountpoint directory, and verifies the resulting ext4 loop device points to
that exact image. It never formats an existing image or resumes an interrupted
job automatically.

The entire dedicated `bridges` directory is bound read-only into the worker,
with `/network.sock` and `/bridge.sock` compatibility symlinks. A controller
restart may unlink/rebind socket files **inside the same directory**; the
worker's new connections see the replacement inodes. Do not replace the bridges
directory itself while a job runs. Binding individual socket inodes would pin
dead sockets after a restart. A real reconnect probe verifies public HTTPS and
private-destination denial before and after terminating/rebinding the proxy
while the same worker remains alive. `--network-selftest --hold-seconds 5`
repeats the network checks after the hold to support that test.

## Controller research directory

The homelab service runs as admin. Provision its persistent research directory
before enabling the workshop:

```sh
sudo install -d -o admin -g admin -m 0750 /mnt/bulk/lectern-autonomy/research
```

The root-owned parent remains protected; its research child must be writable by
the controller so first-run Git/project initialization succeeds. Do not
recursively chown job images or worker mounts. This directory is persistent and
already covered by the workshop backup. Do not use systemd-tmpfiles here: this
host's admin-owned /mnt/bulk followed by the root-owned workshop parent triggers
its unsafe ownership-transition check.
