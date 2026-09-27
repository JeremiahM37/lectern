// Settings → Machines, per machine (docs/ssh.md): how the connection is
// doing and a reconnect, SSH options (alias, jump hosts, agent forwarding,
// OpenSSH transport for Kerberos and security keys), ports on the machine's
// localhost to forward to this browser, downloads, and an editor link.
import { useEffect, useState } from "react";
import { withToken, type JsonValue } from "../api";
import type { LiveView, Target } from "../types";
import { liveAddress } from "../media/Live";
import { usePref } from "../prefs/store";
import { EDITOR_PREF, EDITORS, editorLink, sshOptions, type EditorId, type SSHOptions } from "./editor";
import "./remote.css";

export interface RemoteApi {
  request<T>(path: string, options?: { method?: string; body?: JsonValue }): Promise<T>;
}

export interface ConnStatus {
  state: "idle" | "connected" | "reconnecting" | "down";
  since?: number;
  last_ok?: number;
  last_error?: string;
  reconnects: number;
  transport: string;
  via?: string;
}

function ago(ts?: number) {
  if (!ts) return "";
  const s = Math.max(0, Math.round(Date.now() / 1000 - ts));
  return s < 60 ? `${s}s ago` : s < 3600 ? `${Math.round(s / 60)}m ago` : `${Math.round(s / 3600)}h ago`;
}

const stateLabel: Record<ConnStatus["state"], string> = {
  idle: "not connected yet",
  connected: "connected",
  reconnecting: "reconnecting",
  down: "unreachable",
};

// ConnectionChip polls while shown; a probe reconnects a dropped link, so the
// chip moves from "reconnecting" to "connected" on its own.
export function ConnectionChip({ api, target }: { api: RemoteApi; target: Target }) {
  const [st, setSt] = useState<ConnStatus>();
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let alive = true;
    const load = (probe: boolean) =>
      void api
        .request<ConnStatus>(`/targets/${target.id}/connection${probe ? "?probe=1" : ""}`)
        .then((v) => alive && setSt(v))
        .catch(() => {});
    load(false);
    const timer = setInterval(() => load(true), 20000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, [api, target.id]);
  if (!st || target.kind === "mock") return null;
  return (
    <span className={`conn conn-${st.state}`} data-conn={st.state}>
      <span className="conn-dot" aria-hidden />
      <span title={st.last_error || undefined}>
        {stateLabel[st.state]}
        {st.state === "connected" && st.last_ok ? ` · ${ago(st.last_ok)}` : ""}
        {st.reconnects > 0 ? ` · reconnected ${st.reconnects}×` : ""}
        {st.via ? ` · via ${st.via}` : ""}
        {st.transport === "openssh" ? " · OpenSSH" : ""}
      </span>
      {st.state !== "idle" && (
        <button
          type="button"
          className="linkish"
          disabled={busy}
          onClick={() => {
            setBusy(true);
            void api
              .request<ConnStatus>(`/targets/${target.id}/reconnect`, { method: "POST" })
              .then(setSt)
              .finally(() => setBusy(false));
          }}
        >
          {busy ? "Reconnecting…" : "Reconnect"}
        </button>
      )}
      {st.state === "down" && st.last_error && <small className="conn-error">{st.last_error}</small>}
    </span>
  );
}

function SSHForm({ api, target, onSaved, onNotice }: {
  api: RemoteApi; target: Target; onSaved(): void; onNotice(t: string, e?: boolean): void;
}) {
  const initial = sshOptions(target);
  const [opts, setOpts] = useState<SSHOptions>(initial);
  const [extra, setExtra] = useState((initial.options || []).join("\n"));
  const [busy, setBusy] = useState(false);
  const gssapi = /GSSAPIAuthentication\s*[= ]\s*yes/i.test(extra);
  const set = (patch: Partial<SSHOptions>) => setOpts((o) => ({ ...o, ...patch }));
  return (
    <form
      className="remote-form"
      onSubmit={(event) => {
        event.preventDefault();
        setBusy(true);
        const options = extra.split("\n").map((l) => l.trim()).filter(Boolean);
        void api
          .request(`/targets/${target.id}/ssh`, { method: "PUT", body: { ...opts, options } as unknown as JsonValue })
          .then(() => {
            onNotice(`Saved how ${target.name} is reached`);
            onSaved();
          })
          .catch((e) => onNotice(String(e), true))
          .finally(() => setBusy(false));
      }}
    >
      {target.kind === "ssh" && (
        <>
          <label>
            Transport
            <select value={opts.transport || ""} onChange={(e) => set({ transport: e.target.value as SSHOptions["transport"] })}>
              <option value="">Built in (keys and ssh-agent)</option>
              <option value="openssh">OpenSSH client (full ~/.ssh/config, Kerberos, security keys)</option>
            </select>
          </label>
          <label>
            ssh_config alias
            <input value={opts.alias || ""} placeholder="build-box" onChange={(e) => set({ alias: e.target.value })} />
          </label>
          <label>
            Jump hosts (ProxyJump)
            <input value={opts.proxy_jump || ""} placeholder="user@bastion:22, second-hop" onChange={(e) => set({ proxy_jump: e.target.value })} />
          </label>
          <label className="check">
            <input type="checkbox" checked={!!opts.forward_agent} onChange={(e) => set({ forward_agent: e.target.checked })} />
            Forward ssh-agent (git push with your keys, security keys touched on this server)
          </label>
          <label className="check">
            <input type="checkbox" checked={!opts.no_agent} onChange={(e) => set({ no_agent: !e.target.checked })} />
            Offer keys from ssh-agent
          </label>
          <label className="check">
            <input
              type="checkbox"
              checked={gssapi}
              onChange={(e) =>
                setExtra((x) =>
                  e.target.checked
                    ? [...x.split("\n").filter((l) => l.trim()), "GSSAPIAuthentication=yes", "GSSAPIDelegateCredentials=yes"].join("\n")
                    : x.split("\n").filter((l) => !/^GSSAPI/i.test(l.trim())).join("\n"),
                )
              }
            />
            Kerberos (GSSAPI) — needs the OpenSSH transport and a ticket on this server
          </label>
          <label>
            Extra OpenSSH options, one per line
            <textarea rows={3} value={extra} placeholder="ServerAliveInterval=30" onChange={(e) => setExtra(e.target.value)} />
          </label>
        </>
      )}
      <label>
        Editor host
        <input
          value={opts.editor_host || ""}
          placeholder={target.kind === "local" ? "how your computer reaches this server over SSH" : "defaults to the alias or host"}
          onChange={(e) => set({ editor_host: e.target.value })}
        />
      </label>
      <button type="submit" disabled={busy}>{busy ? "Saving…" : "Save connection"}</button>
    </form>
  );
}

interface Listening { port: number; address: string; process?: string }

// Ports lists what listens on the machine and the forwards already open to
// this browser; one button forwards another.
export function TargetPorts({ api, target, onNotice }: { api: RemoteApi; target: Target; onNotice(t: string, e?: boolean): void }) {
  const [listening, setListening] = useState<Listening[]>();
  const [views, setViews] = useState<LiveView[]>([]);
  const [enabled, setEnabled] = useState(true);
  const [port, setPort] = useState("");
  const [busy, setBusy] = useState(false);
  const reload = () =>
    void api
      .request<{ enabled: boolean; views: LiveView[] }>("/live")
      .then((v) => {
        setEnabled(v.enabled);
        setViews(v.views.filter((x) => x.kind === "port" && x.target_id === target.id));
      })
      .catch(() => {});
  useEffect(reload, [target.id]);
  const detect = () =>
    void api
      .request<{ ports: Listening[] }>(`/targets/${target.id}/ports`)
      .then((v) => setListening(v.ports))
      .catch((e) => onNotice(String(e), true));
  const forward = (p: number) => {
    setBusy(true);
    void api
      .request<LiveView>("/live/ports", { method: "POST", body: { port: p, target_id: target.id, title: `${target.name}:${p}` } })
      .then(() => {
        setPort("");
        reload();
      })
      .catch((e) => onNotice(String(e), true))
      .finally(() => setBusy(false));
  };
  return (
    <div className="remote-ports" data-ports={target.id}>
      {!enabled && <p className="subhint">Forwarding opens a listening port on the Lectern server, so it is off until LECTERN_LIVE=1 is set.</p>}
      <table className="remote-table">
        <tbody>
          {views.map((v) => {
            const { base } = liveAddress(v);
            return (
              <tr key={v.id} data-forward={v.port}>
                <td><code>localhost:{v.port}</code></td>
                <td>→ <a href={base + "/"} target="_blank" rel="noopener noreferrer">{base.replace(/^https?:\/\//, "")}</a></td>
                <td className="sub">{v.connections} open</td>
                <td>
                  <button type="button" className="b danger" onClick={() => void api.request(`/live/${v.id}`, { method: "DELETE" }).then(reload)}>
                    Stop
                  </button>
                </td>
              </tr>
            );
          })}
          {views.length === 0 && (
            <tr><td colSpan={4} className="sub">No ports forwarded from {target.name}.</td></tr>
          )}
        </tbody>
      </table>
      <div className="remote-row">
        <input
          aria-label={`Port on ${target.name}`}
          inputMode="numeric"
          placeholder="port"
          value={port}
          onChange={(e) => setPort(e.target.value.replace(/\D/g, "").slice(0, 5))}
        />
        <button type="button" className="b" disabled={!enabled || busy || !(Number(port) > 0)} onClick={() => forward(Number(port))}>
          Forward
        </button>
        <button type="button" className="b" onClick={detect}>Detect listening ports</button>
      </div>
      {listening && (
        <ul className="remote-listening">
          {listening.length === 0 && <li className="sub">Nothing is listening.</li>}
          {listening.map((l) => (
            <li key={l.port}>
              <code>{l.address}:{l.port}</code> {l.process && <span className="sub">{l.process}</span>}
              {views.some((v) => v.port === l.port) ? (
                <span className="chip ds">forwarded</span>
              ) : (
                <button type="button" className="linkish" disabled={!enabled || busy} onClick={() => forward(l.port)}>
                  Forward
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Download({ target }: { target: Target }) {
  const [path, setPath] = useState(target.workroot || "");
  const ok = path.startsWith("/") || path === "~" || path.startsWith("~/");
  return (
    <div className="remote-row">
      <input aria-label={`Path on ${target.name}`} placeholder="/path/to/file-or-folder" value={path} onChange={(e) => setPath(e.target.value)} />
      <a
        className={"b" + (ok ? "" : " disabled")}
        aria-disabled={!ok}
        href={ok ? withToken(`/api/targets/${target.id}/download?path=${encodeURIComponent(path)}`) : undefined}
        download
      >
        Download
      </a>
      <span className="subhint">A folder arrives as .tar.gz.</span>
    </div>
  );
}

export function EditorLink({ target, path, compact }: { target: Target; path: string; compact?: boolean }) {
  const [editor] = usePref<EditorId | "none">(EDITOR_PREF, "vscode");
  if (editor === "none") return null;
  const href = editorLink(editor, target, path);
  const label = EDITORS.find((e) => e.id === editor)?.label || editor;
  if (!href) return compact ? null : <span className="subhint">Your editor cannot reach {target.name} directly; set an editor host.</span>;
  return (
    <a className="b editor-link" href={href} data-editor={editor}>
      Open in {label}
    </a>
  );
}

export function EditorChoice() {
  const [editor, setEditor] = usePref<EditorId | "none">(EDITOR_PREF, "vscode");
  return (
    <label className="editor-choice">
      Open workspaces in
      <select value={editor} onChange={(e) => setEditor(e.target.value as EditorId | "none")}>
        {EDITORS.map((e) => (
          <option key={e.id} value={e.id}>{e.label}</option>
        ))}
      </select>
    </label>
  );
}

// MachineRemote is the disclosure under each machine card.
export function MachineRemote({ api, target, onChanged, onNotice }: {
  api: RemoteApi; target: Target; onChanged(): void; onNotice(t: string, e?: boolean): void;
}) {
  const [tab, setTab] = useState<"" | "connection" | "ports" | "files">("");
  if (target.kind === "sandbox" || target.kind === "mock") return null;
  return (
    <div className="machine-remote">
      <ConnectionChip api={api} target={target} />
      <div className="remote-tabs" role="tablist">
        {(["connection", "ports", "files"] as const).map((k) => (
          <button type="button" role="tab" key={k} aria-selected={tab === k} data-remote-tab={k} onClick={() => setTab(tab === k ? "" : k)}>
            {k === "connection" ? "Connection" : k === "ports" ? "Ports" : "Files & editor"}
          </button>
        ))}
      </div>
      {tab === "connection" && <SSHForm api={api} target={target} onSaved={onChanged} onNotice={onNotice} />}
      {tab === "ports" && <TargetPorts api={api} target={target} onNotice={onNotice} />}
      {tab === "files" && (
        <div className="remote-files">
          <Download target={target} />
          {target.workroot && (
            <div className="remote-row">
              <code>{target.workroot}</code>
              <EditorLink target={target} path={target.workroot} />
            </div>
          )}
          <EditorChoice />
        </div>
      )}
    </div>
  );
}
