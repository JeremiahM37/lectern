# End-to-end encrypted relay

The relay lets a phone control Lectern with no VPN, no Tailscale and no
inbound port on the Lectern host. Both Lectern and the phone connect *out* to a
relay. The relay passes opaque encrypted frames between them and can read none
of them, even when we run it.

This is different from [remote-access.md](remote-access.md), where a public
HTTPS tunnel terminates TLS in front of Lectern and so sees every request in
the clear. Pick the relay when you do not want to trust whoever runs the path
between your phone and your server.

## Design at a glance

```
 phone (PWA, installed once from Lectern)            Lectern host
 ┌───────────────────────────┐                     ┌──────────────────────────┐
 │ React app, unchanged       │                     │ API handler, unchanged    │
 │ fetch/EventSource/WebSocket│                     │   ▲ in-process listener   │
 │   shims ─► tunnel streams  │                     │   │ (principal = device)  │
 │ Noise IK initiator         │                     │ Noise IK responder        │
 └──────────┬────────────────┘                     └───────────┬──────────────┘
            │ wss, outbound                          wss, outbound │
            └──────────────►  lectern relay  ◄───────────────────────┘
                     routes ciphertext by channel + connection id
```

1. **`lectern relay`** is a subcommand of the same binary. It is a single
   process with no database: everything it needs is either derived from keys
   or re-sent by the host when it reconnects. It serves exactly two WebSocket
   endpoints and a health check, and never serves HTML or JavaScript.
2. **The host** (a normal Lectern with `LECTERN_RELAY_URL` set) keeps one
   outbound WebSocket to the relay. Each phone connection arriving through it
   runs its own Noise handshake. After that, every request the phone makes is
   fed to Lectern's *existing* HTTP handler through an in-process listener, so
   approvals, chat, SSE, terminals and voice all work unchanged. There is no
   second API.
3. **The phone** runs the ordinary Lectern PWA. A small transport layer
   replaces `fetch`, `EventSource` and `WebSocket` for Lectern's own paths and
   carries them over the encrypted tunnel. The service worker pins the app
   shell so the relay (or anyone else) cannot swap the code.

## Cryptography

Only well-reviewed primitives and one standard protocol are used:

- **Noise `Noise_IK_25519_ChaChaPoly_SHA256`** between phone and host
  (X25519, ChaCha20-Poly1305, SHA-256). Go uses
  [`github.com/flynn/noise`](https://github.com/flynn/noise). The browser
  uses the audited [noble](https://paulmillr.com/noble/) libraries
  (`@noble/curves`, `@noble/ciphers`, `@noble/hashes`) for the primitives,
  and a small Noise state machine in `frontend/src/relay/noise.ts` that is
  tested against the Go implementation.
- **Ed25519** for the host's relay routing key and the app-shell signing key.
- The Noise prologue binds every handshake to `lectern-relay-v1` and the
  channel id, so a handshake cannot be replayed onto another channel.

Why IK: the phone learns the host's static public key from the QR code, so
it can encrypt its very first message to the host and authenticate the host
without a round trip. The host learns the phone's static key inside that
first message and checks it against the list of paired devices.

Properties:

| Property | How |
|---|---|
| Relay cannot read or forge traffic | All tunnel traffic is Noise transport messages; the relay has no key. |
| Host authentication | The phone pins the host's X25519 key from the QR code shown on the host's own screen. |
| Device authentication | Each device has its own X25519 key, generated on the phone. Its private half never leaves the phone. |
| Forward secrecy | Every connection uses fresh ephemeral keys (`ee`, `se`). Stealing a static key later does not decrypt recorded sessions. The first handshake message's payload is only as forward-secret as IK allows, so it carries nothing but a version and, once, the pairing code. |
| Replay and reordering | Noise transport nonces are implicit counters. A replayed, reordered, dropped or modified frame fails to decrypt and the connection is closed. A replayed first message cannot complete a handshake without the phone's ephemeral key. |
| Revocation | Revoking a device deletes its key on the host, closes its live connections and removes its relay route. The next handshake with that key is refused. |

## Pairing

1. In **Settings → Phone & devices → Encrypted relay** the owner presses *Pair a
   phone over the relay*. This needs the same rights as deciding an approval
   (`CanDecide`), exactly like ordinary pairing.
2. Lectern mints a single-use pairing code (128 bits, 5 minutes) and a
   separate single-use relay route token, registers only the *hash* of the
   route token with the relay, and shows a QR code:

   ```
   <shell origin>/relay-pair#p=<base64url JSON>
   {"v":1,"relay":"wss://…","ch":"<channel>","hk":"<host X25519>",
    "sk":"<shell Ed25519>","c":"<pairing code>","rt":"<route token>"}
   ```

   Everything is in the URL fragment, which browsers never send to a server.
3. The phone opens that URL (see "Trusting the app code" for which origin),
   generates its own X25519 key pair, connects to the relay with the route
   token, and sends Noise message 1 with `{"pair":{"code":…,"name":…}}` as
   its encrypted payload.
4. The host checks the code (single use, unexpired), records the device's
   public key with the minting owner's identity, generates a long-lived
   route token for this device, registers its hash with the relay, and
   returns the token in Noise message 2.

The relay sees the one-time route token but never the pairing code, which
only travels inside Noise message 1 encrypted to the host. Knowing the route
token lets it open a connection, not pair a key. That separation is
deliberate: if the relay could read the code, it could race the phone and pair
itself.

## Identity and approvals

A request arriving over the tunnel is served by an in-process listener that
attaches the device's principal to the request context. `internal/auth`
honours that principal only for tunnel connections and returns before any
mode rule runs, so a tunnel request never becomes "loopback" or "no auth".
Each request re-checks that the device is still paired.

The principal is `Kind: relay-device`, with the minting owner's login and
`Human` bit, the same rule ordinary pairing uses: a phone the owner paired
*is* the owner, so it can approve. Local processes and agents gain nothing:

- They cannot create a tunnel principal. It exists only as a Go context value
  set by the in-process listener after a successful Noise handshake with a
  paired key.
- Minting a pairing code needs `CanDecide`, which a local process does not
  have (except in `LECTERN_AUTH=none`, where everything already is the owner).
- An agent that can read `lectern.db` could read the host's relay keys, but
  it could equally edit the approvals table. Filesystem access to the data
  directory is outside what the API can protect, now as before.

## Trusting the app code

End-to-end encryption means nothing if the relay can serve the JavaScript
that holds the keys. Lectern's answer has three parts.

**1. The relay never serves code.** It answers only `/v1/host`, `/v1/device`
(WebSocket upgrades) and `/healthz` (plain text). Everything else is a bare
404 with no body. The phone reaches the relay only through `connect-src`
WebSocket calls from a page it loaded elsewhere. The relay is never an origin
the phone navigates to.

**2. The shell is installed once from the host itself.** The phone opens the
pairing link on an origin that serves this Lectern's own frontend: the
Lectern host directly (its tailnet HTTPS address if the phone can reach it
once, `localhost` on a desktop), or a hostname you control that forwards to
Lectern (your own Cloudflare Tunnel hostname, `tailscale funnel`) turned on
only for the pairing. The PWA and its service worker are installed from that
origin, and after pairing the origin can go away. The phone keeps working
over the relay.

**3. The service worker pins the shell.** Pairing starts by pinning: the
phone stores the shell key from the QR code, and the service worker fetches
every file the host's signed manifest lists and checks it. If the app on
that origin is not exactly the build this Lectern signed, pairing stops
before any key is registered. From then on the service worker never uses the
network for the app shell: navigations and assets come only from its cache.
A new shell is accepted only when:

- the host's `shell-manifest.json` carries an Ed25519 signature that verifies
  against the shell key pinned at pairing (`sk` in the QR code), and
- every file's SHA-256 matches the signed manifest.

The check runs in the service worker's `install` handler, so a new worker
whose shell does not verify fails to install and the old one keeps running.

**What this does not cover.** Browsers always fetch `sw.js` itself from the
origin's network when they check for updates, and a service worker cannot
veto its own replacement. Whoever controls the *install origin* at that
moment can replace the worker. So:

- Use an origin you control for installation: the host, your own domain or
  tunnel hostname. **Do not** use a random throwaway hostname (for example a
  `trycloudflare.com` quick tunnel) that someone else could be given later.
- An install origin that is unreachable is fine: the update check fails and
  the pinned worker keeps running.
- The relay is never the install origin, so it can never swap the code. That
  is the property this feature promises.
- The [Android app](android.md) avoids the install origin altogether: its
  copy of the app is inside the signed APK.

## What the relay can still learn

The relay is blind to content, not to metadata. It can see:

- the host's IP address and the phone's IP address, and when each is online;
- the channel id (a hash of the host's routing key) and how many route
  tokens (paired devices plus pending pairings) the host registered;
- which route token each connection used, so it can tell devices apart;
- the timing, direction and size of every frame. Sizes are padded to the
  nearest 256 bytes, which blurs but does not hide them. Someone watching
  can still guess "the phone just opened a terminal" or "a large diff was
  loaded".

It can also deny service: drop, delay or cut connections. Tampering is
detected, but availability is not protected.

## What end-to-end encryption does not protect against

- A compromised **phone** (malware, an unlocked phone in someone's hand):
  the device key is in the browser's storage. Revoke it from Settings.
- A compromised **host**: Lectern holds the plaintext by design.
- A malicious **install origin** or browser extension (see above).
- Someone who photographs the pairing QR code in the five minutes it is
  valid. Pairing shows up in Settings → Phone & devices immediately; revoke anything
  you do not recognise.
- Metadata analysis, as listed above.

## Known limits

- **Shell updates.** A phone that only ever reaches Lectern through the relay
  keeps the shell it was paired with. It picks up a new version (verified as
  above) the next time it can reach its install origin. Delivering signed
  shell updates over the tunnel itself is not built yet.
- **Notification buttons.** Approve/Deny on a push notification needs a
  Lectern window open on the phone, because only a page holds the tunnel.
  With none open the notification reports that the decision failed; tapping
  it opens the app. The [Android app](android.md) does not have this limit.
- **Media and downloads** (images, video, file downloads) go through the
  service worker, which hands them to an open page to fetch over the tunnel.
- **Device key storage.** The phone's X25519 private key is kept in the
  browser's IndexedDB for that origin. Anyone who can use that browser
  profile can use the key; revoke a lost phone from Settings. The
  [Android app](android.md) keeps it in Android Keystore instead.
- **One relay URL.** `LECTERN_RELAY_URL` is also what the QR code tells the
  phone, so both sides must reach the relay at the same address.

## Relay hardening

- **Not an open proxy.** The relay never dials anything. It only forwards
  frames between a channel's authenticated host and the devices that host
  registered.
- **Host admission.** A host must prove it holds the routing key the channel
  id is derived from (Ed25519 signature over a fresh challenge) and must know
  the relay's host secret (`LECTERN_RELAY_HOST_SECRET`, sent as an HMAC of
  the challenge, never in the clear). Without the secret, strangers cannot
  use your relay as a free message bus.
- **Device admission.** A device must present a route token whose SHA-256
  the host registered for that channel within 10 seconds of connecting.
  Pairing tokens are single use and expire. Nothing is forwarded before this.
- **Limits.** Frame size cap (64 KiB), per-connection rate limit
  (256 KiB/s, burst 2 MiB), a bounded per-device send queue (a slow device is
  disconnected rather than stalling everyone), connection caps per IP, per
  channel and in total, and a per-IP connection-rate limit.

## Compared with Happy

Happy (github.com/slopus/happy, `docs/encryption.md`, `docs/api.md`) is the
prior art we read. Its server is a store as well as a relay: clients
encrypt fields (NaCl `secretbox`, or AES-256-GCM under per-session data keys
wrapped with `box`) and the server keeps the ciphertext. Pairing a terminal
works by the terminal posting a fresh public key, shown as a QR code; an
already signed-in phone encrypts the account's secret to that key and posts
it back through the server. Every device then shares one account secret.

Lectern differs where its situation differs:

- The host is the only store, so the relay keeps nothing and can be run by
  anyone, even someone you do not trust.
- Each phone has its own key, revocable on its own, instead of a shared
  account secret. Revoking one phone does not mean re-keying the others.
- Every connection runs a fresh Noise handshake, so recorded traffic stays
  unreadable even if a key leaks later. Content encrypted under one
  long-lived secret has no such property.
- Happy's web app is served by Happy. Lectern's shell comes from your own
  host and is pinned (see above).

## Setup

### 1. Run a relay

On any machine both sides can reach, typically a small VPS (this is the
owner's choice; Lectern never starts or exposes one on its own):

```sh
export LECTERN_RELAY_HOST_SECRET="$(openssl rand -hex 32)"   # keep this
lectern relay --listen 127.0.0.1:9120
```

Put TLS in front of it (Caddy, nginx, a Cloudflare Tunnel): phones need
`wss://`. The relay itself holds no state, so restarting it only makes both
sides reconnect. `lectern relay --help` lists the limits you can tune.

### 2. Point Lectern at it

In Lectern's environment:

```sh
LECTERN_RELAY_URL=wss://relay.example.com
LECTERN_RELAY_HOST_SECRET=<the same secret>
```

Restart Lectern. Settings → Phone & devices → Encrypted relay shows the connection
state and the host's key fingerprint.

### 3. Pair a phone

1. Make the Lectern web app reachable from the phone *once*, on an origin you
   control (see "Trusting the app code"). Set that origin as
   `LECTERN_RELAY_SHELL_URL` if it differs from the address you use for
   Settings.
2. Settings → Phone & devices → Encrypted relay → *Pair a phone over the relay*.
3. Scan the QR code with the phone, name the device and pair. Add the app to
   the home screen.
4. Close the tunnel if you opened one. The phone now reaches Lectern only
   through the relay.

### Revoke

Settings → Phone & devices → Encrypted relay lists every relay device with its key
fingerprint and last use. *Revoke* deletes the key, closes its connections
and removes its relay route at once. On the phone, *Forget this pairing*
deletes the local key.

## Wire format

Relay framing (host side only; devices send and receive bare Noise
messages):

| Byte 0 | Bytes 1–4 | Rest | Meaning |
|---|---|---|---|
| `1` | connection id | Noise message | data |
| `2` | connection id | route token hash (hex) | relay → host: device connected |
| `3` | connection id | – | either way: close this device connection |

Control messages (JSON text frames) carry the host challenge/response and
route registration (`routes`, `route_add`, `route_del`). A control message
with an `id` is acknowledged once applied; Lectern shows a pairing QR code
only after the relay has confirmed its route, and waits for the confirmation
when revoking. Until a (re)connected host has sent its route set, the relay
answers phones "host offline" (retried) rather than "not authorized" (which a
phone takes as revocation).

Tunnel framing (inside Noise transport messages): `[type u8][stream u32]
[payload]`, padded to 256 bytes with a trailing length. Types cover an HTTP
request head, body chunks and end; the response head, body chunks and end;
cancellation; and WebSocket open/opened/text/binary/close. See
`internal/relay/tunnel.go`.
