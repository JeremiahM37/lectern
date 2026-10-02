# GPU lease recovery protocol (experimental qualification)

The helper is a coordination foundation, not GPU execution capability. Its only
production CLI actions are `check`, `guest-start`, `guest-stop` and `reconcile`.
There is no worker command, arbitrary guest selector or root shell. Fixed
`SystemdGuestBackend.launch` now starts only the root-prepared executor described
below. The fixed-target executor has passed an actual hardware qualification
covering a reference run, a negative control and owned-work cancellation. This
is a scoped execution and cleanup result, not timing qualification or general
GPU availability. A real HTTP/controller fixture also passed with the frozen
runner, current source, shared lease fence and guest GPU execution: it verified
a successful reference result through authenticated output with matching digest,
and a terminal receipt with cleanup confirmed. Its assignment and quota records
were synthetic, so this does not qualify production admission or autonomous
workshop outcomes. No production workload selector is present, and this does
not establish full autonomous GPU execution.

`tools/research-lease.sh` is an installation template for the existing
`/mnt/bulk/inference-research/floor/research-lease.sh`. Do not run the checkout
wrapper as an alternate lease. Deployment must keep the existing
`.machine-leases/host-lxc105.lock` inode/path and update every cooperating entry
point before claiming protection. Direct `flock` callers bypass the fence;
noncooperating GPU services, including Ollama, remain outside this advisory
coordination. No deployment or live lock acquisition is part of these tests.

After acquiring the same physical flock, the template asks the installed root
helper to check its private owner record, then runs the original supervisor as
the original caller. It never passes that supervisor command to the root helper.
An unresolved remote owner returns exit75 even if the crashed host process has
released its flock. The wrapper cannot repair remote state; a bounded trusted
reconciler does that through fixed `pct exec 105`, not worker SSH credentials.

Host library `HostFence.acquire(binding)` seals ownership before yielding
launch authority. Binding has exactly schema_version=1, target=
`aiserver-amd-research-v1`, run_id (64hex), generation (positive integer),
request_sha256 (64hex), guest_boot_id (UUID). It records physical lock inode/device
and host boot ID. Exiting the context never silently clears ownership. Only
`reconcile(backend)` or `cancel(binding, backend)` can clear after authenticated
transport reports the same binding, exact derived unit, stopped state, empty
owned cgroup and durable guest launch revocation. Guest boot changes are explicit
in the retained cleanup receipt and still require empty owned unit/cgroup.
Unreachable or uncertain state retains the fence.

`cancel` writes a durable host tombstone even before reservation/request dispatch,
then asks the guest to revoke under its shared launch guard. A delayed reservation
or guest launch for that run cannot proceed. Each run ID is single execution;
new authorized work uses a new run ID, not generation rewriting of an old run.
Completed/cancelled receipts remain immutable. Ownership records are root-private,
regular, single-link files with atomic fsynced publication and canonical SHA256.
The Python constructor's owner override exists for unprivileged disposable tests;
production CLI fixes owner to root and accepts no filesystem overrides.

Guest `GuestFence.start(binding)` persists launch_requested before dispatch and
never repeats an uncertain launch. A backend must retain the launchguard FD through
an asynchronous launcher handoff (as existing runner systemd launch code does),
then close it once launch is settled. A killed launcher cannot permit concurrent
stop to falsely report completion while a surviving child is still launching.
The guard is nonblocking; callers retry bounded Busy responses. Guest stop
persists revocation before issuing systemctl stop --no-block, checks required
systemd properties plus the exact derived cgroup's populated value, and reports
stopping until cleanup is authoritative. Missing/incomplete systemd output fails
closed. Request bytes arrive only over fixed trusted root transport; a worker
receipt is never a backend proof.

Tests: `python3 tools/test_autonomy_gpu_lease.py`. They use disposable local files,
real flock, fork/SIGKILL, bounded child processes, inherited descriptors and a
simulated guest transport. They test orphan exclusion, cancellation before
reservation/dispatch, failed transport, uncertain/foreign cleanup, replay,
identity corruption and reboot evidence. They do not prove actual LXC systemd
or GPU behavior. Before integration, add real disposable guest unit/systemd
cleanup tests, deployment inode preservation/all-launcher audit, source/runtime
isolation, device identity, cumulative budgets and thermal/resource tests.

## Fixed guest executor

`autonomy-gpu-executor.py` consumes a root-prepared run selected only by 64hex ID.
Preparation binds source archive SHA, runtime key, fixed profile, inline Python
and bounded argv to binding.request_sha256. It verifies/extracts regular source
members (links rejected), freezes the source, records full directory/file identity,
and seals/fsyncs the preparation before publication. Runtime registration accepts
an administrator-curated rootfs, never worker paths; it executes no code and
publishes an immutable directory/file inventory with content hash and fsync before
rename. The runtime includes its own interpreter and libraries; no host /usr,
/root, home, credential or service socket is mounted as fallback. Rootfs, bundle,
registry and parent directory ownership/modes are validated. Source files and
runtime directory modes participate in verification, including the root itself.

Fixed profiles are gpu-screen600 (600sec,8GiB RAM,4CPU,256tasks,2GiB scratch,
16MiB output) and device-free30 (30sec,512MiB RAM,1CPU,64tasks,64MiB scratch,
1MiB output). The latter exists for proving the executor without device access.
Systemd unit name is derived from the sealed run ID; it has control-group kill,
5sec stop timeout, no swap and bounded scratch tmpfs. The executor requires that
exact cgroup and scratch mount, verifies runtime/source before execution, enters
a private mount namespace, exposes only input binds, drops groups/GID/UID to
65534, then starts bwrap with all namespaces, no capabilities, fresh environment,
read-only source/runtime and the bounded scratch mount (also used for /tmp).
GPU profile mounts only registered kfd/render character devices after exact
major/minor checks; the device-free profile mounts neither. No device identity,
clock/thermal or per-process GPU-memory guarantee is inferred from these tests.

The executor retains a single-execution intent before child creation, captures
bounded combined output with hashes, and seals exit/timeout/output-limit/cancelled
receipts. Closing stdout early does not avoid walltime accounting. Detached
children remain in the owned systemd cgroup/PID namespace; cancellation kills
that tree and the guest fence requires cgroup-empty proof before release.

Tests now include `python3 tools/test_autonomy_gpu_executor.py`. Real disposable
HOST systemd+bwrap proof is `/mnt/bulk/workshop-quality-20260925/`:
`prove-gpu-executor-device-free.py` and `gpu-executor-device-free-proof.json`.
It includes correct/wrong-result controls, output/scratch bounds, early closed
output streams and setsid descendant cancellation. This is not an LXC105/ROCm
proof. Failed exploratory fixture directories are retained; first UID mapping
and buffered log assumptions were corrected before the final cancellation proof.
This device-free proof is distinct from the fixed-target hardware and
controller-fixture qualifications described above. Cumulative controller
budgets and production workload selection remain outside the qualified scope.

The same six-case fixture subsequently passed in **actual LXC105** after the
coordinator's separately backed-up bubblewrap installation. Evidence:
`gpu-executor-guest-device-free-proof.json`, disposable guest tree
`/var/tmp/gpu-device-free-proof-q9ui9lox`. All six unique units were stopped or
collected; cancellation observed live owned descendants before stop and exact
empty cgroup afterward. It used a separate curated Python3.11-only rootfs, not
the research Torch/ROCm installation. No GPU device, physical research lease,
existing research runtime or unrelated guest service was used or changed.
This establishes guest isolation/lifecycle, not GPU compatibility or availability.

Administrator-curated runtime rootfs registration now preserves internal relative
file and directory symlinks. Inventory records link text without copying target
contents twice; link ownership remains trusted, while Unix symlink mode0777 is
not interpreted as file writability. The resolver handles each component inside
the root, refuses absolute/escaping/dangling links and bounded cyclic chains,
and rejects directory graph cycles (including cross-directory and ancestor
cycles). The walker never descends through a link. File/directory modes and all
link targets remain authenticated. Source archives still reject every link.
The six-case device-free proof was repeated using the interpreter's required
libc.so.6 as a relative link; pre-change receipts remain in separate
`*-before-runtime-links.json` files.

### ROCr discovery

GPU execution additionally binds these sysfs discovery trees read-only:
`/sys/devices/virtual/kfd/kfd/topology`, `/sys/devices/system/cpu`, and
`/sys/devices/system/node`. ROCr reads KFD agent and CPU/NUMA topology before
opening devices. The device-only sandbox failed real PyTorch GPU discovery.
The full sysfs tree, KFD process metadata and driver controls are not exposed.
Device-free profiles receive none of these mounts. This exposes hardware
metadata; it does not grant sysfs write access or establish GPU qualification.
Runtime registration creates empty discovery mountpoint directories before
freezing and hashing the rootfs. Existing snapshots retain their identity;
register a new snapshot rather than adding directories to an immutable one.

The GPU profile builds a private `/sys` tree and remounts it read-only after
binding discovery metadata. libdrm additionally receives only the selected
PCI device's vendor/device/revision/subsystem IDs and uevent, and render-node
dev/uevent files. Canonical PCI/render symlinks preserve discovery semantics;
the live render-to-PCI mapping must match the registered fixed target.
PCI config, resources, ROM/reset controls, other GPUs and primary card devices
remain absent. The private tree contains no writable workload storage.

### Qualified helper versions

`guest-identity` reports `helpers` with `executor_sha256` and `lease_sha256`.
Both files must be root-owned, non-writable regular files with one link; reads
are bounded and refuse symlinks. New execution envelopes require that exact
map, which is covered by `binding.request_sha256`. Preparation and execution
recheck the installed helper hashes before running code; execution receipts
retain the selected map. Missing or stale helper identities cannot launch a
new workload. Existing terminal receipts and ownership cleanup bindings remain
readable so upgrades never prevent cleanup of an earlier run.

Preparation stores the verified helper bytes in a frozen `helpers/` directory
inside that run. The guest launcher verifies these copies and imports/launches
the frozen executor, which imports the neighboring frozen lease helper. Its
receipt therefore refers to that job's code, independent of later global
helper deployment. Root administrators remain trusted; neither workers nor the
installer write previously prepared helper copies. Cleanup uses stable run
bindings and can still reconcile legacy jobs without launching their code.

Guest identity reports live free bytes and a 22 GiB staging floor. The host
checks that before uploading source; new guest preparation checks again before
copying. Already prepared work remains retryable without allocating another
copy. This is a free-space guard, not a claim that guest storage is covered by
the host workshop's aggregate retention accounting.
