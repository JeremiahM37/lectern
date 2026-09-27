// Pairing links as the Android app understands them (docs/android.md):
//   lectern://pair?p=<relay payload>            same payload as /relay-pair#p=
//   lectern://pair?origin=<https origin>&code=  same code as /pair#code=
// The https links stay the canonical ones (a camera app, a browser and the
// installed web app all open them); these open the app straight to its
// pairing screen, where the person still confirms before anything happens.

export const ANDROID_PACKAGE = "io.github.jeremiahm37.lectern";

export function appPairLink(link: string): string | undefined {
  let u: URL;
  try {
    u = new URL(link);
  } catch {
    return undefined;
  }
  if (u.pathname.endsWith("/relay-pair")) {
    const p = /[#&]p=([A-Za-z0-9_-]+)/.exec(u.hash)?.[1];
    return p ? `lectern://pair?p=${p}` : undefined;
  }
  if (u.pathname.endsWith("/pair")) {
    const code = /[#&]code=([A-Za-z0-9%-]+)/.exec(u.hash)?.[1];
    return code ? `lectern://pair?origin=${encodeURIComponent(u.origin)}&code=${code}` : undefined;
  }
  return undefined;
}

/** A Chrome-on-Android intent link: opens the app when installed, and
 * otherwise stays on (or returns to) the https page. */
export function androidIntentLink(link: string): string | undefined {
  const app = appPairLink(link);
  if (!app) return undefined;
  const rest = app.slice("lectern://".length);
  return `intent://${rest}#Intent;scheme=lectern;package=${ANDROID_PACKAGE};S.browser_fallback_url=${encodeURIComponent(link)};end`;
}

export function onAndroid(userAgent: string): boolean {
  return /Android/i.test(userAgent);
}
