import { useEffect, useState } from "react";
import type { SettingsApi } from "./Settings";
// connect-tools.css is imported by main.tsx and settings/harness.tsx, not
// here: this module has its own node:test unit tests (ConnectTools.test.ts,
// pulled in transitively by Settings.test.ts too), and tsx's plain Node
// runtime has no loader for a bare .css import.

export interface MCPClientLastSeen {
  name: string;
  version: string;
  at: number; // unix seconds
}

export interface MCPClientInfo {
  id: string;
  name: string;
  installed: boolean | null;
  detail: string;
  can_install: boolean;
  lectern_path?: string;
  command?: string;
  remote_command?: string;
  external_url?: string;
  last_seen?: MCPClientLastSeen | null;
}

// LECTERN_API_PLACEHOLDER is what the backend leaves inside a "remote" command
// string in place of an origin it cannot reliably know (see
// internal/api/mcp_clients.go's mcpAPIPlaceholder) — the browser is the only
// thing that knows its own origin.
export const LECTERN_API_PLACEHOLDER = "__LECTERN_API__";

export function fillOrigin(template: string, origin: string): string {
  return template.split(LECTERN_API_PLACEHOLDER).join(origin);
}

function serverSpec(lecternPath: string, remote: boolean, origin: string): Record<string, unknown> {
  return remote
    ? { command: "lectern", args: ["mcp"], env: { LECTERN_API: origin } }
    : { command: lecternPath, args: ["mcp"] };
}

export function claudeDesktopConfig(lecternPath: string, remote: boolean, origin: string): string {
  return JSON.stringify({ mcpServers: { lectern: serverSpec(lecternPath, remote, origin) } }, null, 2);
}

export function cursorDeepLink(lecternPath: string, remote: boolean, origin: string): string {
  const encoded = btoa(JSON.stringify(serverSpec(lecternPath, remote, origin)));
  return `cursor://anysphere.cursor-deeplink/mcp/install?name=lectern&config=${encoded}`;
}

export function vscodeDeepLink(lecternPath: string, remote: boolean, origin: string): string {
  const config = { name: "lectern", ...serverSpec(lecternPath, remote, origin) };
  return `vscode:mcp/install?${encodeURIComponent(JSON.stringify(config))}`;
}

// formatAgo renders a last-seen unix-seconds timestamp the way the card wants
// it: "used 2 min ago", falling back to coarser units as the gap grows.
export function formatAgo(atSeconds: number, nowMs: number = Date.now()): string {
  const deltaSeconds = Math.max(0, Math.round(nowMs / 1000 - atSeconds));
  if (deltaSeconds < 45) return "just now";
  const minutes = Math.round(deltaSeconds / 60);
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  return `${days}d ago`;
}

export function statusLine(c: MCPClientInfo): string {
  if (c.last_seen) return `✓ ${c.name} connected · used ${formatAgo(c.last_seen.at)}`;
  if (c.installed === true) return `✓ ${c.detail || "Already connected"}`;
  return c.detail;
}

export function ConnectTools({
  api,
  onNotice,
}: {
  api: SettingsApi;
  onNotice(t: string, e?: boolean): void;
}) {
  const [clients, setClients] = useState<MCPClientInfo[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [remote, setRemote] = useState(false);
  const [busy, setBusy] = useState<string>();
  const [results, setResults] = useState<Record<string, { ok: boolean; output: string }>>({});
  const origin = typeof window !== "undefined" && window.location ? window.location.origin : "";

  function load() {
    api
      .request<MCPClientInfo[]>("/mcp-clients")
      .then((rows) => {
        setClients(rows);
        setLoaded(true);
      })
      .catch((e) => onNotice(String(e), true));
  }
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function install(id: string) {
    setBusy(id);
    try {
      const result = await api.request<{ ok: boolean; output: string }>(
        `/mcp-clients/${id}/install`,
        { method: "POST" },
      );
      setResults((old) => ({ ...old, [id]: result }));
      onNotice(result.ok ? "Connected" : result.output || "Install failed", !result.ok);
      load();
    } catch (e) {
      setResults((old) => ({ ...old, [id]: { ok: false, output: String(e) } }));
      onNotice(String(e), true);
    } finally {
      setBusy(undefined);
    }
  }

  function copy(text: string) {
    if (navigator.clipboard?.writeText) {
      void navigator.clipboard.writeText(text).then(
        () => onNotice("Copied"),
        () => onNotice("Could not copy — select and copy manually", true),
      );
    }
  }

  return (
    <section className="connect-tools" aria-labelledby="connect-tools-heading">
      <h3 id="connect-tools-heading">Connect your AI tools</h3>
      <p className="subhint">
        Hook up Claude Code, Codex, or another MCP client so it can see and steer this
        Lectern.
      </p>
      <label className="connect-remote-toggle">
        <input
          type="checkbox"
          checked={remote}
          onChange={(e) => setRemote(e.target.checked)}
        />{" "}
        This computer is not the Lectern host
      </label>
      {!loaded && (
        <p role="status" className="sub">
          Loading…
        </p>
      )}
      <div className="connect-tools-grid" id="connect-tools-grid">
        {clients.map((c) => {
          // The web connector's URL is the same from every computer.
          const command =
            remote && c.id !== "web-connectors" ? c.remote_command : c.command;
          const shown = command ? fillOrigin(command, origin) : undefined;
          const result = results[c.id];
          return (
            <article className="rowcard connect-client" key={c.id} data-client={c.id}>
              <h3>{c.name}</h3>
              <p className="sub connect-status" role="status">
                {statusLine(c)}
              </p>
              <div className="btnrow">
                {c.can_install && (
                  <button
                    className="b"
                    type="button"
                    disabled={busy === c.id}
                    aria-label={`Connect ${c.name}`}
                    onClick={() => void install(c.id)}
                  >
                    {busy === c.id ? "Connecting…" : c.installed ? "Reinstall" : "Connect"}
                  </button>
                )}
                {c.id === "claude-desktop" && (
                  <a className="b connect-deeplink" href="claude://">
                    Open Claude Desktop
                  </a>
                )}
                {c.id === "cursor" && c.lectern_path && (
                  <a
                    className="b connect-deeplink"
                    href={cursorDeepLink(c.lectern_path, remote, origin)}
                  >
                    Connect Cursor
                  </a>
                )}
                {c.id === "vscode" && c.lectern_path && (
                  <a
                    className="b connect-deeplink"
                    href={vscodeDeepLink(c.lectern_path, remote, origin)}
                  >
                    Connect VS Code
                  </a>
                )}
                {c.external_url && (
                  <a
                    className="b connect-deeplink"
                    href={c.external_url}
                    target="_blank"
                    rel="noreferrer"
                  >
                    Open claude.ai connectors
                  </a>
                )}
                {c.id === "web-connectors" && (
                  <a
                    className="b connect-deeplink"
                    href="https://chatgpt.com/#settings"
                    target="_blank"
                    rel="noreferrer"
                  >
                    Open ChatGPT settings
                  </a>
                )}
              </div>
              {result && (
                <p
                  className={`connect-result ${result.ok ? "ok" : "err"}`}
                  role="status"
                >
                  {result.ok ? "✓ " : "✗ "}
                  {result.output}
                </p>
              )}
              {shown && (
                <div className="connect-snippet">
                  <pre>{shown}</pre>
                  <button className="b" type="button" onClick={() => copy(shown)}>
                    Copy command
                  </button>
                </div>
              )}
              {c.id === "claude-desktop" && c.lectern_path && (
                <div className="connect-snippet">
                  <pre>{claudeDesktopConfig(c.lectern_path, remote, origin)}</pre>
                  <button
                    className="b"
                    type="button"
                    onClick={() => copy(claudeDesktopConfig(c.lectern_path!, remote, origin))}
                  >
                    Copy config
                  </button>
                </div>
              )}
            </article>
          );
        })}
      </div>
    </section>
  );
}
