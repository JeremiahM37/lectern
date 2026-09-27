// Which requests the relay transport carries: only this Lectern's own API,
// terminal and A2A paths on the page's own origin.
const TUNNELLED = /^\/(api|term|a2a)(\/|$)/;

export function tunnelled(raw: string | URL, base = location.href): boolean {
  let u: URL;
  try {
    u = new URL(String(raw), base);
  } catch {
    return false;
  }
  const page = new URL(base);
  const sameHost = u.host === page.host && /^(https?|wss?):$/.test(u.protocol);
  return sameHost && TUNNELLED.test(u.pathname);
}
