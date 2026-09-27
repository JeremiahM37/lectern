// Sandboxes (docs/sandboxes.md): a sandbox machine's provider — Proxmox
// linked clones, Docker containers on this server or another machine, or a
// script provider wired to any cloud through a lectern.sandbox.yaml — plus
// the lifecycle of every sandbox Lectern made: create, suspend, resume,
// destroy.
import { useEffect, useState } from "react";
import type { JsonValue } from "../api";
import type { Target } from "../types";
import type { RemoteApi } from "./MachineRemote";

export interface SandboxConfig {
  provider?: "proxmox" | "docker" | "script";
  image?: string;
  machine?: string;
  docker_host?: string;
  run_args?: string[];
  config_path?: string;
  env?: Record<string, string>;
  on_finish?: "" | "destroy" | "suspend" | "keep";
  trusted?: Record<string, string>;
}

export interface SandboxRow {
  id: number;
  target_id: number;
  target_name: string;
  provider: string;
  ext_id: string;
  attempt_id: number | null;
  status: string;
  task_id?: number;
  task_title?: string;
  attempt_n?: number;
  created_at: number;
  destroyed_at: number | null;
  can: Record<string, boolean>;
}

export function sandboxConfig(target: Pick<Target, "sandbox_json">): SandboxConfig {
  try {
    const c = JSON.parse(target.sandbox_json || "{}") as SandboxConfig;
    return { ...c, provider: c.provider || "proxmox" };
  } catch {
    return { provider: "proxmox" };
  }
}

function envText(env?: Record<string, string>) {
  return Object.entries(env || {}).map(([k, v]) => `${k}=${v}`).join("\n");
}
export function parseEnv(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const i = line.indexOf("=");
    if (i > 0) out[line.slice(0, i).trim()] = line.slice(i + 1);
  }
  return out;
}

// ProviderFields edits a config; used when adding a machine and on one.
export function ProviderFields({ value, onChange, machines }: {
  value: SandboxConfig; onChange(v: SandboxConfig): void; machines: Target[];
}) {
  const set = (patch: Partial<SandboxConfig>) => onChange({ ...value, ...patch });
  return (
    <div className="sandbox-fields">
      <label>
        Provider
        <select value={value.provider || "proxmox"} onChange={(e) => set({ provider: e.target.value as SandboxConfig["provider"] })}>
          <option value="proxmox">Proxmox LXC linked clone</option>
          <option value="docker">Docker container</option>
          <option value="script">Script (Fly, Modal, Vercel, any CLI)</option>
        </select>
      </label>
      {value.provider === "docker" && (
        <>
          <label>
            Image
            <input value={value.image || ""} placeholder="ghcr.io/you/dev-image:latest" onChange={(e) => set({ image: e.target.value })} />
          </label>
          <label>
            Docker runs on
            <select value={value.machine || ""} onChange={(e) => set({ machine: e.target.value })}>
              <option value="">this server</option>
              {machines.filter((m) => m.kind !== "sandbox").map((m) => (
                <option key={m.id} value={m.name}>{m.name}</option>
              ))}
            </select>
          </label>
          <label>
            Docker host (optional, -H)
            <input value={value.docker_host || ""} placeholder="ssh://user@docker-box or tcp://…" onChange={(e) => set({ docker_host: e.target.value })} />
          </label>
          <label>
            Extra run arguments, one per line
            <textarea rows={2} value={(value.run_args || []).join("\n")} placeholder={"--memory\n4g"}
              onChange={(e) => set({ run_args: e.target.value.split("\n").map((l) => l.trim()).filter(Boolean) })} />
          </label>
        </>
      )}
      {value.provider === "script" && (
        <label>
          Hooks file
          <input value={value.config_path || ""} placeholder="{repo}/lectern.sandbox.yaml" onChange={(e) => set({ config_path: e.target.value })} />
        </label>
      )}
      <label>
        Environment for every sandbox, NAME=value per line
        <textarea rows={2} defaultValue={envText(value.env)} onBlur={(e) => set({ env: parseEnv(e.target.value) })} />
      </label>
      <label>
        When an attempt finishes
        <select value={value.on_finish || ""} onChange={(e) => set({ on_finish: e.target.value as SandboxConfig["on_finish"] })}>
          <option value="">Destroy the sandbox</option>
          <option value="suspend">Suspend it (resume later from the list)</option>
          <option value="keep">Keep it running</option>
        </select>
      </label>
    </div>
  );
}

function Hooks({ api, target, onNotice }: { api: RemoteApi; target: Target; onNotice(t: string, e?: boolean): void }) {
  const [view, setView] = useState<{ path: string; content: string; sha256: string; trusted: boolean; error?: string }>();
  const load = () =>
    void api
      .request<typeof view>(`/targets/${target.id}/sandbox/hooks`)
      .then(setView)
      .catch((e) => onNotice(String(e), true));
  useEffect(load, [target.id, target.sandbox_json]);
  if (!view) return null;
  return (
    <div className="sandbox-hooks" data-trusted={view.trusted}>
      <div className="remote-row">
        <code>{view.path}</code>
        {view.trusted ? <span className="chip ds">trusted</span> : <span className="chip warn">not trusted</span>}
      </div>
      {view.error && <p className="subhint" role="alert">{view.error}</p>}
      {view.content && <pre className="sandbox-yaml">{view.content}</pre>}
      {!view.trusted && view.content && !view.error && (
        <button
          type="button"
          className="ok"
          onClick={() =>
            void api
              .request(`/targets/${target.id}/sandbox/trust`, { method: "POST", body: { path: view.path, sha256: view.sha256 } })
              .then(() => {
                onNotice("Hooks trusted. Any change to the file needs trusting again.");
                load();
              })
              .catch((e) => onNotice(String(e), true))
          }
        >
          Trust these hooks
        </button>
      )}
      <p className="subhint">These commands run on the Lectern server. Lectern runs them only while the file is exactly what you trusted, so an agent editing it cannot run anything.</p>
    </div>
  );
}

export function SandboxMachine({ api, target, machines, onChanged, onNotice }: {
  api: RemoteApi; target: Target; machines: Target[]; onChanged(): void; onNotice(t: string, e?: boolean): void;
}) {
  const [open, setOpen] = useState(false);
  const [cfg, setCfg] = useState<SandboxConfig>(() => sandboxConfig(target));
  const [busy, setBusy] = useState("");
  const saved = sandboxConfig(target);
  return (
    <div className="machine-remote sandbox-machine">
      <div className="remote-row">
        <span className="chip info" data-provider={saved.provider}>
          {saved.provider === "docker" ? `Docker · ${saved.image}${saved.machine ? ` on ${saved.machine}` : ""}` :
            saved.provider === "script" ? "Script provider" : `Proxmox template ${target.host}`}
        </span>
        <button type="button" className="linkish" onClick={() => setOpen(!open)} aria-expanded={open}>
          {open ? "Close" : "Provider"}
        </button>
        <button
          type="button"
          className="b"
          disabled={!!busy}
          onClick={() => {
            setBusy("create");
            void api
              .request("/sandboxes", { method: "POST", body: { target_id: target.id } })
              .then(() => {
                onNotice("Sandbox created");
                window.dispatchEvent(new CustomEvent("lec-sandboxes-changed"));
              })
              .catch((e) => onNotice(String(e), true))
              .finally(() => setBusy(""));
          }}
        >
          {busy === "create" ? "Creating…" : "+ Sandbox"}
        </button>
      </div>
      {open && (
        <form
          className="remote-form"
          onSubmit={(e) => {
            e.preventDefault();
            setBusy("save");
            void api
              .request(`/targets/${target.id}/sandbox`, { method: "PUT", body: cfg as unknown as JsonValue })
              .then(() => {
                onNotice(`Saved ${target.name}'s sandbox provider`);
                onChanged();
              })
              .catch((err) => onNotice(String(err), true))
              .finally(() => setBusy(""));
          }}
        >
          <ProviderFields value={cfg} onChange={setCfg} machines={machines} />
          <button type="submit" disabled={!!busy}>Save provider</button>
        </form>
      )}
      {saved.provider === "script" && open && <Hooks api={api} target={target} onNotice={onNotice} />}
    </div>
  );
}

function when(ts: number) {
  return new Date(ts * 1000).toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

// SandboxList is every live sandbox with its lifecycle buttons.
export function SandboxList({ api, onNotice, onOpenTask }: {
  api: RemoteApi; onNotice(t: string, e?: boolean): void; onOpenTask?(id: number): void;
}) {
  const [rows, setRows] = useState<SandboxRow[]>();
  const [all, setAll] = useState(false);
  const [busy, setBusy] = useState(0);
  const load = () =>
    void api
      .request<SandboxRow[]>(`/sandboxes${all ? "?all=1" : ""}`)
      .then(setRows)
      .catch(() => setRows([]));
  useEffect(() => {
    load();
    const on = () => load();
    window.addEventListener("lec-sandboxes-changed", on);
    return () => window.removeEventListener("lec-sandboxes-changed", on);
  }, [all]);
  if (!rows) return null;
  const act = (row: SandboxRow, action: string) => {
    if (action === "destroy" && !confirm(`Destroy sandbox ${row.ext_id}? Anything only inside it is lost.`)) return;
    setBusy(row.id);
    void api
      .request(`/sandboxes/${row.id}/${action}`, { method: "POST" })
      .then(load)
      .catch((e) => onNotice(String(e), true))
      .finally(() => setBusy(0));
  };
  return (
    <section className="sandbox-list" aria-label="Sandboxes">
      <div className="remote-row">
        <h3>Sandboxes</h3>
        <label className="check">
          <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> show destroyed
        </label>
      </div>
      {rows.length === 0 && <p className="sub">No sandboxes are running.</p>}
      <div className="sandbox-cards">
        {rows.map((row) => (
          <article key={row.id} className={`sandbox-card s-${row.status}`} data-sandbox={row.ext_id}>
            <header>
              <b>{row.ext_id}</b>
              <span className={`chip ${row.status === "running" ? "ds" : row.status === "suspended" ? "warn" : ""}`}>{row.status}</span>
            </header>
            <p className="sub">
              {row.provider} on {row.target_name} · {when(row.created_at)}
              {row.task_id ? (
                <>
                  {" · "}
                  <button type="button" className="linkish" onClick={() => onOpenTask?.(row.task_id!)}>
                    {row.task_title} #{row.attempt_n}
                  </button>
                </>
              ) : " · made by hand"}
            </p>
            {!row.destroyed_at && (
              <div className="btnrow">
                {row.can.suspend && <button type="button" disabled={busy === row.id} onClick={() => act(row, "suspend")}>Suspend</button>}
                {row.can.resume && <button type="button" disabled={busy === row.id} onClick={() => act(row, "resume")}>Resume</button>}
                {row.can.destroy && <button type="button" className="danger" disabled={busy === row.id} onClick={() => act(row, "destroy")}>Destroy</button>}
              </div>
            )}
          </article>
        ))}
      </div>
    </section>
  );
}
