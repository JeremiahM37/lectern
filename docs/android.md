# Android app

A native Android app for Lectern. It is a prototype: it works end to end (see
"Tested" below) but is built from source and not published anywhere.

It runs the **same web app** as the browser (`frontend/`, staged into `web/`),
bundled and signed inside the APK, and adds what a web page cannot do:

| | Installed web app (PWA) | Android app |
|---|---|---|
| Approve / Deny / Reply on a notification | Needs a Lectern tab open when paired over the relay | Works with no Lectern screen open, over the relay or directly |
| Where the app code comes from | Your host or an install origin, then pinned by the service worker | The signed APK. No relay or install origin ever serves code |
| Relay device key | Raw bytes in the browser's IndexedDB | X25519 key in Android Keystore, not extractable |
| Push | Web Push through the browser vendor (Google for Chrome) | UnifiedPush: ntfy (self-hosted or ntfy.sh) or any distributor, no Google |
| Installing | Browser "Add to Home screen" | Install an APK |

<p>
<img src="media/android/pairing.png" width="216" alt="Pairing over the encrypted relay">
<img src="media/android/sessions.png" width="216" alt="Sessions, connected through the relay">
</p>
<p>
<img src="media/android/notification.png" width="432" alt="An approval notification with Approve, Deny and Reply">
<br>
<img src="media/android/approval-result.png" width="432" alt="The same notification after Approve, and the task ready for review">
</p>

## Build

Needs the Android SDK (platform 36, build-tools 34+) and JDK 17 or newer.

```sh
(cd frontend && npm ci && npm run build && python3 scripts/stage.py)   # only if the frontend changed
mobile/build-android.sh            # debug APK: mobile/android/app/build/outputs/apk/debug/
mobile/build-android.sh test       # JVM unit tests
LECTERN_ANDROID_SIGNING=/path/to/signing.properties mobile/build-android.sh release
```

`signing.properties` holds `storeFile`, `storePassword`, `keyAlias` and
`keyPassword`. It and the keystore stay outside the repository; without it the
release APK is unsigned. Sizes at the time of writing: release 3.3 MB, debug
8.6 MB (most of the debug size is unshrunk libraries; the web app itself is
about 7 MB uncompressed, mostly the PDF viewer).

**Signing in CI**: keep the keystore as a base64 secret, decode it to a temp
file in the job, write `signing.properties` from secrets, set
`LECTERN_ANDROID_SIGNING`, build, then delete both. Keep the same key forever:
Android refuses an update signed with a different key, and the key is what
proves an update came from you. For stronger protection, build unsigned in CI
and sign on a machine that holds the key (`apksigner sign`).

## Install and connect

1. Install the APK (`adb install app-release.apk`, or open it on the phone
   after allowing installs from your file manager).
2. Open **Lectern**. It asks for a pairing QR code or link. On your Lectern,
   open **Settings → Devices** and either:
   - **Pair a phone over the relay** (end-to-end encrypted, works from
     anywhere; see [relay.md](relay.md)), or
   - **Pair a phone** (device pairing, when the phone reaches Lectern
     directly; see [remote-access.md](remote-access.md)).
3. Tap **Scan QR code**, or paste the link. You can also paste just your
   Lectern's address (for example `https://host.tailnet.ts.net:8443`) when the
   phone is on the same tailnet: Lectern then knows it by its Tailscale
   identity and no pairing is needed.
4. Name the device and pair. The app opens Lectern.

The relay pairing page is the same `/relay-pair` page the browser uses, with
the same host key fingerprint to compare. The pairing link's own origin is
ignored: the app never loads code from it.

To connect somewhere else: **Settings → Devices → Encrypted relay → Forget this
pairing** (relay) or **Disconnect this app** (direct). The owner can revoke the
device from Settings on the host as usual.

## Push notifications (UnifiedPush and ntfy)

The app receives push through [UnifiedPush](https://unifiedpush.org), so no
Google service is involved. You need a distributor app; **ntfy** is the usual
one.

1. Install ntfy on the phone (F-Droid or Play). Open it once.
2. To use your own server instead of ntfy.sh, set it in ntfy's **Settings →
   Default server** before step 3. Lectern must be able to reach that server:
   it POSTs each notification to it.
3. In Lectern, tap **Enable phone alerts** (on the Sessions screen, or
   Settings → Notifications). Allow notifications when Android asks.

The distributor gives the app a Web Push endpoint and keys, and the app
registers them with Lectern's existing `/api/push/subscribe`, exactly like a
browser. Lectern encrypts every notification to the app's keys (RFC 8291), so
**ntfy only ever sees ciphertext**. Nothing on the host changes and nothing
needs configuring there; this is separate from Lectern's own `ntfy_topic`
setting, which sends plaintext notifications to a topic you read in the ntfy
app itself.

Notification buttons:

| Notification | Buttons | What they do |
|---|---|---|
| An approval | **Approve**, **Deny**, **Reply** | Decide the approval. Reply denies it with your text as the note, which the agent receives as its feedback |
| A session waiting for input or permission | **Reply** | Sends your text to the session |
| Anything else | (tap) | Opens that screen in the app |

A button press is handled in the background: the notification changes to
"Approving…" and then to the result ("Approved", or the error Lectern
returned). The call goes out the same way the app is connected: directly with
the paired device's own token, or through the encrypted relay. With the relay,
the app loads its own copy of the web app in an invisible WebView, so the
Noise and tunnel code are the same code the browser runs.

**Who may decide.** Nothing new is trusted. The decision reaches Lectern as
the paired device (`device` or `relay-device`), which carries the identity of
the owner who paired it, exactly like the browser. Lectern's existing rule
still applies: local processes and agents on the host cannot decide
approvals, and a revoked device gets a 401.

## How it works

- **A small Kotlin shell** around a WebView (`mobile/android`), not Capacitor.
  The app needs a handful of native pieces (Keystore, UnifiedPush, background
  work, a QR scanner) and no plugin system; a lean shell keeps the APK at
  3.3 MB, has no Google dependencies (so it can go to F-Droid) and keeps the
  whole native surface reviewable in about 1,300 lines of Kotlin.
- **The web app comes from the APK.** The build copies `web/index.html` and
  `web/static` into the APK. In relay mode the WebView uses the private origin
  `https://app.lectern.invalid`, which never touches the network. In direct
  mode the page's origin is your Lectern's address, so API calls, SSE and
  terminals are ordinary same-origin requests, but every app file is still
  answered from the APK. The service worker is not installed in the app.
- **`window.LecternNative`** (`frontend/src/native/bridge.ts`) is how the page
  reaches the native side. It refuses calls from any origin but the connected
  one, and the WebView opens every other link in the phone's browser.
- **Keys.** The relay device key is generated in Android Keystore (Android 13
  and later) and never leaves it; the page gets its public key and a DH
  function. On older Android, or a Keystore without X25519, the app uses a
  software key sealed with a Keystore AES key. The route token and a direct
  device token are sealed with that AES key too. Nothing is included in
  backups.
- **Direct-mode credential.** A device paired directly gets the usual device
  token. The app stores it sealed, gives it to the WebView as a session cookie
  at each start, and uses it as a bearer token for notification buttons.

## Tested

In an Android 14 emulator against an isolated Lectern (its own port, database,
home and tmux directory, token auth, mock agents), a local `lectern relay`,
and a local ntfy server, with the app's process killed before each push:

- Pairing over the relay: the device key was created in Keystore
  (`LecternNative.version()` reports `"key":"keystore"`), `/api/whoami` over
  the tunnel is `relay-device`, human.
- Direct device pairing over `http://10.0.2.2`: `/api/whoami` is `device`,
  human; afterwards the token is an HttpOnly cookie, not readable by page
  scripts (the pairing page itself sees it once, as in a browser).
- UnifiedPush registration through ntfy; the message ntfy stored was
  ciphertext.
- A real gated approval (`[mock:approval]` task): the notification arrived
  with the app not running; **Approve**, **Deny** and **Reply** each decided
  it through the relay (`decided_by=user`, the Reply text as the note) and the
  agent continued or stopped accordingly. **Approve** also worked directly.
- The same approval round trip with the signed, minified release APK.

To re-run it: boot an emulator, install ntfy and the APK, point ntfy's
default server at `http://127.0.0.1:19281`, then use the scripts in
`mobile/android/e2e/` (`start-stack.sh`, `pair-relay.sh`, `approve-flow.sh
<label> Approve|Deny|Reply [text]`), with `adb reverse` for ports 19210,
19281 and 19282 so the emulator and host share the same addresses. They
drive the phone through `adb` and `uiautomator`; `pair-relay.sh` taps the pair
button by screen position (Pixel 7 profile).

Not tested end to end: **Reply** on a waiting session notification (unit
tested only), the QR camera scanner (the emulator has no real camera; links
were pasted), physical devices, and Android versions other than 14.

## Limits

- **Web app updates arrive with the APK.** The app runs the web app it was
  built with. A host much newer or older than the app can differ in API; build
  the APK from the same commit as the host.
- **Media over the relay.** Images, video and downloads that the web app loads
  by URL rather than `fetch` (the browser's service worker handles those) do
  not load in relay mode yet.
- **A UnifiedPush distributor is required** for notifications. If none is
  installed, Enable phone alerts says so.
- **Keystore strength varies.** On the emulator the key is in software
  Keystore; on phones with a TEE or StrongBox it is in hardware. Anyone who
  can use the unlocked phone can use the app; revoke a lost phone from
  Settings.
- **One Lectern per install.**
- **Plain http is allowed**, because the address is yours to choose (a
  tailnet address is already encrypted). Use https or the relay on untrusted
  networks.

## Publishing (not done)

Publishing is the owner's decision; nothing has been uploaded anywhere.

- **F-Droid**: the app has no proprietary dependencies (UnifiedPush connector,
  Tink, ZXing, AndroidX). F-Droid builds from source, so it needs a tagged
  release, fastlane metadata (`fastlane/metadata/android/…`: descriptions,
  screenshots, changelogs), a merge request to `fdroiddata`, and a build recipe
  that runs the frontend build first (or relies on the committed `web/`).
  Reproducible builds would let F-Droid ship the APK signed with your key.
- **Google Play**: a developer account, an app bundle (`bundleRelease`) signed
  with an upload key (Play App Signing), a privacy policy, the Data safety
  form, and a target SDK within Play's current requirement. UnifiedPush works
  on Play, but Play users may expect FCM; the connector can embed an FCM
  distributor if that is ever wanted.
- **iOS**: UnifiedPush does not exist there; notifications must go through
  APNs, which needs an Apple developer account and a small push gateway (the
  host cannot send to APNs without Apple credentials). Notification buttons
  would use a Notification Service/Content extension plus background URLSession
  work. The same shell design carries over (WKWebView serving the bundled web
  app from a custom scheme, Keychain/Secure Enclave for keys, although the
  Secure Enclave has no X25519, so the relay key would be sealed rather than
  hardware-held). Distribution is via the App Store or TestFlight only.
