// "Connect your phone" (docs/design/simple-ui.md): which address a phone
// should use to reach Lectern. The server lists what it can offer
// (GET /api/phone/addresses); this adds the address the browser is already
// on when that is not this computer's own loopback, and guarantees no
// loopback address is ever turned into a QR code — a phone cannot reach
// 127.0.0.1 of the computer showing it.

export type PhoneKind = "current" | "tailnet" | "lan" | "relay";

export interface PhoneOption {
  kind: PhoneKind;
  url?: string;
  available: boolean;
  reason?: string;
  secure?: boolean;
}

export interface PhoneAddresses {
  can_enable_wifi?: boolean;
  listening: string;
  loopback_only: boolean;
  options: PhoneOption[];
}

export function isLoopbackURL(url: string): boolean {
  let host = "";
  try {
    host = new URL(url).hostname;
  } catch {
    return false;
  }
  host = host.replace(/^\[|\]$/g, "");
  return host === "localhost" || host.endsWith(".localhost") || host === "::1" || /^127\./.test(host) || host === "0.0.0.0";
}

/**
 * The choices to show, best first. `origin` is where this browser is (for
 * "the address you are using now"). Loopback URLs are never available.
 */
export function phoneChoices(addresses: PhoneAddresses | undefined, origin: string): PhoneOption[] {
  const out: PhoneOption[] = [];
  const current = origin && !isLoopbackURL(origin) ? origin.replace(/\/+$/, "") : "";
  if (current) out.push({ kind: "current", url: current, available: true, secure: current.startsWith("https:") });
  for (const option of addresses?.options || []) {
    if (option.url && current && option.url.replace(/\/+$/, "") === current) continue;
    const loopback = !!option.url && isLoopbackURL(option.url);
    out.push(loopback ? { ...option, available: false, reason: option.reason || "loopback_only" } : option);
  }
  return out;
}

/** The first choice that can work, if any. */
export function bestChoice(choices: PhoneOption[]): PhoneOption | undefined {
  return choices.find((row) => row.available);
}

/** The pairing link a QR code carries for a direct (non-relay) address. */
export function directPairURL(base: string, code: string): string {
  return `${base.replace(/\/+$/, "")}/pair#code=${encodeURIComponent(code)}`;
}
