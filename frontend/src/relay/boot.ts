// Routes this page's traffic to Lectern through the encrypted relay when the
// device is paired over it. Imported first by every entry point, so the
// replacements are in place before any module makes a request.
//
// Only this Lectern's own paths (/api, /term, /a2a on the page's origin) go
// through the tunnel; anything else keeps the browser's own fetch,
// EventSource and WebSocket. The React app is unchanged: it keeps calling
// fetch('/api/...') and new EventSource('/api/stream').
import { RelayTunnel, TunnelEventSource, TunnelWebSocket } from "./tunnel";
import { loadPairing, preferDirect, relayFlagged } from "./store";
import { tunnelled } from "./paths";

declare global {
  interface Window {
    __lecternRelay?: RelayTunnel;
  }
}

// A terminal tab is a same-origin iframe: it shares the parent page's tunnel
// rather than opening a second relay connection and handshake.
function parentTunnel(): RelayTunnel | undefined {
  try {
    if (window.parent !== window) return window.parent.__lecternRelay;
  } catch { /* cross-origin parent */ }
  return undefined;
}

function install(tunnel: RelayTunnel) {
  const nativeFetch = window.fetch.bind(window);
  window.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = input instanceof Request ? input.url : input;
    return tunnelled(url) ? tunnel.fetch(input, init) : nativeFetch(input, init);
  }) as typeof fetch;

  const NativeEventSource = window.EventSource;
  window.EventSource = function (url: string | URL, init?: EventSourceInit) {
    return tunnelled(url) ? new TunnelEventSource(tunnel, url) : new NativeEventSource(url, init);
  } as unknown as typeof EventSource;
  Object.assign(window.EventSource, { CONNECTING: 0, OPEN: 1, CLOSED: 2 });

  const NativeWebSocket = window.WebSocket;
  window.WebSocket = function (url: string | URL, protocols?: string | string[]) {
    return tunnelled(url) ? new TunnelWebSocket(tunnel, String(url), protocols) : new NativeWebSocket(url, protocols);
  } as unknown as typeof WebSocket;
  Object.assign(window.WebSocket, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 });
}

// Media elements, downloads and other requests the page does not make through
// fetch reach the service worker, which asks an open page to fetch them over
// the tunnel (service-worker.ts relayFetch). A page not using the relay says
// so, and the worker goes to the network itself.
function answerWorker(tunnel: RelayTunnel | undefined) {
  navigator.serviceWorker?.addEventListener("message", (event) => {
    const msg = event.data as { type?: string; url?: string; method?: string; headers?: [string, string][]; body?: string } | undefined;
    const port = event.ports[0];
    if (msg?.type !== "lec-relay-fetch" || !port || !msg.url) return;
    if (!tunnel || !tunnelled(msg.url)) {
      port.postMessage({ type: "direct" });
      return;
    }
    void (async () => {
      try {
        const response = await tunnel.fetch(msg.url!, { method: msg.method || "GET", headers: msg.headers, body: msg.body });
        const headers: [string, string][] = [];
        response.headers.forEach((v, k) => headers.push([k, v]));
        port.postMessage({ type: "head", status: response.status, headers });
        if (response.body) {
          const reader = response.body.getReader();
          for (;;) {
            const { value, done } = await reader.read();
            if (done) break;
            port.postMessage({ type: "chunk", data: value.buffer }, [value.buffer]);
          }
        }
        port.postMessage({ type: "end" });
      } catch (err) {
        port.postMessage({ type: "error", message: String(err) });
      }
    })();
  });
}

/** Starts the relay transport if this device is paired and has not chosen a
 * direct connection. Returns the tunnel, or undefined when not in use. */
export function bootRelay(): RelayTunnel | undefined {
  if (!relayFlagged() || preferDirect()) {
    answerWorker(undefined);
    return undefined;
  }
  let tunnel = parentTunnel();
  if (!tunnel) {
    tunnel = new RelayTunnel(loadPairing);
    void tunnel.start();
  }
  window.__lecternRelay = tunnel;
  install(tunnel);
  answerWorker(tunnel);
  return tunnel;
}

export const relayTunnel = bootRelay();
