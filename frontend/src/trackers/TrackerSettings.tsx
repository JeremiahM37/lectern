import { useCallback, useEffect, useState } from "react";
import { errorText, type Notice, type TrackerApi } from "./api";
import { isForge, SOURCE_NAME } from "./logic";
import type { Source, TrackerConnection, TrackersResponse } from "./types";

type Draft = Record<string, string>;

const FIELDS: Record<Source, { key: string; label: string; secret?: boolean; placeholder?: string; hint?: string; options?: string[] }[]> = {
  github: [
    { key: "repo", label: "Repository", placeholder: "owner/repo", hint: "Leave empty to use the clone's origin remote." },
    { key: "host", label: "Host", placeholder: "github.com", hint: "Only for GitHub Enterprise." },
  ],
  gitlab: [
    { key: "repo", label: "Project path", placeholder: "group/subgroup/project", hint: "Leave empty to use the clone's origin remote." },
    { key: "host", label: "Host", placeholder: "gitlab.com", hint: "For a self-managed GitLab." },
  ],
  bitbucket: [
    { key: "repo", label: "Repository", placeholder: "workspace/repo or PROJECT/repo", hint: "Leave empty to use the clone's origin remote." },
    { key: "host", label: "Host", placeholder: "bitbucket.org", hint: "For Bitbucket Data Center, its host name." },
    { key: "flavor", label: "Deployment", options: ["", "cloud", "server"], hint: "Empty guesses from the host: bitbucket.org is Cloud." },
    { key: "base_url", label: "API URL (optional)", placeholder: "https://bitbucket.example.com", hint: "Data Center behind a path prefix." },
    { key: "username", label: "Account (Cloud)", placeholder: "you@example.com", hint: "Cloud API token or app password: the account it belongs to. Leave empty for a repository/workspace access token." },
    { key: "token", label: "Token", secret: true, hint: "Cloud: API token, app password or access token. Data Center: an HTTP access token." },
  ],
  gitea: [
    { key: "host", label: "Host", placeholder: "codeberg.org" },
    { key: "repo", label: "Repository", placeholder: "owner/repo", hint: "Leave empty to use the clone's origin remote." },
    { key: "base_url", label: "API URL (optional)", placeholder: "https://git.example.com/api/v1" },
    { key: "token", label: "Access token", secret: true, hint: "Settings → Applications → a token with repository and issue scopes." },
  ],
  azure: [
    { key: "repo", label: "Repository", placeholder: "organisation/project/repo", hint: "Leave empty to use the clone's origin remote." },
    { key: "base_url", label: "Server URL (optional)", placeholder: "https://devops.example.com/tfs", hint: "Only for Azure DevOps Server." },
    { key: "token", label: "Personal access token", secret: true, hint: "Scopes: Code (read & write), Work Items (read & write)." },
  ],
  linear: [
    { key: "team_key", label: "Team key", placeholder: "ENG", hint: "The team the hub opens on." },
    { key: "api_key", label: "API key", secret: true, placeholder: "lin_api_…", hint: "Linear → Settings → API. Empty reuses this project's Linear trigger key." },
  ],
  jira: [
    { key: "base_url", label: "Site URL", placeholder: "https://yourteam.atlassian.net" },
    { key: "flavor", label: "Deployment", options: ["", "cloud", "server"], hint: "Empty guesses from the URL: *.atlassian.net is Cloud." },
    { key: "email", label: "Account email", placeholder: "you@example.com", hint: "Jira Cloud only: the account the API token belongs to." },
    { key: "token", label: "Token", secret: true, hint: "Cloud: an API token. Server / Data Center: a personal access token." },
    { key: "project_key", label: "Project key", placeholder: "OPS" },
    { key: "jql", label: "JQL (optional)", placeholder: "project = OPS AND component = api", hint: "Replaces the project filter." },
  ],
};

const SECRET_KEYS = new Set(["api_key", "token"]);

// The flavor select's option labels, per kind.
const FLAVOR: Record<string, string> = { "": "Guess from the host", cloud: "Cloud", server: "Server / Data Center" };

/** A project's tracker connections for the Tasks hub. Keys and tokens are
 * write-only here: the server only ever reports whether one is set. */
export function TrackerSettings({ api, projectId, onNotice }: { api: TrackerApi; projectId: number; onNotice: Notice }) {
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
      .catch((e) => onNotice(`Trackers: ${errorText(e)}`, true));
  }, [api, projectId, onNotice]);
  useEffect(load, [load]);

  function split(kind: Source, d: Draft) {
    const config: Record<string, string> = {},
      secrets: Record<string, string> = {};
    for (const f of FIELDS[kind]) {
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
      onNotice(`${SOURCE_NAME[kind]} ${id ? "saved" : "connected"}`);
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
    setTests((t) => ({ ...t, [c.id]: "Testing…" }));
    try {
      const r = await api.request<{ ok: boolean; message?: string; error?: string }>(`/trackers/${c.id}/test`, { method: "POST" });
      setTests((t) => ({ ...t, [c.id]: r.ok ? `✓ ${r.message}` : `✗ ${r.error}` }));
    } catch (e) {
      setTests((t) => ({ ...t, [c.id]: `✗ ${errorText(e)}` }));
    }
  }

  async function remove(c: TrackerConnection) {
    if (!confirm(`Disconnect ${c.name}?`)) return;
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
            Name
            <input value={draft.name || ""} placeholder={SOURCE_NAME[kind]} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
          </label>
        )}
        {FIELDS[kind].map((f) => (
          <label key={f.key}>
            {f.label}
            {f.options ? (
              <select value={draft[f.key] || ""} onChange={(e) => setDraft({ ...draft, [f.key]: e.target.value })}>
                {f.options.map((o) => (
                  <option key={o} value={o}>
                    {FLAVOR[o]}
                  </option>
                ))}
              </select>
            ) : (
              <input
                type={f.secret ? "password" : "text"}
                autoComplete={f.secret ? "new-password" : "off"}
                spellCheck={false}
                value={draft[f.key] || ""}
                placeholder={f.secret && secrets?.[f.key] ? "•••••• set — leave empty to keep" : f.placeholder}
                onChange={(e) => setDraft({ ...draft, [f.key]: e.target.value })}
              />
            )}
            {f.hint && <small className="th-muted">{f.hint}</small>}
          </label>
        ))}
        <div className="btnrow">
          <button className="b ok" type="submit" disabled={busy}>
            {id ? "Save" : "Connect"}
          </button>
          <button className="b" type="button" onClick={() => { setAdding(""); setEditing(undefined); setDraft({}); }}>
            Cancel
          </button>
        </div>
      </form>
    );
  }

  const forge = data?.forge;
  return (
    <section className="project-trackers">
      <h4>Tasks hub</h4>
      <p>
        Where this project's pull requests and issues come from. GitHub and GitLab use the <code>gh</code> / <code>glab</code> login on this
        project's machine; Bitbucket, Gitea/Forgejo, Azure DevOps, Linear and Jira need a token, which is kept on the server and never
        shown again.
      </p>
      <p className="th-forge-line">
        {forge?.repo ? (
          <>
            {SOURCE_NAME[forge.kind || "github"]} · <b>{forge.repo}</b> <span className="th-muted">({forge.source === "connection" ? "set here" : "from the origin remote"})</span>
          </>
        ) : (
          <span className="th-muted">{forge?.error || "Checking the repository…"}</span>
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
                  {k} {set ? "set" : "not set"}
                </span>
              ))}
              {c.borrowed && <span className="th-badge">key from trigger</span>}
            </div>
            <div className="btnrow">
              <button className="b" onClick={() => void test(c)}>Test</button>
              <button className="b" onClick={() => { setEditing(c.id); setAdding(""); setDraft(Object.fromEntries(Object.entries(c.config).map(([k, v]) => [k, String(v ?? "")]))); }}>
                Edit
              </button>
              <button className="b no" onClick={() => void remove(c)}>Disconnect</button>
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
              + {SOURCE_NAME[k]}
              {isForge(k) ? " repository" : ""}
            </button>
          ))}
        </div>
      )}
    </section>
  );
}
