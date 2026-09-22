import type { ITheme } from "@xterm/xterm";
import { ApiError, authToken, withToken } from "../api/client";
export { withToken };
export interface TerminalInfo {
  kind: string;
  id: string;
  tmux_session: string;
  target: string;
  workdir: string;
  terminal_url: string;
  shell_url: string;
  files_available: boolean;
  desktop_uri: string;
  desktop_command: string;
  attach_argv: string[];
}
// A phone and a desk want different type. 15px on a 390px screen is 40 columns,
// and an agent's interface is unreadable wrapped that tight; the same 11px that
// gives a phone 65 columns is too small to work at on a monitor.
export const FONT_MIN = 7,
  FONT_MAX = 30,
  MOBILE_FONT_DEFAULT = 11;
export interface Prefs {
  fontSize: number;
  mobileFontSize: number;
  lineHeight: number;
  theme: string;
}
export interface History {
  text: string;
  truncated: boolean;
  limit_lines: number;
}
export interface FileEntry {
  name: string;
  path: string;
  directory: boolean;
  size: number;
}
export interface FileListing {
  path: string;
  entries: FileEntry[];
}
export interface Attachment {
  name: string;
  path: string;
}
export const themes: Record<string, ITheme> = {
  slate: {
    background: "#10121c",
    foreground: "#e0e4f0",
    cursor: "#b6a4ff",
    selectionBackground: "#66578a",
  },
  black: { background: "#000000", foreground: "#e9e9e9", cursor: "#ffffff" },
  light: {
    background: "#f6f4ee",
    foreground: "#202334",
    cursor: "#453575",
    selectionBackground: "#c7b9ee",
  },
};
export function loadPrefs(): Prefs {
  try {
    const value: Partial<Prefs> = JSON.parse(
      localStorage.getItem("lec-terminal-prefs") || "{}",
    );
    return {
      fontSize: Math.max(10, Math.min(FONT_MAX, Number(value.fontSize) || 15)),
      mobileFontSize: Math.max(
        FONT_MIN,
        Math.min(FONT_MAX, Number(value.mobileFontSize) || MOBILE_FONT_DEFAULT),
      ),
      lineHeight: [1, 1.15, 1.3].includes(Number(value.lineHeight))
        ? Number(value.lineHeight)
        : 1.15,
      theme: value.theme && themes[value.theme] ? value.theme : "slate",
    };
  } catch {
    return {
      fontSize: 15,
      mobileFontSize: MOBILE_FONT_DEFAULT,
      lineHeight: 1.15,
      theme: "slate",
    };
  }
}
export function quote(path: string) {
  return "'" + path.replaceAll("'", "'\\''") + "'";
}
export async function request(
  url: string,
  options: RequestInit = {},
): Promise<Response> {
  const headers = new Headers(options.headers),
    token = authToken();
  if (token) headers.set("Authorization", "Bearer " + token);
  const response = await fetch(url, { ...options, headers });
  if (!response.ok) {
    let message = response.statusText || `HTTP ${response.status}`;
    try {
      const data: unknown = await response.json();
      if (
        data &&
        typeof data === "object" &&
        "detail" in data &&
        typeof data.detail === "string"
      )
        message = data.detail;
    } catch {}
    throw new ApiError(response.status, message);
  }
  return response;
}
export async function json<T>(url: string, options?: RequestInit): Promise<T> {
  return (await request(url, options)).json() as Promise<T>;
}
export async function copyClipboard(text: string, restoreFocus?: () => void) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {}
  }
  const previous = document.activeElement,
    textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.readOnly = true;
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.append(textarea);
  textarea.select();
  let copied = false;
  try {
    copied = document.execCommand("copy");
  } finally {
    textarea.remove();
    if (restoreFocus) restoreFocus();
    else if (previous instanceof HTMLElement)
      previous.focus({ preventScroll: true });
  }
  if (!copied)
    throw new Error(
      "Clipboard access is unavailable. Use your browser's Copy command.",
    );
}
export function downloadBlob(blob: Blob, name: string) {
  const link = document.createElement("a"),
    url = URL.createObjectURL(blob);
  link.href = url;
  link.download = name;
  link.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 30000);
}

export const errorMessage = (error: unknown) =>
  error instanceof Error ? error.message : String(error);
