// Settings → Machines, per machine (docs/ssh.md): how the connection is
// doing and a reconnect, SSH options (alias, jump hosts, agent forwarding,
// OpenSSH transport for Kerberos and security keys), ports on the machine's
// localhost to forward to this browser, downloads, and an editor link.
import { useEffect, useState } from "react";
import { withToken, type JsonValue } from "../api";
import type { LiveView, Target } from "../types";
import { liveAddress } from "../media/Live";
import { usePref } from "../prefs/store";
import { formatRelative, t, useLocale } from "../i18n";
import { EDITOR_PREF, EDITORS, editorLink, sshOptions, type EditorId, type SSHOptions } from "./editor";

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
  return s < 60 ? formatRelative(-s, "second") : s < 3600 ? formatRelative(-Math.round(s / 60), "minute") : formatRelative(-Math.round(s / 3600), "hour");
}

const stateLabel = (state: ConnStatus["state"]) => t(`remote.conn.${state}`);

// ConnectionChip polls while shown; a probe reconnects a dropped link, so the
// chip moves from "reconnecting" to "connected" on its own.
export function ConnectionChip({ api, target }: { api: RemoteApi; target: Target }) {
  const [st, setSt] = useState<ConnStatus>();
  const [busy, setBusy] = useState(false);
  useLocale();
  useEffect(() => {
    let alive = true;
    const load = (probe: boolean) =>
      void api
        .request<ConnStatus>(`/targets/${target.id}/connection${probe ? "?probe=1" : ""}`)
        .then((v) => alive && setSt(v && v.state ? v : undefined))
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
        {stateLabel(st.state)}
        {st.state === "connected" && st.last_ok ? ` · ${ago(st.last_ok)}` : ""}
        {st.reconnects > 0 ? ` · ${t("remote.conn.reconnected", { n: st.reconnects })}` : ""}
        {st.via ? ` · ${t("remote.conn.via", { host: st.via })}` : ""}
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
          {busy ? t("remote.conn.reconnectingBusy") : t("remote.conn.reconnect")}
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
            onNotice(t("remote.ssh.saved", { name: target.name }));
            onSaved();
          })
          .catch((e) => onNotice(String(e), true))
          .finally(() => setBusy(false));
      }}
    >
      {target.kind === "ssh" && (
        <>
          <label>
            {t("remote.ssh.transport")}
            <select value={opts.transport || ""} onChange={(e) => set({ transport: e.target.value as SSHOptions["transport"] })}>
              <option value="">{t("remote.ssh.transportBuiltin")}</option>
              <option value="openssh">{t("remote.ssh.transportOpenssh")}</option>
            </select>
          </label>
          <label data-setting="machines.alias">
            {t("remote.ssh.alias")}
            <input value={opts.alias || ""} placeholder="build-box" onChange={(e) => set({ alias: e.target.value })} />
          </label>
          <label data-setting="machines.proxyJump">
            {t("remote.ssh.proxyJump")}
            <input value={opts.proxy_jump || ""} placeholder="user@bastion:22, second-hop" onChange={(e) => set({ proxy_jump: e.target.value })} />
          </label>
          <label className="check" data-setting="machines.forwardAgent">
            <input type="checkbox" checked={!!opts.forward_agent} onChange={(e) => set({ forward_agent: e.target.checked })} />
            {t("remote.ssh.forwardAgent")}
          </label>
          <label className="check">
            <input type="checkbox" checked={!opts.no_agent} onChange={(e) => set({ no_agent: !e.target.checked })} />
            {t("remote.ssh.useAgent")}
          </label>
          <label className="check" data-setting="machines.kerberos">
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
            {t("remote.ssh.kerberos")}
          </label>
          <label>
            {t("remote.ssh.extraOptions")}
            <textarea rows={3} value={extra} placeholder="ServerAliveInterval=30" onChange={(e) => setExtra(e.target.value)} />
          </label>
        </>
      )}
      <label data-setting="machines.editorHost">
        {t("remote.ssh.editorHost")}
        <input
          value={opts.editor_host || ""}
          placeholder={target.kind === "local" ? t("remote.ssh.editorHostLocal") : t("remote.ssh.editorHostRemote")}
          onChange={(e) => set({ editor_host: e.target.value })}
        />
      </label>
      <button type="submit" disabled={busy}>{busy ? t("remote.saving") : t("remote.ssh.save")}</button>
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
  useLocale();
  const reload = () =>
    void api
      .request<{ enabled: boolean; views: LiveView[] }>("/live")
      .then((v) => {
        setEnabled(v.enabled !== false);
        setViews((Array.isArray(v.views) ? v.views : []).filter((x) => x.kind === "port" && x.target_id === target.id));
      })
      .catch(() => {});
  useEffect(reload, [target.id]);
  const detect = () =>
    void api
      .request<{ ports: Listening[] }>(`/targets/${target.id}/ports`)
      .then((v) => setListening(Array.isArray(v.ports) ? v.ports : []))
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
      {!enabled && <p className="subhint">{t("remote.ports.off")}</p>}
      <table className="remote-table">
        <tbody>
          {views.map((v) => {
            const { base } = liveAddress(v);
            return (
              <tr key={v.id} data-forward={v.port}>
                <td><code>localhost:{v.port}</code></td>
                <td>→ <a href={base + "/"} target="_blank" rel="noopener noreferrer">{base.replace(/^https?:\/\//, "")}</a></td>
                <td className="sub">{t("remote.ports.open", { n: v.connections })}</td>
                <td>
                  <button type="button" className="b danger" onClick={() => void api.request(`/live/${v.id}`, { method: "DELETE" }).then(reload)}>
                    {t("remote.ports.stop")}
                  </button>
                </td>
              </tr>
            );
          })}
          {views.length === 0 && (
            <tr><td colSpan={4} className="sub">{t("remote.ports.none", { name: target.name })}</td></tr>
          )}
        </tbody>
      </table>
      <div className="remote-row">
        <input
          aria-label={t("remote.ports.portOn", { name: target.name })}
          inputMode="numeric"
          placeholder={t("remote.ports.port")}
          value={port}
          onChange={(e) => setPort(e.target.value.replace(/\D/g, "").slice(0, 5))}
        />
        <button type="button" className="b" disabled={!enabled || busy || !(Number(port) > 0)} onClick={() => forward(Number(port))}>
          {t("remote.ports.forward")}
        </button>
        <button type="button" className="b" onClick={detect}>{t("remote.ports.detect")}</button>
      </div>
      {listening && (
        <ul className="remote-listening">
          {listening.length === 0 && <li className="sub">{t("remote.ports.nothing")}</li>}
          {listening.map((l) => (
            <li key={l.port}>
              <code>{l.address}:{l.port}</code> {l.process && <span className="sub">{l.process}</span>}
              {views.some((v) => v.port === l.port) ? (
                <span className="chip ds">{t("remote.ports.forwarded")}</span>
              ) : (
                <button type="button" className="linkish" disabled={!enabled || busy} onClick={() => forward(l.port)}>
                  {t("remote.ports.forward")}
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
  useLocale();
  return (
    <div className="remote-row">
      <input aria-label={t("remote.files.pathOn", { name: target.name })} placeholder={t("remote.files.pathPlaceholder")} value={path} onChange={(e) => setPath(e.target.value)} />
      <a
        className={"b" + (ok ? "" : " disabled")}
        aria-disabled={!ok}
        href={ok ? withToken(`/api/targets/${target.id}/download?path=${encodeURIComponent(path)}`) : undefined}
        download
        data-setting="machines.download"
      >
        {t("remote.files.download")}
      </a>
      <span className="subhint">{t("remote.files.folderHint")}</span>
    </div>
  );
}

export function EditorLink({ target, path, compact }: { target: Target; path: string; compact?: boolean }) {
  const [editor] = usePref<EditorId | "none">(EDITOR_PREF, "vscode");
  useLocale();
  if (editor === "none") return null;
  const href = editorLink(editor, target, path);
  const label = editorLabel(editor);
  if (!href) return compact ? null : <span className="subhint">{t("remote.editor.unreachable", { name: target.name })}</span>;
  return (
    <a className="b editor-link" href={href} data-editor={editor}>
      {t("remote.editor.openIn", { editor: label })}
    </a>
  );
}

export function EditorChoice() {
  const [editor, setEditor] = usePref<EditorId | "none">(EDITOR_PREF, "vscode");
  useLocale();
  return (
    <label className="editor-choice" data-setting="machines.editor">
      {t("remote.editor.choice")}
      <select value={editor} onChange={(e) => setEditor(e.target.value as EditorId | "none")}>
        {EDITORS.map((e) => (
          <option key={e.id} value={e.id}>{editorLabel(e.id)}</option>
        ))}
      </select>
    </label>
  );
}

// Editor names are product names; only "Don't show" is translated.
function editorLabel(id: EditorId | "none") {
  return id === "none" ? t("remote.editor.none") : EDITORS.find((e) => e.id === id)?.label || id;
}

// MachineRemote is the disclosure under each machine card.
export function MachineRemote({ api, target, onChanged, onNotice }: {
  api: RemoteApi; target: Target; onChanged(): void; onNotice(t: string, e?: boolean): void;
}) {
  const [tab, setTab] = useState<"" | "connection" | "ports" | "files">("");
  useLocale();
  if (target.kind === "sandbox" || target.kind === "mock") return null;
  return (
    <div className="machine-remote">
      <ConnectionChip api={api} target={target} />
      <div className="remote-tabs" role="tablist">
        {(["connection", "ports", "files"] as const).map((k) => (
          <button type="button" role="tab" key={k} aria-selected={tab === k} data-remote-tab={k} data-setting={`machines.${k}`} onClick={() => setTab(tab === k ? "" : k)}>
            {t(`remote.tab.${k}`)}
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
