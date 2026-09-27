// Settings → Plugins (docs/plugins.md): what extends this Lectern — plugins
// bundled with it, plugins a person installed, and the configuration made
// outside any plugin — plus installing, updating and trusting, each behind a
// preview of exactly what the plugin may do. The server refuses every change
// that does not come from a signed-in person; this page only shows the
// decision clearly enough to make it.
import { useEffect, useState } from "react";
import type { Project } from "../types";
import type { SettingsApi } from "./Settings";
import { Modal } from "../sessions/Modal";
import { t, useLocale } from "../i18n";
import { loadPluginContributions } from "../plugins/contributions";

interface Capability {
  key: string;
  detail?: string[];
}
interface PluginRow {
  id: string;
  name: string;
  version?: string;
  description?: string;
  author?: string;
  homepage?: string;
  license?: string;
  bundled: boolean;
  status: string;
  problem?: string;
  enabled: boolean;
  project_ids: number[];
  contributions?: Record<string, number>;
  capabilities?: Capability[];
  secrets?: string[];
  secrets_set?: string[];
  source?: { kind: string; source?: string; ref?: string; index?: string; commit?: string; tree?: string; subdir?: string };
  content_hash?: string;
  consented_by?: string;
  consented_at?: number;
}
interface HookRun {
  id: number;
  event: string;
  run: string;
  started_at: number;
  duration_ms: number;
  ok: boolean;
  message: string;
  error: string;
}
interface PluginDetail extends PluginRow {
  detail?: {
    agents?: string[];
    mcp_servers?: string[];
    skills?: { id: string; description?: string }[];
    workflows?: { id: string; name: string }[];
    hooks?: { event: string; run: string; command: string[] }[];
    sandbox_providers?: { id: string; name: string }[];
    quick_commands?: { id: string; label: string; text: string }[];
    themes?: { id: string; name: string }[];
    palette_commands?: { id: string; title: string }[];
  };
  agents_more?: number;
  hook_runs?: HookRun[];
}
interface Preview {
  hash: string;
  source: { kind: string; path?: string; url?: string; ref?: string; index?: string; id?: string };
  commit?: string;
  format: string;
  skipped?: string[];
  manifest: { id: string; name: string; version: string; description?: string; author?: string; homepage?: string };
  capabilities: Capability[];
  accept: string[];
  contributions: Record<string, number>;
  from_version?: string;
  grown?: string[];
  unchanged?: boolean;
}
interface Local {
  agents: string[];
  mcp_servers: { project_id: number; project: string; servers: string[] }[];
}
interface Listing {
  id: string;
  name?: string;
  description?: string;
  version?: string;
  commit: string;
  index: string;
  installed?: string;
}
interface Source {
  name: string;
  url: string;
  ref: string;
  commit?: string;
  plugins?: number;
}

type Mode = "install" | "update" | "trust";

const KINDS = ["agents", "mcp_servers", "skills", "workflows", "hooks", "sandbox_providers", "quick_commands", "themes", "palette_commands"];
const short = (sha?: string) => (sha ? sha.slice(0, 12) : "");
const errorText = (error: unknown) => (error instanceof Error ? error.message : String(error));

function Contributions({ counts }: { counts?: Record<string, number> }) {
  const rows = KINDS.filter((kind) => counts?.[kind]);
  if (!rows.length) return <span className="plugin-chip">{t("plugins.contributesNothing")}</span>;
  return (
    <>
      {rows.map((kind) => (
        <span className="plugin-chip" key={kind}>
          {t("plugins.kind." + kind)} {counts![kind]}
        </span>
      ))}
    </>
  );
}

export function CapabilityList({ capabilities, grown = [] }: { capabilities: Capability[]; grown?: string[] }) {
  if (!capabilities.length) return <p className="plugin-caps-none">{t("plugins.cap.none")}</p>;
  const isNew = (key: string, detail?: string) => grown.includes(detail ? `${key}: ${detail}` : key);
  return (
    <ul className="plugin-caps">
      {capabilities.map((cap) => (
        <li key={cap.key} data-cap={cap.key} className={cap.key === "host_exec" || cap.key === "target_exec" ? "risky" : undefined}>
          <b>{t("plugins.cap." + cap.key)}</b>
          {isNew(cap.key) && <span className="plugin-new">{t("plugins.new")}</span>}
          {!!cap.detail?.length && (
            <ul>
              {cap.detail.map((d) => (
                <li key={d}>
                  <code>{d}</code>
                  {isNew(cap.key, d) && <span className="plugin-new">{t("plugins.new")}</span>}
                </li>
              ))}
            </ul>
          )}
        </li>
      ))}
    </ul>
  );
}

function ConsentDialog({ preview, mode, busy, onConsent, onClose }: { preview: Preview; mode: Mode; busy: boolean; onConsent: () => void; onClose: () => void }) {
  const [allowed, setAllowed] = useState(false);
  const m = preview.manifest;
  const title = mode === "install" ? t("plugins.consent.installTitle", { name: m.name }) : mode === "update" ? t("plugins.consent.updateTitle", { name: m.name }) : t("plugins.consent.trustTitle", { name: m.name });
  const where = preview.source.url || preview.source.path || "";
  return (
    <Modal id="plugin-consent" className="plugin-consent" aria-labelledby="plugin-consent-title" onCancel={(event) => { event.preventDefault(); onClose(); }}>
      <header className="plugin-consent-head">
        <h3 id="plugin-consent-title">{title}</h3>
        <button type="button" className="x" aria-label={t("settings.common.close")} data-close onClick={onClose}>×</button>
      </header>
      <div className="plugin-consent-body">
        <p className="plugin-consent-id">
          <code>{m.id}</code> {preview.from_version ? t("plugins.consent.versionChange", { from: preview.from_version, to: m.version }) : m.version}
          {m.author && <> · {m.author}</>}
        </p>
        {m.description && <p>{m.description}</p>}
        <dl className="plugin-facts">
          {where && (<><dt>{t("plugins.source")}</dt><dd><code>{where}</code>{preview.source.ref && <> @ <code>{preview.source.ref}</code></>}</dd></>)}
          {preview.source.index && (<><dt>{t("plugins.fromSource")}</dt><dd>{preview.source.index}</dd></>)}
          {preview.commit && (<><dt>{t("plugins.commit")}</dt><dd><code>{short(preview.commit)}</code></dd></>)}
          <dt>{t("plugins.contentHash")}</dt>
          <dd><code>{short(preview.hash)}</code></dd>
        </dl>
        <h4>{t("plugins.contributes")}</h4>
        <p className="plugin-chips"><Contributions counts={preview.contributions} /></p>
        <h4>{t("plugins.consent.capabilities")}</h4>
        <CapabilityList capabilities={preview.capabilities} grown={preview.grown} />
        {!!preview.grown?.length && <p className="plugin-warning" role="note">{t("plugins.consent.grown", { count: preview.grown.length })}</p>}
        {preview.format === "claude" && <p className="plugin-note">{t("plugins.consent.claude")}</p>}
        {!!preview.skipped?.length && <p className="plugin-note">{t("plugins.consent.skipped", { items: preview.skipped.join(", ") })}</p>}
        {preview.unchanged ? (
          <p className="plugin-note" role="status">{t("plugins.consent.unchanged")}</p>
        ) : (
          <label className="plugin-allow">
            <input id="plugin-consent-check" type="checkbox" checked={allowed} onChange={(event) => setAllowed(event.target.checked)} />
            {preview.capabilities.length ? t("plugins.consent.allow") : t("plugins.consent.allowNone")}
          </label>
        )}
      </div>
      <div className="btnrow plugin-consent-actions">
        <button type="button" className="b" onClick={onClose}>{t("plugins.cancel")}</button>
        {!preview.unchanged && (
          <button id="plugin-consent-install" type="button" className="b primary" disabled={!allowed || busy} onClick={onConsent}>
            {mode === "install" ? t("plugins.consent.install") : mode === "update" ? t("plugins.consent.update") : t("plugins.consent.trust")}
          </button>
        )}
      </div>
    </Modal>
  );
}

function Scope({ plugin, projects, onSave }: { plugin: PluginRow; projects: Project[]; onSave: (ids: number[]) => void }) {
  const [chosen, setChosen] = useState<number[]>(plugin.project_ids || []);
  const [some, setSome] = useState((plugin.project_ids || []).length > 0);
  useEffect(() => {
    setChosen(plugin.project_ids || []);
    setSome((plugin.project_ids || []).length > 0);
  }, [plugin.project_ids?.join(",")]);
  return (
    <fieldset className="plugin-scope">
      <legend>{t("plugins.scope")}</legend>
      <label><input type="radio" name={"scope-" + plugin.id} checked={!some} onChange={() => setSome(false)} /> {t("plugins.scope.everywhere")}</label>
      <label><input type="radio" name={"scope-" + plugin.id} checked={some} onChange={() => setSome(true)} /> {t("plugins.scope.chosen")}</label>
      {some && (
        <div className="plugin-scope-projects">
          {projects.map((p) => (
            <label key={p.id}>
              <input type="checkbox" checked={chosen.includes(p.id)} onChange={(event) => setChosen(event.target.checked ? [...chosen, p.id] : chosen.filter((id) => id !== p.id))} />
              {p.name}
            </label>
          ))}
        </div>
      )}
      <p className="personal-hint">{t("plugins.scope.hint")}</p>
      <button type="button" className="b" disabled={some && !chosen.length} onClick={() => onSave(some ? chosen : [])}>{t("plugins.scope.save")}</button>
    </fieldset>
  );
}

function Secrets({ plugin, onSave }: { plugin: PluginRow; onSave: (name: string, value: string) => void }) {
  const [values, setValues] = useState<Record<string, string>>({});
  if (!plugin.secrets?.length) return null;
  return (
    <fieldset className="plugin-secrets">
      <legend>{t("plugins.secrets")}</legend>
      {plugin.secrets.map((name) => (
        <label key={name}>
          <span><code>{name}</code> {plugin.secrets_set?.includes(name) ? t("plugins.secrets.set") : t("plugins.secrets.unset")}</span>
          <span className="plugin-secret-row">
            <input type="password" autoComplete="off" aria-label={t("plugins.secrets.value", { name })} value={values[name] || ""} onChange={(event) => setValues({ ...values, [name]: event.target.value })} />
            <button type="button" className="b" onClick={() => { onSave(name, values[name] || ""); setValues({ ...values, [name]: "" }); }}>
              {values[name] ? t("plugins.secrets.save") : t("plugins.secrets.clear")}
            </button>
          </span>
        </label>
      ))}
    </fieldset>
  );
}

function Detail({ api, id, projects, onChanged, onNotice }: { api: SettingsApi; id: string; projects: Project[]; onChanged: () => void; onNotice(text: string, error?: boolean): void }) {
  const [detail, setDetail] = useState<PluginDetail | null>(null);
  const load = () => api.request<PluginDetail>(`/plugins/${encodeURIComponent(id)}`).then(setDetail).catch((error) => onNotice(errorText(error), true));
  useEffect(() => void load(), [id]);
  if (!detail) return <p className="plugin-loading">{t("plugins.loading")}</p>;
  const d = detail.detail || {};
  const put = async (path: string, body: Record<string, unknown>, done: string) => {
    try {
      await api.request(`/plugins/${encodeURIComponent(id)}${path}`, { method: "PUT", body: body as never });
      onNotice(done);
      await load();
      onChanged();
    } catch (error) {
      onNotice(errorText(error), true);
    }
  };
  const list = (key: string, rows: string[]) =>
    rows.length > 0 && (
      <div className="plugin-detail-row" key={key}>
        <b>{t("plugins.kind." + key)}</b>
        <span>{rows.join(", ")}</span>
      </div>
    );
  return (
    <div className="plugin-detail">
      <h4>{t("plugins.consent.capabilities")}</h4>
      <CapabilityList capabilities={detail.capabilities || []} />
      <h4>{t("plugins.contributes")}</h4>
      {list("agents", [...(d.agents || []), ...(detail.agents_more ? [t("plugins.more", { count: detail.agents_more })] : [])])}
      {list("mcp_servers", d.mcp_servers || [])}
      {list("skills", (d.skills || []).map((s) => s.id))}
      {list("workflows", (d.workflows || []).map((w) => w.name))}
      {list("hooks", (d.hooks || []).map((h) => `${h.event} (${h.run === "host" ? t("plugins.hook.host") : t("plugins.hook.target")})`))}
      {list("sandbox_providers", (d.sandbox_providers || []).map((p) => p.name))}
      {list("quick_commands", (d.quick_commands || []).map((q) => q.label || q.text))}
      {list("themes", (d.themes || []).map((th) => th.name))}
      {list("palette_commands", (d.palette_commands || []).map((p) => p.title))}
      {!detail.bundled && detail.source && (
        <dl className="plugin-facts">
          <dt>{t("plugins.source")}</dt>
          <dd><code>{detail.source.source}</code>{detail.source.ref && <> @ <code>{detail.source.ref}</code></>}</dd>
          {detail.source.commit && (<><dt>{t("plugins.commit")}</dt><dd><code>{short(detail.source.commit)}</code></dd></>)}
          <dt>{t("plugins.contentHash")}</dt>
          <dd><code>{short(detail.content_hash)}</code></dd>
          {detail.consented_by && (<><dt>{t("plugins.consentedBy")}</dt><dd>{detail.consented_by}</dd></>)}
        </dl>
      )}
      <Scope plugin={detail} projects={projects} onSave={(ids) => void put("", { project_ids: ids }, t("plugins.scope.saved"))} />
      <Secrets plugin={detail} onSave={(name, value) => void put("/secrets", { [name]: value }, t("plugins.secrets.saved"))} />
      {!!detail.hook_runs?.length && (
        <>
          <h4>{t("plugins.hookRuns")}</h4>
          <ul className="plugin-runs">
            {detail.hook_runs.map((run) => (
              <li key={run.id} className={run.ok ? "ok" : "failed"}>
                <b>{run.event}</b> {run.ok ? t("plugins.hookRun.ok") : t("plugins.hookRun.failed")} · {run.duration_ms} ms
                {(run.message || run.error) && <div className="plugin-run-msg">{run.message || run.error}</div>}
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}

export function Plugins({ api, projects, onNotice }: { api: SettingsApi; projects: Project[]; onNotice(text: string, error?: boolean): void }) {
  useLocale();
  const [rows, setRows] = useState<PluginRow[]>([]);
  const [local, setLocal] = useState<Local>({ agents: [], mcp_servers: [] });
  const [loaded, setLoaded] = useState(false);
  const [kind, setKind] = useState<"path" | "git" | "source">("git");
  const [path, setPath] = useState("");
  const [gitURL, setGitURL] = useState("");
  const [ref, setRef] = useState("");
  const [preview, setPreview] = useState<{ data: Preview; mode: Mode } | null>(null);
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  const [sources, setSources] = useState<Source[]>([]);
  const [listings, setListings] = useState<Listing[]>([]);
  const [problems, setProblems] = useState<string[]>([]);
  const [query, setQuery] = useState("");
  const [sourceName, setSourceName] = useState("");
  const [sourceURL, setSourceURL] = useState("");

  async function load() {
    try {
      const body = await api.request<{ plugins: PluginRow[]; local: Local }>("/plugins");
      setRows(body.plugins || []);
      setLocal(body.local || { agents: [], mcp_servers: [] });
      const src = await api.request<{ sources: Source[] }>("/plugin-sources");
      setSources(src.sources || []);
    } catch (error) {
      onNotice(errorText(error), true);
    } finally {
      setLoaded(true);
    }
  }
  useEffect(() => void load(), []);

  async function changed() {
    await load();
    // Themes, quick commands and palette commands follow at once.
    void loadPluginContributions().catch(() => {});
  }

  async function runPreview(request: () => Promise<Preview>, mode: Mode) {
    setBusy(true);
    try {
      setPreview({ data: await request(), mode });
    } catch (error) {
      onNotice(errorText(error), true);
    } finally {
      setBusy(false);
    }
  }
  const previewSource = (body: Record<string, string>) =>
    runPreview(() => api.request<Preview>("/plugins/preview", { method: "POST", body }), "install");

  async function consent() {
    if (!preview) return;
    setBusy(true);
    try {
      await api.request("/plugins/install", { method: "POST", body: { hash: preview.data.hash, accept: preview.data.accept } });
      onNotice(preview.mode === "install" ? t("plugins.installed", { name: preview.data.manifest.name }) : t("plugins.updated", { name: preview.data.manifest.name }));
      setPreview(null);
      await changed();
    } catch (error) {
      onNotice(errorText(error), true);
    } finally {
      setBusy(false);
    }
  }

  async function toggle(row: PluginRow) {
    try {
      await api.request(`/plugins/${encodeURIComponent(row.id)}`, { method: "PUT", body: { enabled: !row.enabled } });
      onNotice(row.enabled ? t("plugins.disabledNotice", { name: row.name }) : t("plugins.enabledNotice", { name: row.name }));
      await changed();
    } catch (error) {
      onNotice(errorText(error), true);
    }
  }

  async function remove(row: PluginRow) {
    if (!window.confirm(t("plugins.removeConfirm", { name: row.name }))) return;
    try {
      await api.request(`/plugins/${encodeURIComponent(row.id)}`, { method: "DELETE" });
      onNotice(t("plugins.removed", { name: row.name }));
      await changed();
    } catch (error) {
      onNotice(errorText(error), true);
    }
  }

  async function search() {
    setBusy(true);
    try {
      const body = await api.request<{ plugins: Listing[]; problems: string[] }>(`/plugins/search?q=${encodeURIComponent(query)}&refresh=1`);
      setListings(body.plugins || []);
      setProblems(body.problems || []);
    } catch (error) {
      onNotice(errorText(error), true);
    } finally {
      setBusy(false);
    }
  }

  async function addSource() {
    setBusy(true);
    try {
      await api.request("/plugin-sources", { method: "POST", body: { name: sourceName.trim(), url: sourceURL.trim() } });
      setSourceName("");
      setSourceURL("");
      onNotice(t("plugins.sources.added"));
      await load();
    } catch (error) {
      onNotice(errorText(error), true);
    } finally {
      setBusy(false);
    }
  }

  const statusLabel = (row: PluginRow) => t("plugins.status." + row.status);
  return (
    <section className="plugins-page" aria-labelledby="plugins-heading">
      <h3 id="plugins-heading" data-setting="plugins.installed">{t("plugins.title")}</h3>
      <p className="plugins-intro">{t("plugins.intro")}</p>

      <div className="rowcard plugin-add" data-setting="plugins.add">
        <h4>{t("plugins.add.title")}</h4>
        <div className="segmented" role="radiogroup" aria-label={t("plugins.add.from")}>
          {(["git", "path", "source"] as const).map((value) => (
            <label key={value} className={kind === value ? "on" : ""}>
              <input type="radio" name="plugin-add-kind" value={value} checked={kind === value} onChange={() => setKind(value)} />
              {t("plugins.add.kind." + value)}
            </label>
          ))}
        </div>
        {kind === "git" && (
          <form className="plugin-add-form" onSubmit={(event) => { event.preventDefault(); void previewSource({ kind: "git", url: gitURL.trim(), ref: ref.trim() }); }}>
            <label>{t("plugins.add.gitURL")}<input id="plugin-git-url" inputMode="url" autoCapitalize="off" spellCheck={false} placeholder="https://github.com/owner/plugin.git" value={gitURL} onChange={(event) => setGitURL(event.target.value)} /></label>
            <label>{t("plugins.add.ref")}<input id="plugin-git-ref" autoCapitalize="off" spellCheck={false} placeholder="main" value={ref} onChange={(event) => setRef(event.target.value)} /></label>
            <button className="b" id="plugin-preview-git" disabled={busy || !gitURL.trim()}>{t("plugins.add.preview")}</button>
          </form>
        )}
        {kind === "path" && (
          <form className="plugin-add-form" onSubmit={(event) => { event.preventDefault(); void previewSource({ kind: "path", path: path.trim() }); }}>
            <label>{t("plugins.add.path")}<input id="plugin-path" autoCapitalize="off" spellCheck={false} placeholder="/home/you/my-plugin" value={path} onChange={(event) => setPath(event.target.value)} /></label>
            <p className="personal-hint">{t("plugins.add.pathHint")}</p>
            <button className="b" id="plugin-preview-path" disabled={busy || !path.trim()}>{t("plugins.add.preview")}</button>
          </form>
        )}
        {kind === "source" && (
          <div className="plugin-available" data-setting="plugins.sources">
            {!sources.length && <p className="personal-hint">{t("plugins.sources.none")}</p>}
            <form className="plugin-search" onSubmit={(event) => { event.preventDefault(); void search(); }}>
              <input id="plugin-search" type="search" aria-label={t("plugins.sources.search")} placeholder={t("plugins.sources.search")} value={query} onChange={(event) => setQuery(event.target.value)} />
              <button className="b" disabled={busy || !sources.length}>{t("plugins.sources.searchButton")}</button>
            </form>
            {problems.map((problem) => <p className="plugin-warning" key={problem}>{problem}</p>)}
            {listings.map((row) => (
              <div className="plugin-listing" key={row.index + row.id}>
                <div>
                  <b>{row.name || row.id}</b> <code>{row.id}</code> {row.version}
                  {row.description && <div className="plugin-desc">{row.description}</div>}
                  <div className="plugin-desc">{t("plugins.sources.listedIn", { source: row.index, commit: short(row.commit) })}</div>
                </div>
                <button type="button" className="b" disabled={busy} onClick={() => void previewSource({ kind: "index", id: row.id, index: row.index })}>
                  {row.installed ? t("plugins.sources.reinstall") : t("plugins.add.preview")}
                </button>
              </div>
            ))}
            <h4>{t("plugins.sources.title")}</h4>
            <ul className="plugin-sources">
              {sources.map((s) => (
                <li key={s.name}>
                  <b>{s.name}</b> <code>{s.url}</code>
                  <button type="button" className="b" aria-label={t("plugins.sources.remove", { name: s.name })} onClick={() => void api.request(`/plugin-sources/${encodeURIComponent(s.name)}`, { method: "DELETE" }).then(load, (error) => onNotice(errorText(error), true))}>
                    {t("plugins.remove")}
                  </button>
                </li>
              ))}
            </ul>
            <form className="plugin-add-form" onSubmit={(event) => { event.preventDefault(); void addSource(); }}>
              <label>{t("plugins.sources.name")}<input id="plugin-source-name" autoCapitalize="off" spellCheck={false} value={sourceName} onChange={(event) => setSourceName(event.target.value)} /></label>
              <label>{t("plugins.sources.url")}<input id="plugin-source-url" inputMode="url" autoCapitalize="off" spellCheck={false} placeholder="https://github.com/owner/marketplace.git" value={sourceURL} onChange={(event) => setSourceURL(event.target.value)} /></label>
              <button className="b" disabled={busy || !sourceName.trim() || !sourceURL.trim()}>{t("plugins.sources.add")}</button>
            </form>
          </div>
        )}
      </div>

      {!loaded && <p className="plugin-loading">{t("plugins.loading")}</p>}
      <div className="plugin-list">
        {rows.map((row) => (
          <article className={"rowcard plugin-card status-" + row.status} key={row.id} data-plugin={row.id}>
            <header className="plugin-card-head">
              <h4>{row.name}</h4>
              {row.version && <span className="plugin-version">{row.version}</span>}
              {row.bundled && <span className="plugin-badge">{t("plugins.bundled")}</span>}
              <span className={"plugin-status " + row.status}>{statusLabel(row)}</span>
            </header>
            {row.description && <p className="plugin-desc">{row.description}</p>}
            {row.problem && <p className="plugin-warning">{row.problem}</p>}
            <p className="plugin-chips"><Contributions counts={row.contributions} />
              {row.project_ids?.length ? <span className="plugin-chip scope">{t("plugins.scope.projects", { count: row.project_ids.length })}</span> : null}
            </p>
            <div className="btnrow">
              <button type="button" className="b" aria-expanded={open === row.id} onClick={() => setOpen(open === row.id ? null : row.id)}>
                {open === row.id ? t("plugins.hideDetails") : t("plugins.details")}
              </button>
              {(row.status === "active" || row.status === "disabled") && (
                <button type="button" className="b plugin-toggle" aria-pressed={row.enabled} onClick={() => void toggle(row)}>
                  {row.enabled ? t("plugins.disable") : t("plugins.enable")}
                </button>
              )}
              {row.status === "modified" && (
                <button type="button" className="b plugin-trust" disabled={busy} onClick={() => void runPreview(() => api.request<Preview>(`/plugins/${encodeURIComponent(row.id)}/trust`, { method: "POST" }), "trust")}>
                  {t("plugins.trustAgain")}
                </button>
              )}
              {!row.bundled && (
                <button type="button" className="b plugin-update" disabled={busy} onClick={() => void runPreview(() => api.request<Preview>(`/plugins/${encodeURIComponent(row.id)}/update`, { method: "POST", body: {} }), "update")}>
                  {t("plugins.update")}
                </button>
              )}
              {!row.bundled && (
                <button type="button" className="b plugin-remove" onClick={() => void remove(row)}>{t("plugins.remove")}</button>
              )}
            </div>
            {open === row.id && <Detail api={api} id={row.id} projects={projects} onChanged={() => void changed()} onNotice={onNotice} />}
          </article>
        ))}
      </div>

      <div className="rowcard plugin-local" data-setting="plugins.local">
        <h4>{t("plugins.local.title")}</h4>
        <p className="personal-hint">{t("plugins.local.intro")}</p>
        {!local.agents.length && !local.mcp_servers.length && <p className="plugin-desc">{t("plugins.local.none")}</p>}
        {!!local.agents.length && (
          <div className="plugin-detail-row"><b>{t("plugins.local.agents")}</b><span>{local.agents.join(", ")}</span></div>
        )}
        {local.mcp_servers.map((row) => (
          <div className="plugin-detail-row" key={row.project_id}>
            <b>{t("plugins.local.mcp", { project: row.project })}</b>
            <span>{row.servers.join(", ")}</span>
          </div>
        ))}
      </div>

      {preview && <ConsentDialog preview={preview.data} mode={preview.mode} busy={busy} onConsent={() => void consent()} onClose={() => setPreview(null)} />}
    </section>
  );
}
