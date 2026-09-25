# Docker and remote setup

Docker runs the hosted control plane. Its `local` target is **inside the
container**. To run agents installed on your laptop/server, register that
machine as an SSH target instead. Agent CLIs and provider sign-in belong on
the target; Lectern does not supply a subscription or agent login.

## Start privately

From a source checkout (Docker Compose v2):

```sh
docker compose -f deploy/docker-compose.yml up -d --build
```

Open http://localhost:9110. The default published port binds only to localhost.
Choose **Add your first machine** and add a local or SSH target in Settings.
A local target can open a blank shell immediately; an agent session additionally
requires an installed, authenticated CLI. Target **Probe** reports missing tools.
Each target needs Git, tmux and Python 3.

The `/data` volume stores the database, container home, scratch files and
recovery data. Keep project/worktree directories under `/data` or explicitly
bind mount them. Back up that volume. `docker compose down` preserves it;
`down -v` deletes it. A container restart ends container-local processes, but
retains files and conversation records. Remote SSH sessions survive a control
plane restart because their tmux server runs on the target.

## Reach it remotely

Either forward the private port:

```sh
ssh -L 9110:127.0.0.1:9110 your-server
```

or configure a trusted-network listener and a token before starting Compose:

```sh
export LECTERN_BIND=0.0.0.0
export LECTERN_AUTH_TOKEN="$(openssl rand -hex 32)"
export LECTERN_BASE_URL=https://lectern.your-private-domain.example
```

Use a private HTTPS reverse proxy for browser access. Enter the token in the
browser's access-token dialog. Keep the token in a private environment file
(mode 600) for future restarts. The base URL must be reachable from targets
for agent callbacks; localhost is only suitable when the target shares the
control plane's network. Do not expose an unauthenticated port to a public
network. When proxying, keep the token requirement enabled.

## SSH targets

Use a dedicated key authorized on the target. Put that key
in a private `ssh/` directory beside the Compose file,
then enable its read-only volume line. Set the target's key path to
`/data/home/.ssh/id_ed25519`. The current server-side SSH executor does not verify SSH host keys; a
mounted known_hosts file does not change that. Use this on a trusted private
network until host-key verification is supported.
The SSH host is resolved from **inside the container**; `localhost` refers
to the container, not the Docker host. Use a reachable hostname/IP.

## Terminal client on another computer

Install the client there, then configure:

```sh
export LECTERN_API=https://lectern.your-private-domain.example
export LECTERN_AUTH_TOKEN=your-access-token
export LECTERN_ATTACH_HOST=your-ssh-alias-for-the-control-plane
lectern
```

The browser terminal needs no client SSH configuration. Native terminal attach
needs SSH to the control-plane host and a reachable `lectern` command there;
for Docker, prefer browser attach unless you have explicitly provided a host
launcher into the container. Native Windows is a remote client; use WSL for a
local runtime. `lectern up` is local-only and rejects a configured remote API.
Local mode trusts processes running as your OS user; it is not isolation from
other agents running under that same account.
