import { useCallback, useEffect, useState } from "react";
import { t, useLocale } from "../i18n";
import { errorText, type Notice, type TrackerApi } from "./api";
import { isForge, SOURCE_NAME } from "./logic";
import type { Source, TrackerConnection, TrackersResponse } from "./types";

type Draft = Record<string, string>;

// Placeholders are sample values (repository paths, hosts, keys, a JQL query) and stay as they are.
const FIELDS = (): Record<Source, { key: string; label: string; secret?: boolean; placeholder?: string; hint?: string; options?: string[] }[]> => ({
  github: [
    { key: "repo", label: t("trackers.settings.field.repository"), placeholder: "owner/repo", hint: t("trackers.settings.hint.originRemote") },
    { key: "host", label: t("trackers.settings.field.host"), placeholder: "github.com", hint: t("trackers.settings.hint.githubEnterprise") },
  ],
  gitlab: [
    { key: "repo", label: t("trackers.settings.field.projectPath"), placeholder: "group/subgroup/project", hint: t("trackers.settings.hint.originRemote") },
    { key: "host", label: t("trackers.settings.field.host"), placeholder: "gitlab.com", hint: t("trackers.settings.hint.gitlabSelfManaged") },
  ],
  bitbucket: [
    { key: "repo", label: t("trackers.settings.field.repository"), placeholder: t("trackers.settings.placeholder.bitbucketRepo"), hint: t("trackers.settings.hint.originRemote") },
    { key: "host", label: t("trackers.settings.field.host"), placeholder: "bitbucket.org", hint: t("trackers.settings.hint.bitbucketHost") },
    { key: "flavor", label: t("trackers.settings.field.deployment"), options: ["", "cloud", "server"], hint: t("trackers.settings.hint.bitbucketDeployment") },
    { key: "base_url", label: t("trackers.settings.field.apiUrl"), placeholder: "https://bitbucket.example.com", hint: t("trackers.settings.hint.bitbucketApiUrl") },
    { key: "username", label: t("trackers.settings.field.cloudAccount"), placeholder: "you@example.com", hint: t("trackers.settings.hint.bitbucketAccount") },
    { key: "token", label: t("trackers.settings.field.token"), secret: true, hint: t("trackers.settings.hint.bitbucketToken") },
  ],
  gitea: [
    { key: "host", label: t("trackers.settings.field.host"), placeholder: "codeberg.org" },
    { key: "repo", label: t("trackers.settings.field.repository"), placeholder: "owner/repo", hint: t("trackers.settings.hint.originRemote") },
    { key: "base_url", label: t("trackers.settings.field.apiUrl"), placeholder: "https://git.example.com/api/v1" },
    { key: "token", label: t("trackers.settings.field.accessToken"), secret: true, hint: t("trackers.settings.hint.giteaToken") },
  ],
  azure: [
    { key: "repo", label: t("trackers.settings.field.repository"), placeholder: "organisation/project/repo", hint: t("trackers.settings.hint.originRemote") },
    { key: "base_url", label: t("trackers.settings.field.serverUrl"), placeholder: "https://devops.example.com/tfs", hint: t("trackers.settings.hint.azureServerUrl") },
    { key: "token", label: t("trackers.settings.field.pat"), secret: true, hint: t("trackers.settings.hint.azureToken") },
  ],
  linear: [
    { key: "team_key", label: t("trackers.settings.field.teamKey"), placeholder: "ENG", hint: t("trackers.settings.hint.teamKey") },
    { key: "api_key", label: t("trackers.settings.field.apiKey"), secret: true, placeholder: "lin_api_…", hint: t("trackers.settings.hint.linearKey") },
  ],
  jira: [
    { key: "base_url", label: t("trackers.settings.field.siteUrl"), placeholder: "https://yourteam.atlassian.net" },
    { key: "flavor", label: t("trackers.settings.field.deployment"), options: ["", "cloud", "server"], hint: t("trackers.settings.hint.deployment") },
    { key: "email", label: t("trackers.settings.field.email"), placeholder: "you@example.com", hint: t("trackers.settings.hint.email") },
    { key: "token", label: t("trackers.settings.field.token"), secret: true, hint: t("trackers.settings.hint.token") },
    { key: "project_key", label: t("trackers.settings.field.projectKey"), placeholder: "OPS" },
    { key: "jql", label: t("trackers.settings.field.jql"), placeholder: "project = OPS AND component = api", hint: t("trackers.settings.hint.jql") },
  ],
});

const SECRET_KEYS = new Set(["api_key", "token"]);

// The flavor select's option labels, per kind.
const FLAVOR = (): Record<string, string> => ({
  "": t("trackers.settings.flavor.guessHost"),
  cloud: t("trackers.settings.flavor.cloudAny"),
  server: t("trackers.settings.flavor.server"),
});

/** A project's tracker connections for the Tasks hub. Keys and tokens are
 * write-only here: the server only ever reports whether one is set. */
export function TrackerSettings({ api, projectId, onNotice }: { api: TrackerApi; projectId: number; onNotice: Notice }) {
  useLocale();
  const [data, setData] = useState<TrackersResponse>();
  const [adding, setAdding] = useState<Source | "">("");
  const [draft, setDraft] = useState<Draft>({});
  const [editing, setEditing] = useState<number>();
  const [busy, setBusy] = useState(false);
  const [tests, setTests] = useState<Record<number, string>>({});

  const load = useCallback(() => {
    api
      .request<TrackersResponse>(`/projects/${projectId}/trackers`)
      .then(setData)
      .catch((e) => onNotice(t("trackers.settings.loadFailed", { error: errorText(e) }), true));
  }, [api, projectId, onNotice]);
  useEffect(load, [load]);

  function split(kind: Source, d: Draft) {
    const config: Record<string, string> = {},
      secrets: Record<string, string> = {};
    for (const f of FIELDS()[kind]) {
      const v = (d[f.key] || "").trim();
      if (SECRET_KEYS.has(f.key)) {
        if (v) secrets[f.key] = v;
      } else if (v) config[f.key] = v;
    }
    return { config, secrets };
  }

  async function submit(kind: Source, id?: number) {
    setBusy(true);
    try {
      const { config, secrets } = split(kind, draft);
      if (id) await api.request(`/trackers/${id}`, { method: "PATCH", body: { config, secrets } });
      else await api.request(`/projects/${projectId}/trackers`, { method: "POST", body: { kind, name: draft.name || SOURCE_NAME[kind], config, secrets } });
      onNotice(id ? t("trackers.settings.saved", { source: SOURCE_NAME[kind] }) : t("trackers.settings.connected", { source: SOURCE_NAME[kind] }));
      setAdding("");
      setEditing(undefined);
      setDraft({});
      load();
    } catch (e) {
      onNotice(errorText(e), true);
    } finally {
      setBusy(false);
    }
  }

  async function test(c: TrackerConnection) {
    setTests((prev) => ({ ...prev, [c.id]: t("trackers.settings.testing") }));
    try {
      const r = await api.request<{ ok: boolean; message?: string; error?: string }>(`/trackers/${c.id}/test`, { method: "POST" });
      setTests((prev) => ({ ...prev, [c.id]: r.ok ? `✓ ${r.message}` : `✗ ${r.error}` }));
    } catch (e) {
      setTests((prev) => ({ ...prev, [c.id]: `✗ ${errorText(e)}` }));
    }
  }

  async function remove(c: TrackerConnection) {
    if (!confirm(t("trackers.settings.confirmDisconnect", { name: c.name }))) return;
    try {
      await api.request(`/trackers/${c.id}`, { method: "DELETE" });
      load();
    } catch (e) {
      onNotice(errorText(e), true);
    }
  }

  function form(kind: Source, id?: number, secrets?: Record<string, boolean>) {
    return (
      <form
        className="th-conn-form"
        onSubmit={(e) => {
          e.preventDefault();
          void submit(kind, id);
        }}
      >
        {!id && (
          <label>
            {t("trackers.settings.name")}
            <input value={draft.name || ""} placeholder={SOURCE_NAME[kind]} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
          </label>
        )}
        {FIELDS()[kind].map((f) => (
          <label key={f.key}>
            {f.label}
            {f.options ? (
              <select value={draft[f.key] || ""} onChange={(e) => setDraft({ ...draft, [f.key]: e.target.value })}>
                {f.options.map((o) => (
                  <option key={o} value={o}>
                    {FLAVOR()[o]}
                  </option>
                ))}
              </select>
            ) : (
              <input
                type={f.secret ? "password" : "text"}
                autoComplete={f.secret ? "new-password" : "off"}
                spellCheck={false}
                value={draft[f.key] || ""}
                placeholder={f.secret && secrets?.[f.key] ? t("trackers.settings.secretSet") : f.placeholder}
                onChange={(e) => setDraft({ ...draft, [f.key]: e.target.value })}
              />
            )}
            {f.hint && <small className="th-muted">{f.hint}</small>}
          </label>
        ))}
        <div className="btnrow">
          <button className="b ok" type="submit" disabled={busy}>
            {id ? t("trackers.settings.save") : t("trackers.settings.connect")}
          </button>
          <button className="b" type="button" onClick={() => { setAdding(""); setEditing(undefined); setDraft({}); }}>
            {t("trackers.cancel")}
          </button>
        </div>
      </form>
    );
  }

  const forge = data?.forge;
  return (
    <section className="project-trackers">
      <h4>{t("trackers.settings.title")}</h4>
      <p>
        {t("trackers.settings.intro1")} <code>gh</code> / <code>glab</code> {t("trackers.settings.intro2Tokens")}
      </p>
      <p className="th-forge-line">
        {forge?.repo ? (
          <>
            {SOURCE_NAME[forge.kind || "github"]} · <b>{forge.repo}</b> <span className="th-muted">({forge.source === "connection" ? t("trackers.settings.setHere") : t("trackers.settings.fromOrigin")})</span>
          </>
        ) : (
          <span className="th-muted">{forge?.error || t("trackers.settings.checking")}</span>
        )}
      </p>
      <ul className="th-conns">
        {(data?.connections || []).map((c) => (
          <li key={c.id}>
            <div className="th-conn-head">
              <b>{SOURCE_NAME[c.kind]}</b> <span>{c.name}</span>
              <span className="th-muted">
                {Object.entries(c.config)
                  .filter(([k, v]) => v && k !== "api_url")
                  .map(([k, v]) => `${k}: ${String(v)}`)
                  .join(" · ")}
              </span>
              {Object.entries(c.secrets).map(([k, set]) => (
                <span key={k} className={`th-badge ${set ? "th-review-approved" : ""}`}>
                  {set ? t("trackers.settings.secretIsSet", { name: k }) : t("trackers.settings.secretNotSet", { name: k })}
                </span>
              ))}
              {c.borrowed && <span className="th-badge">{t("trackers.settings.borrowed")}</span>}
            </div>
            <div className="btnrow">
              <button className="b" onClick={() => void test(c)}>{t("trackers.settings.test")}</button>
              <button className="b" onClick={() => { setEditing(c.id); setAdding(""); setDraft(Object.fromEntries(Object.entries(c.config).map(([k, v]) => [k, String(v ?? "")]))); }}>
                {t("trackers.settings.edit")}
              </button>
              <button className="b no" onClick={() => void remove(c)}>{t("trackers.settings.disconnect")}</button>
            </div>
            {tests[c.id] && <p className="th-muted" role="status">{tests[c.id]}</p>}
            {editing === c.id && form(c.kind, c.id, c.secrets)}
          </li>
        ))}
      </ul>
      {adding ? (
        form(adding)
      ) : (
        <div className="btnrow">
          {(["linear", "jira", "github", "gitlab", "bitbucket", "gitea", "azure"] as Source[]).map((k) => (
            <button key={k} className="b" onClick={() => { setAdding(k); setEditing(undefined); setDraft({}); }}>
              {isForge(k)
                ? t("trackers.settings.addRepository", { source: SOURCE_NAME[k] })
                : t("trackers.settings.addSource", { source: SOURCE_NAME[k] })}
            </button>
          ))}
        </div>
      )}
    </section>
  );
}
