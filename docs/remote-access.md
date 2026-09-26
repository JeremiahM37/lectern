# Remote access without Tailscale — device pairing behind a public tunnel

Lectern's normal story for "reach my own control plane from my phone" is
Tailscale: `internal/auth` resolves a caller's identity from tailscaled's
LocalAPI, so an owner on the tailnet never logs in at all. That works well
until the phone genuinely cannot run Tailscale — a work-managed device, a
borrowed one, a network that blocks it. This document is for that case: how
to put Lectern behind an ordinary public HTTPS tunnel and still keep it
locked down to the one person who owns it.

We looked at Happy's approach first (github.com/slopus/happy,
`docs/encryption.md`): a phone talks to Claude Code/Codex through an
end-to-end-encrypted relay it does not own, so the relay operator is
untrusted by design. That threat model does not apply here — **Lectern is
already the owner's own server**; the thing a public tunnel adds is exposure
to the open internet, not a relay operator to distrust. Full E2E encryption
would protect against a threat (a malicious relay) that does not exist in
this deployment. The simpler, correct-for-this-case answer is **device
pairing**: an already-authenticated owner mints a short-lived, single-use
code; a new device exchanges it once for its own long-lived, individually
revocable credential. See `internal/pairing`'s package doc for the
implementation notes.

## Setting it up

1. **Turn on device pairing.** Either set `LECTERN_DEVICE_PAIRING=1` in
   Lectern's environment, or flip the toggle in Settings → Devices — either
   one turns it on; the env var cannot be overridden off from the UI (see
   `pairing.Enabled`). It is **off by default**: enabling it is a deliberate
   choice, not something a bare upgrade turns on for you.
2. **Pick your tunnel.** Two options that need no port-forward and no static
   LAN IP:
   - **Cloudflare Tunnel** (`cloudflared`): run it on the same host as
     Lectern, pointed at Lectern's normal HTTP port (e.g.
     `cloudflared tunnel --url http://127.0.0.1:9110`, or a named tunnel with
     an `ingress` rule to the same address). Cloudflare terminates TLS at its
     edge and forwards to `cloudflared`, which forwards to Lectern over
     plain HTTP on loopback.
   - **`tailscale funnel`**: if this node already has Tailscale (most do —
     pairing is for the *phone* that doesn't, not necessarily the server),
     `tailscale funnel 9110` exposes the same port publicly with a real
     Let's Encrypt certificate, no second daemon needed.
3. **Set `LECTERN_AUTH` explicitly — do not leave it on `auto`/`tailscale`.**
   See "The loopback trap" below for why. `LECTERN_AUTH=token` with a strong
   `LECTERN_AUTH_TOKEN` is the recommended baseline: treat that token as an
   **admin bootstrap secret**, used once to mint the first pairing code from
   a browser or `curl`, never handed to the phone itself. Pairing is the
   phone's credential from then on.
4. Open `https://<your-tunnel-hostname>/pair` on the phone (or scan the QR
   code minted in Settings → Devices — see below), pair it, and use Lectern
   there exactly as you would over Tailscale.

## The loopback trap (and how Lectern closes it)

`internal/auth` trusts a request from **loopback** as "a process on this
machine" (the CLI, the MCP server, a dispatched agent): `KindLocal` in
`tailscale` mode, and the owner in `none` mode. A tunnel that forwards to
`127.0.0.1` (Cloudflare Tunnel's default, Tailscale Funnel) opens that
loopback connection on behalf of **every internet visitor**, so without a
guard they would all inherit that trust.

Lectern refuses loopback requests that show they were relayed from
outside, in every mode. That covers any request carrying
`Cf-Connecting-Ip`/`Cf-Ray` (Cloudflare) or `Tailscale-Funnel-Request`, or a
`X-Forwarded-For`/`X-Real-Ip`/`Forwarded` client address that is neither
loopback nor a tailnet IP. Such a request gets in only with a credential: the
static token or a paired device. Plain local callers, and `tailscale serve`
relaying a tailnet client, behave as before. The rules and their tests live in
`internal/auth` (`cameFromOutside`, `tunnel_test.go`).

Still set `LECTERN_AUTH=token` with a strong `LECTERN_AUTH_TOKEN` when you
expose Lectern. The guard depends on the tunnel sending a forwarding header,
and every mainstream one does. A bespoke proxy that strips all of them would
defeat it, and token mode does not extend loopback trust at all.

## Security model

With pairing on and `LECTERN_AUTH=token` set, here is exactly what an
anonymous attacker on the public internet can reach:

- **`GET /pair`** — a static page. It renders a form; nothing behind it is
  reachable without a code.
- **Static assets** (`/style.css`, icons, the manifest, …) — no different
  from any public web page.
- **`POST /api/pair/exchange`** — the one unauthenticated *write*. It accepts
  a code and a device name. Guessing a valid code means guessing 128 bits of
  randomness (`internal/pairing.CodeTTL` gives an attacker 5 minutes per
  code, and `internal/pairing.RateLimiter` caps attempts at 10/minute per IP
  and 30/minute globally, with a 5-minute lockout on top of that once
  tripped) — not a realistic attack. A wrong, reused, or expired code all get
  the same generic "invalid or expired code" response, so this endpoint is
  not an oracle for narrowing down a real one.
- **Nothing else.** Every other `/api/*` route and every `/term/*` route
  requires either the static token or a valid paired-device credential (a
  cookie or bearer token that hashes to a row in `pairing_devices`). Both are
  opaque, high-entropy secrets stored **hashed** — the database never holds a
  value that authenticates anyone on its own (matching `internal/oauth`'s
  existing convention).

**What a paired device can do once paired: everything the owner's browser
can**, including deciding approvals. This is deliberate, not an oversight —
see `internal/auth`'s `KindDevice` doc comment. A paired phone IS the owner,
reachable without Tailscale; it is not a second, lesser account. That is why
minting a code requires `CanDecide` (the same gate an approval decision
itself requires) and why revoking a device is instant and final
(`DELETE /api/pair/devices/{id}`, checked on every subsequent request via
`internal/pairing.Store.LookupDevice`).

**Trade-offs worth knowing about, not hidden:**
- A device token idles out after `LECTERN_DEVICE_PAIRING`'s configured
  `idle_days` (default 30, editable in Settings → Devices) of no requests —
  but the browser cookie itself is set with a ~400-day `Max-Age` (the
  practical ceiling most browsers enforce), so the cookie's own lifetime is
  never the limiting factor; the server-side idle check, refreshed on every
  authenticated request, is the real control.
- CSRF protection (`SameSite=Strict` plus an Origin/Referer check on every
  non-GET request authenticated by the device *cookie*) does not apply to a
  device token presented as an `Authorization: Bearer` header — that is a
  deliberate scope match to the actual risk (a browser tricked into sending a
  cookie it holds), not an oversight; a script that already has the raw
  bearer value out-of-band is not the CSRF scenario.
- Settings → Devices shows a hint when the request reaching it arrived over
  neither loopback nor a tailnet address while pairing is off
  (`GET /api/pair/settings`'s `untrusted_origin_hint`) — but this cannot
  detect a tunnel that forwards to loopback (see "The loopback trap" above);
  it is a nudge for the cases it *can* see, not a complete guarantee.
- The self-signed wildcard cert Lectern uses for its own `*.homelab.internal`
  vhosts is irrelevant here: a public tunnel (Cloudflare, `tailscale funnel`)
  terminates TLS with its own trusted certificate, so the phone never has to
  trust anything self-signed.

## PWA and push notifications

A paired phone can install the PWA from the public tunnel origin exactly as
it would from a tailnet HTTPS origin — service worker registration and the
Web Share Target both only require a secure context (`https://`), which the
tunnel provides. Push notification eligibility (`frontend/src/push.ts`,
`pushAvailability`) checks browser feature support and secure-context status
only — it never checks or assumes a tailnet identity, so "Enable phone
alerts" works the same way for a paired device as it does for a tailnet
browser.

## Do not expose Lectern without pairing or auth

To say the obvious part plainly: putting a tunnel in front of Lectern with
`LECTERN_AUTH=none` (or `auto`, which quietly resolves to `none` off a
loopback listener) and pairing **off** hands unauthenticated control of every
session, task, and approval on this box to the entire internet. Pairing is
the thing that makes a public tunnel survivable; skipping it is not a smaller
version of this feature, it is not having it.
