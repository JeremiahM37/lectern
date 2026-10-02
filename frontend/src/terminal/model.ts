import type { ITheme } from "@xterm/xterm";
import { ApiError, authToken, withToken } from "../api/client";
import { builtinTerminalThemes } from "../theme/terminal-themes";
import { t } from "../i18n";
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
  // The pane is a full-screen program without mouse reporting (Claude Code
  // with its mouse turned off): only its own PageUp/PageDown scroll it.
  app_screen?: boolean;
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
// Built-in schemes by id; the full list, including imported ones, is in
// theme/terminal-themes.ts.
export const themes: Record<string, ITheme> = Object.fromEntries(builtinTerminalThemes.map((row) => [row.id, row.theme]));
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
      theme: typeof value.theme === "string" && value.theme ? value.theme.slice(0, 60) : "slate",
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
/** A workspace or outside path as shell text for the agent's prompt: relative
 * paths are made absolute, and ~/ stays unquoted so the shell expands it. */
export function shellPath(workdir: string, path: string) {
  if (path.startsWith("~/")) return "~/" + quote(path.slice(2));
  return quote(path.startsWith("/") ? path : workdir.replace(/\/+$/, "") + "/" + path);
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
    throw new Error(t("terminalPage.errors.clipboard"));
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
