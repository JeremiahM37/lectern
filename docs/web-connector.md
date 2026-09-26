# Web connector: driving Lectern from claude.ai

`lectern mcp --http` serves the same MCP tool surface as `lectern mcp`
(stdio) over the Streamable-HTTP transport, protected by OAuth 2.1, so the
owner's everyday claude.ai chats — web or the mobile app — can add Lectern as
a **custom connector** and drive it directly: "start a Lectern session that
builds this," "give my open session doing X this file and start."

claude.ai's connector runs from Anthropic's own cloud, not from the owner's
browser. It cannot be configured with a static bearer header — the only
credential it can send is whatever a normal OAuth authorization-code+PKCE
flow hands it — so this is what makes Lectern reachable as a connector at
all. It is ported from [Grimoire's identical, already-live
feature](https://github.com/JeremiahM37/grimoire) (the same owner's sibling
project): Dynamic Client Registration (RFC 7591), PKCE S256 (required, not
just preferred), RFC 9728 protected-resource metadata, RFC 8414
authorization-server metadata, and RFC 8707 resource indicators.

## The security model, in one picture

```
                     ┌─────────────────────────────┐
   claude.ai cloud   │  PUBLIC listener (internet-  │
   ───────────────►  │  reachable via a tunnel)     │
                      │  /mcp                        │
                      │  /.well-known/oauth-*        │
                      │  /oauth/register             │
                      │  /oauth/token                │
                      │  /oauth/revoke               │
                      └──────────────┬───────────────┘
                                      │ tokens looked up in the
                                      │ same sqlite store
                      ┌───────────────┴───────────────┐
   the owner's own    │  PRIVATE listener (tailnet-    │
   browser, tailnet   │  only)                         │
   or loopback only   │  /oauth/authorize  (consent)   │
   ───────────────►   │  /admin/oauth      (clients)   │
                      └────────────────────────────────┘
```

The one fact that makes this safe: **nothing in the OAuth
authorization-code flow requires the authorization endpoint to be on the
same network as the client** — only the browser doing the redirect has to
reach it. So the `/oauth/authorize` consent page that actually grants access
is *never* mounted on the public listener at all (see
`internal/oauth.Handler.RegisterPublic` vs `RegisterPrivate` —
`RegisterPublic` simply has no handler for that path, so it 404s by
construction, not by a check someone could forget to add). It only lives on
a second, tailnet-only listener, so the only browser that can ever approve a
connector is the owner's own.

Other decisions worth knowing before you turn this on:

- **Scopes are `lectern.read` and `lectern.write`.** Read covers everything
  that only observes the board (`list_sessions`, `list_tasks`,
  `list_projects`, `board_summary`, `task_status`, `task_diff`,
  `pending_approvals`, `active_work`, `list_claims`, `wait_build`). Write
  covers everything that starts, sends into, files, or dispatches work
  (`start_session`, `send_to_session`, `create_task`, `delegate_build`,
  `complete_task`, `request_changes`, `accept_build`, `claim_work`,
  `release_work`, `post_media`, `open_live_view`). Both are on by default on
  the consent page — see `internal/oauth/consent.go`'s `scopeInfo` — because
  neither can reach outside the board itself (no third-party spend, unlike
  Grimoire's separate credential-broker scope).
- **`decide_approval` is never reachable over this transport, at all,
  regardless of scope.** It is structurally absent from the scope table
  (`internal/mcp/scope.go`'s `webExcluded`), not merely gated behind a scope
  nothing grants — a token with every scope checked still cannot call it,
  and it is filtered out of `tools/list` unconditionally. Approval decisions
  are the operator's call, never a remote LLM's, on principle: see
  `TestWebConnectorDecideApprovalNeverReachable` and
  `TestDecideApprovalIsWebExcludedNotScoped`.
- **The local `files` parameter is refused for this transport.**
  `start_session`/`send_to_session` accept `files` (absolute paths on the
  machine the MCP process runs on) for the stdio case, where the caller and
  the MCP server share a filesystem. claude.ai has no such filesystem, and
  must not be able to make this process read an arbitrary host path on its
  behalf — so a `Server.Remote` flag (set only when built for `--http`)
  makes `files` a clear, immediate tool error instead of a silent read.
  Use **`inline_files`** instead: `{name, content, encoding}` — the file's
  actual bytes, pasted or chat-uploaded, up to 10 files and 5 MiB each after
  decoding. See [sessions-mcp.md](sessions-mcp.md) for the full
  `start_session`/`send_to_session` reference.
- **Tokens are hashed at rest**, exactly like every other secret Lectern
  stores. Refresh tokens rotate on use (the old one is revoked the moment a
  new one is issued — OAuth 2.1's reuse-detection requirement), and
  `/oauth/revoke` (RFC 7009) always answers success whether or not the token
  was real, so it can't be used to test which tokens are live.
- **`/oauth/register` and `/oauth/token` are the two endpoints an attacker
  could hammer without ever reaching consent** — DCR needs no credential at
  all, and the token endpoint is what a stolen `code`/`refresh_token` would
  be redeemed against. Put your tunnel's own rate limiting in front of the
  public listener (a Cloudflare tunnel or `tailscale funnel` both support
  this); this package does not rate-limit internally.
- **Redirect URIs are checked against a HOST allowlist at registration**,
  then an **exact match** at `/oauth/authorize` — see
  `internal/oauth/redirect.go`'s doc comment for why a host allowlist rather
  than a hardcoded path (neither Anthropic nor OpenAI publishes a stable,
  versioned callback path). The default allowlist covers `claude.ai`,
  `claude.com`, `chatgpt.com`, `openai.com` subdomains, and
  `localhost`/`127.0.0.1` for local testing with the MCP Inspector.

## Environment variables

| Variable | Required | What it does |
|---|---|---|
| `LECTERN_PUBLIC_BASE` | to enable OAuth | The public HTTPS origin claude.ai reaches, e.g. `https://lectern-mcp.example.ts.net`. Must be set together with `LECTERN_OAUTH_AUTHORIZE_BASE` — one without the other refuses to start. |
| `LECTERN_OAUTH_AUTHORIZE_BASE` | to enable OAuth | The base URL of the **private** consent listener, e.g. `https://lectern.tailXXXX.ts.net:9120` — must only ever be reachable from the tailnet or loopback. |
| `LECTERN_MCP_ADDR` | no (default `127.0.0.1:9119`) | Where the public `/mcp` + OAuth-protocol listener binds. |
| `LECTERN_OAUTH_AUTHORIZE_ADDR` | no (default `127.0.0.1:9120`) | Where the private consent listener binds. Keep this loopback or tailnet-bound — see the security model above. |
| `LECTERN_OAUTH_DB` | no (default `~/.lectern-mcp/oauth.db`) | sqlite file for registered clients, codes and tokens. |
| `LECTERN_OAUTH_ALLOWED_LOGINS` | to use the Tailscale fast-path | Comma-separated logins (e.g. `jam@github`) allowed to pre-approve via Tailscale identity. Empty means every consent goes through the admin-token form. |
| `LECTERN_OAUTH_ALLOWED_REDIRECTS` | no | Comma-separated `scheme://host` patterns (supports a leading `*.`), replacing — not merging with — the default allowlist. |
| `LECTERN_OAUTH_ADMIN_TOKEN` | no (falls back to `LECTERN_AUTH_TOKEN`) | Gates the admin-token consent fallback and the `/admin/oauth/*` client-management API, sent as `X-Lectern-Admin`. |
| `LECTERN_IDENTITY` | no | Set to `tailscale` to pre-recognise the owner at `/oauth/authorize` via Tailscale identity (see below). Unset: every consent uses the admin-token form. |
| `LECTERN_MCP_TOKEN` | no | A static bearer, independent of OAuth, for a simpler (non-connector) HTTP client. Unrestricted — sees every tool except `decide_approval`. |
| `LECTERN_API` | no (default `http://127.0.0.1:$LECTERN_PORT`) | The Lectern control-plane API this process forwards tool calls to — same variable the ordinary `lectern mcp` remote path uses. |
| `LECTERN_AUTH_TOKEN` | no | Forwarded to `LECTERN_API` as the client's own bearer, same as stdio. |

### Tailscale identity

Setting `LECTERN_IDENTITY=tailscale` reuses Lectern's own tailnet-identity
code (`internal/auth`'s `LocalAPI`/`WhoIs` — the exact mechanism
`LECTERN_AUTH=tailscale` already uses to gate the ordinary API) to look up
who is loading the consent page, via tailscaled's LocalAPI `whois` call. If
that login is in `LECTERN_OAUTH_ALLOWED_LOGINS`, the consent page shows them
as pre-identified and skips the admin-token form. Anyone else — not on the
tailnet, or on it but not allowlisted — falls back to the admin-token form,
so the feature can never be used to approve as someone it didn't verify.

## Exposing it

`lectern mcp --http` binds loopback by default and refuses to bind
off-loopback without either `LECTERN_MCP_TOKEN` or OAuth configured (the
same fail-closed rule the ordinary `lectern mcp` HTTP transport already
follows) — so getting the public listener to claude.ai needs something in
front of it. Two options, both already used elsewhere in this deployment:

**A Cloudflare tunnel** (recommended for the public side — it also gives you
rate limiting and WAF rules for `/oauth/register` and `/oauth/token`):

```
public hostname  →  http://127.0.0.1:9119   (LECTERN_MCP_ADDR)
```

Set `LECTERN_PUBLIC_BASE` to that hostname's `https://` URL.

**`tailscale serve`**, for the private consent listener — it should only
ever be reachable from the tailnet, which `tailscale serve` (not `funnel`)
guarantees by construction:

```bash
tailscale serve --https=9120 http://127.0.0.1:9120
```

Set `LECTERN_OAUTH_AUTHORIZE_BASE` to `https://<tailnet-name>:9120`. Do
**not** put the consent listener behind the same public tunnel as `/mcp` —
that would defeat the entire reason it is a second listener.

## systemd unit (example)

```ini
[Unit]
Description=Lectern MCP web connector
After=network-online.target

[Service]
Type=simple
User=lectern
ExecStart=/usr/local/bin/lectern mcp --http
Restart=on-failure
RestartSec=5
Environment=LECTERN_API=http://127.0.0.1:8420
Environment=LECTERN_AUTH_TOKEN=%d/lectern-auth-token
Environment=LECTERN_MCP_ADDR=127.0.0.1:9119
Environment=LECTERN_PUBLIC_BASE=https://lectern-mcp.example.com
Environment=LECTERN_OAUTH_AUTHORIZE_ADDR=127.0.0.1:9120
Environment=LECTERN_OAUTH_AUTHORIZE_BASE=https://lectern.tailXXXX.ts.net:9120
Environment=LECTERN_OAUTH_DB=/var/lib/lectern-mcp/oauth.db
Environment=LECTERN_OAUTH_ALLOWED_LOGINS=you@github
Environment=LECTERN_IDENTITY=tailscale
Environment=LECTERN_OAUTH_ADMIN_TOKEN=%d/lectern-oauth-admin-token

[Install]
WantedBy=multi-user.target
```

(Prefer `EnvironmentFile=` plus `systemd-creds`/a secrets directory over
inlining tokens for anything beyond a quick test — the values above are
placeholders for whatever this estate's usual secret-injection mechanism is.)

## Adding it in claude.ai

1. claude.ai → **Settings → Connectors → Add connector**.
2. **Name**: Lectern (or whatever you like).
3. **URL**: `https://<LECTERN_PUBLIC_BASE>/mcp`.
4. claude.ai performs Dynamic Client Registration against
   `/oauth/register`, then opens `LECTERN_OAUTH_AUTHORIZE_BASE +
   /oauth/authorize` in a browser tab for you to approve. If that URL is
   only reachable over the tailnet, open the connector flow from a device
   on your tailnet (or with Tailscale's MagicDNS resolving that hostname).
5. The consent page lists exactly what's being granted — `lectern.read`,
   `lectern.write`, or both (see the security model above for what each
   covers, and note `decide_approval` never appears here because it is never
   offered at all). Approve, and claude.ai redirects back with a token.

From there, ask claude.ai things like "start a Lectern session in the
librarr project that adds the two-tier cache we just designed — here's the
spec" (pasting or attaching the spec, which lands as `inline_files`), or
"tell my open session about the retry logic in this ticket" (attaching the
ticket).

## Admin: listing and revoking connected clients

`GET LECTERN_OAUTH_AUTHORIZE_BASE/admin/oauth` is a small, dependency-free
page (fetch + a table, no build step) that lists every registered client,
its redirect host, the scopes any live token actually carries, and when it
was last used, with a **Revoke** button per client. It asks for
`LECTERN_OAUTH_ADMIN_TOKEN` (kept in `sessionStorage` only, sent as
`X-Lectern-Admin`) rather than a CLI subcommand — the same shape Grimoire's
equivalent surface shipped as. Revoking a client deletes every access and
refresh token it holds but leaves its registration in place, so a
subsequent attempt lands back on the consent page rather than a confusing
registration error.

## Testing without claude.ai

The [MCP Inspector](https://github.com/modelcontextprotocol/inspector) can
drive the whole flow against `http://127.0.0.1:9119/mcp` directly —
`localhost`/`127.0.0.1` are in the default redirect allowlist for exactly
this. `internal/oauth`'s and `internal/mcp`'s own test suites
(`oauth_flow_test.go`, `http_test.go`) exercise the full DCR → consent →
token → `tools/list` → `tools/call` path, scope filtering, the
`decide_approval` exclusion, and the `files`/`inline_files` split against a
fake Lectern API, so a change here should come with a test there before
anything is deployed.
