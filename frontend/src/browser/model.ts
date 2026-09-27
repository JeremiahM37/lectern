// The Browser pane's pure logic, kept apart from React so it can be tested:
// device presets, mapping a click on the streamed picture back into the page,
// Design Mode's selection list and the messages the picker may send.
import { t } from "../i18n";

export interface Viewport {
  width: number;
  height: number;
  mobile: boolean;
  scale: number;
}

export interface Device {
  id: "phone" | "tablet" | "desktop";
  label: string;
  viewport: Viewport;
}

// Labels are getters so they are looked up in the language shown when read.
export const DEVICES: Device[] = [
  { id: "phone", get label() { return t("browser.device.phone"); }, viewport: { width: 390, height: 844, mobile: true, scale: 2 } },
  { id: "tablet", get label() { return t("browser.device.tablet"); }, viewport: { width: 820, height: 1180, mobile: true, scale: 2 } },
  { id: "desktop", get label() { return t("browser.device.desktop"); }, viewport: { width: 1280, height: 800, mobile: false, scale: 1 } },
];

export function device(id: string): Device {
  return DEVICES.find((d) => d.id === id) ?? (DEVICES[2] as Device);
}

export interface Box {
  left: number;
  top: number;
  width: number;
  height: number;
}

/** Where a picture of aspect `vw:vh` sits inside a box with object-fit: contain. */
export function containedBox(box: Box, vw: number, vh: number): Box {
  if (!vw || !vh || !box.width || !box.height) return box;
  const scale = Math.min(box.width / vw, box.height / vh);
  const width = vw * scale,
    height = vh * scale;
  return { left: box.left + (box.width - width) / 2, top: box.top + (box.height - height) / 2, width, height };
}

/** A pointer position on the picture, as CSS pixels in the page; null outside it. */
export function mapPoint(clientX: number, clientY: number, box: Box, viewport: { width: number; height: number }) {
  const shown = containedBox(box, viewport.width, viewport.height);
  const x = ((clientX - shown.left) / shown.width) * viewport.width;
  const y = ((clientY - shown.top) / shown.height) * viewport.height;
  if (!(x >= 0 && y >= 0 && x <= viewport.width && y <= viewport.height)) return null;
  return { x: Math.round(x * 10) / 10, y: Math.round(y * 10) / 10 };
}

/** DevTools' modifier bits: Alt 1, Ctrl 2, Meta 4, Shift 8. */
export function modifiers(e: { altKey: boolean; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean }) {
  return (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0);
}

/** The picker's description of one element (internal/browser/picker.js). */
export interface DesignElement {
  selector: string;
  breadcrumb: string;
  tag: string;
  text: string;
  html: string;
  html_truncated: boolean;
  html_length: number;
  css: Record<string, string>;
  rules: string[];
  /** Where a dev build says the element comes from, when it says. */
  source?: { file: string; line?: number; column?: number; via: string } | null;
  context_html?: string;
  rect: { x: number; y: number; width: number; height: number };
  scroll: { x: number; y: number };
  viewport: { width: number; height: number; dpr: number };
  url: string;
  title: string;
  client_png?: string;
}

export const MAX_SELECTION = 8;

/** A click replaces the selection; Shift (or multi-select) adds, or removes one already picked. */
export function mergeSelection(list: DesignElement[], el: DesignElement, additive: boolean): DesignElement[] {
  if (!additive) return [el];
  const at = list.findIndex((x) => x.selector === el.selector);
  if (at >= 0) return list.filter((_, i) => i !== at);
  return [...list, el].slice(-MAX_SELECTION);
}

export type PickerMessage =
  | { type: "hover"; breadcrumb: string }
  | { type: "select"; additive: boolean; element: DesignElement }
  | { type: "cancel" }
  | { type: "capture"; id: string; png?: string; error?: string };

function isElement(v: unknown): v is DesignElement {
  if (!v || typeof v !== "object") return false;
  const e = v as Record<string, unknown>;
  return typeof e.selector === "string" && e.selector.length > 0 && e.selector.length < 2000 &&
    typeof e.html === "string" && typeof e.url === "string" && !!e.rect && typeof e.rect === "object" &&
    !!e.viewport && typeof e.viewport === "object";
}

/** Reads a picker message, or null for anything else: only the picker's own
 * shapes are accepted, and only as data. */
export function readPicker(raw: unknown): PickerMessage | null {
  if (!raw || typeof raw !== "object") return null;
  const m = raw as Record<string, unknown>;
  switch (m.type) {
    case "hover":
      return typeof m.breadcrumb === "string" ? { type: "hover", breadcrumb: m.breadcrumb.slice(0, 400) } : null;
    case "select":
      return isElement(m.element) ? { type: "select", additive: m.additive === true, element: m.element } : null;
    case "cancel":
      return { type: "cancel" };
    case "capture":
      if (typeof m.id !== "string") return null;
      return {
        type: "capture",
        id: m.id,
        png: typeof m.png === "string" && m.png.startsWith("data:image/png;base64,") ? m.png : undefined,
        error: typeof m.error === "string" ? m.error : undefined,
      };
  }
  return null;
}

/** A message from the framed dev server is the picker's only when it comes
 * from that frame's window and that view's own origin. */
export function fromFrame(event: { origin: string; source: unknown; data: unknown }, frame: unknown, origin: string) {
  if (!frame || event.source !== frame || event.origin !== origin) return null;
  const data = event.data as { lecternDesign?: unknown } | null;
  return data && typeof data === "object" ? readPicker(data.lecternDesign) : null;
}

export interface ViewInfo {
  id: string;
  port: number;
  listen_port: number;
  design: boolean;
  tls: boolean;
  ticket?: string;
  ticket_param?: string;
}

/** The address a view is framed at: this page's own host, the view's port,
 * and https only when the view speaks it. */
export function viewOrigin(here: { protocol: string; hostname: string }, view: ViewInfo) {
  const host = here.hostname.includes(":") ? `[${here.hostname}]` : here.hostname;
  const scheme = here.protocol === "https:" && view.tls ? "https:" : "http:";
  return `${scheme}//${host}:${view.listen_port}`;
}

/** Whether the view can be framed here: a secure page may not frame plain http. */
export function viewFramable(here: { protocol: string }, view: ViewInfo) {
  return here.protocol !== "https:" || view.tls;
}

export function viewURL(here: { protocol: string; hostname: string }, view: ViewInfo, path = "/") {
  const u = new URL(path.startsWith("/") ? path : "/" + path, viewOrigin(here, view));
  if (view.ticket) u.searchParams.set(view.ticket_param || "__lectern_ticket", view.ticket);
  return u.toString();
}

/** The path and query of a target-local address, for the same page in a view. */
export function localPath(address: string) {
  try {
    const u = new URL(address);
    return u.pathname + u.search + u.hash;
  } catch {
    return "/";
  }
}

export function loopbackPort(address: string): number | null {
  try {
    const u = new URL(address);
    if (!["localhost", "127.0.0.1", "[::1]"].includes(u.hostname)) return null;
    return Number(u.port) || (u.protocol === "https:" ? 443 : 80);
  } catch {
    return null;
  }
}

/** What someone types in the address bar, as an address. */
export function normalizeAddress(raw: string): string {
  const v = raw.trim();
  if (!v) return v;
  if (/^\d{2,5}$/.test(v)) return `http://localhost:${v}/`;
  if (/^:\d{2,5}(\/.*)?$/.test(v)) return `http://localhost${v}`;
  if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(v)) return `http://${v}`;
  return v;
}
