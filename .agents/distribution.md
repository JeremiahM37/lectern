# Lectern distribution context

## Product and audience
Lectern — the self-hosted control plane for coding agents.
Repository: https://github.com/JeremiahM37/lectern
An open-source Go service, terminal client and web/PWA interface for developers
running multiple coding agents across workstations, SSH servers and sandboxes.
Audience: developers with more than one machine or agent, self-hosters and
homelab operators who need to dispatch, isolate and supervise coding work.

## Positioning
Lead with where work runs and how it is supervised: choose machine/project →
dispatch agent → isolated workspace → review/approval. Agent interoperability
is central. File transfer and preview support this story; they are not the hook.
Do not let the most recently implemented feature redefine the product.
No competitor comparison or migration pitch. No claims about unique features,
market leadership, adoption, model performance or star growth without evidence.

## Claim boundaries
Shipped: explicit project-to-machine placement, per-target concurrency, task
queue, worktrees, local/SSH/pct execution, Proxmox/Docker/script sandboxes,
agent adapters, handoffs, terminal/web/phone supervision and approvals.
Future direction: generic resource-aware placement (GPU/RAM/OS), unified worker
telemetry, and a versioned worker protocol. Existing MCP and sandbox hooks are
not a generic worker protocol. Do not present these ideas as shipping features.

## Proof and assets
Current screenshots and video: docs/media/control-plane/.
Capture against disposable data on the agent desk. Label simulated machines and
scripted agents; real UI events do not establish real distributed execution.
Show real file movement in the supporting file demo; verify destination bytes.
Keep personally identifying data and credentials out of all published assets.

## Distribution
GitHub README and docs are the destination. Prepare reusable short clips and
release/announcement copy. Public social posts remain drafts for the user to
publish. GitHub owner is JeremiahM37; do not invent other handles.

**Core promise:** choose where agent work runs and stay in control across your machines.
**Proof:** current product workflows, recorded with disclosed demo fixtures;
platform and runtime tests are separate evidence, not implied by the footage.
