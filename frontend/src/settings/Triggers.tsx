import { useEffect, useState } from "react";
import type { JsonValue } from "../api";
import type { SettingsApi } from "./Settings";

// A trigger source is how a project picks up work on its own — a labelled
// GitHub issue, a Slack mention, a labelled Linear issue — instead of a
// human always pressing dispatch. See internal/triggers and docs/triggers.md.

interface TriggerSource {
  id: number;
  kind: "github" | "slack" | "linear";
  name: string;
  enabled: boolean;
  config: Record<string, unknown>;
  secrets: Record<string, boolean>;
  interval_s: number;
  status: string;
  last_poll_at?: number;
  last_error: string;
}

interface TriggerEvent {
  id: number;
  source_id: number;
  external_id: string;
  kind: string;
  author: string;
  summary: string;
  action: string;
  reason: string;
  task_id?: number;
  task_title?: string;
  task_status?: string;
  postback_status: string;
  created_at: number;
}

const KIND_LABEL: Record<TriggerSource["kind"], string> = {
  github: "GitHub",
  slack: "Slack",
  linear: "Linear",
};

const DEFAULT_CONFIG: Record<TriggerSource["kind"], string> = {
  github: JSON.stringify({ repo: "owner/repo", label: "lectern", mention_handle: "@lectern", allowed_authors: [] }, null, 2),
  slack: JSON.stringify({ channel: "", allowed_users: [] }, null, 2),
  linear: JSON.stringify({ team_key: "ENG", label: "lectern", allowed_users: [] }, null, 2),
};

const DEFAULT_SECRETS: Record<TriggerSource["kind"], string> = {
  github: "{}",
  slack: JSON.stringify({ app_token: "", bot_token: "" }, null, 2),
  linear: JSON.stringify({ api_key: "" }, null, 2),
};

export function timeAgo(sec?: number): string {
  if (!sec) return "never";
  const delta = Date.now() / 1000 - sec;
  if (delta < 60) return "just now";
  if (delta < 3600) return `${Math.floor(delta / 60)}m ago`;
  if (delta < 86400) return `${Math.floor(delta / 3600)}h ago`;
  return `${Math.floor(delta / 86400)}d ago`;
}

export function Triggers({
  api,
  projectId,
  onNotice,
}: {
  api: SettingsApi;
  projectId: number;
  onNotice(t: string, e?: boolean): void;
}) {
  const [sources, setSources] = useState<TriggerSource[]>([]);
  const [events, setEvents] = useState<TriggerEvent[]>([]);
  const [busy, setBusy] = useState(false);
  const [newKind, setNewKind] = useState<TriggerSource["kind"]>("github");
  const [testResults, setTestResults] = useState<Record<number, string>>({});
  const [open, setOpen] = useState<Record<number, boolean>>({});
  const [drafts, setDrafts] = useState<Record<number, { name: string; config: string; secrets: string; interval: string }>>({});

  async function load() {
    try {
      const [s, e] = await Promise.all([
        api.request<TriggerSource[]>(`/projects/${projectId}/triggers`),
        api.request<TriggerEvent[]>(`/projects/${projectId}/trigger-events`),
      ]);
      setSources(s);
      setEvents(e);
    } catch (error) {
      onNotice(`Triggers: ${(error as Error).message}`, true);
    }
  }
  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId]);

  function draftFor(s: TriggerSource) {
    return (
      drafts[s.id] || {
        name: s.name,
        config: JSON.stringify(s.config, null, 2),
        secrets: DEFAULT_SECRETS[s.kind],
        interval: String(s.interval_s),
      }
    );
  }

  async function addSource() {
    setBusy(true);
    try {
      await api.request(`/projects/${projectId}/triggers`, {
        method: "POST",
        body: { kind: newKind, name: KIND_LABEL[newKind], config: JSON.parse(DEFAULT_CONFIG[newKind]), secrets: {} },
      });
      onNotice(`${KIND_LABEL[newKind]} trigger added — configure it below`);
      await load();
    } catch (error) {
      onNotice((error as Error).message, true);
    } finally {
      setBusy(false);
    }
  }

  async function toggle(s: TriggerSource) {
    try {
      await api.request(`/triggers/${s.id}`, { method: "PATCH", body: { enabled: !s.enabled } });
      await load();
    } catch (error) {
      onNotice((error as Error).message, true);
    }
  }

  async function save(s: TriggerSource) {
    const d = draftFor(s);
    let config, secrets: Record<string, JsonValue>;
    try {
      config = JSON.parse(d.config);
    } catch {
      onNotice("Config must be valid JSON", true);
      return;
    }
    try {
      secrets = d.secrets.trim() ? JSON.parse(d.secrets) : {};
    } catch {
      onNotice("Secrets must be valid JSON", true);
      return;
    }
    setBusy(true);
    try {
      // An empty secrets draft means "leave the stored secrets alone" — the
      // field always renders blank (see draftFor), so only a non-empty edit
      // is ever sent back.
      const secretsChanged = d.secrets.trim() && Object.values(secrets).some((v) => String(v).trim());
      const interval = Number(d.interval);
      await api.request(`/triggers/${s.id}`, {
        method: "PATCH",
        body: {
          name: d.name,
          config,
          ...(interval > 0 ? { interval_s: interval } : {}),
          ...(secretsChanged ? { secrets } : {}),
        },
      });
      onNotice(`${s.name || KIND_LABEL[s.kind]} saved`);
      await load();
    } catch (error) {
      onNotice((error as Error).message, true);
    } finally {
      setBusy(false);
    }
  }

  async function remove(s: TriggerSource) {
    if (!confirm(`Delete the ${KIND_LABEL[s.kind]} trigger "${s.name || s.kind}"?`)) return;
    try {
      await api.request(`/triggers/${s.id}`, { method: "DELETE" });
      await load();
    } catch (error) {
      onNotice((error as Error).message, true);
    }
  }

  async function test(s: TriggerSource) {
    setTestResults((r) => ({ ...r, [s.id]: "Testing…" }));
    try {
      const res = await api.request<{ ok: boolean; message?: string; error?: string }>(`/triggers/${s.id}/test`, { method: "POST" });
      setTestResults((r) => ({ ...r, [s.id]: res.ok ? `✓ ${res.message}` : `✗ ${res.error}` }));
    } catch (error) {
      setTestResults((r) => ({ ...r, [s.id]: `✗ ${(error as Error).message}` }));
    }
  }

  return (
    <section className="project-triggers">
      <h4>Triggers</h4>
      <p>
        Let this project pick up work on its own: a labelled GitHub issue or an @mention, a Slack
        message or /lectern command, or a labelled Linear issue. Every source needs an author
        allowlist before it can act — see docs/triggers.md.
      </p>
      <div className="trigger-add">
        <select aria-label="Trigger kind" value={newKind} onChange={(e) => setNewKind(e.target.value as TriggerSource["kind"])} disabled={busy}>
          <option value="github">GitHub</option>
          <option value="slack">Slack</option>
          <option value="linear">Linear</option>
        </select>
        <button onClick={() => void addSource()} disabled={busy}>
          Add trigger
        </button>
      </div>
      {sources.length === 0 && <p>No triggers configured for this project yet.</p>}
      {sources.map((s) => {
        const d = draftFor(s);
        const isOpen = open[s.id] ?? false;
        return (
          <article className="trigger-source-row" key={s.id}>
            <div className="trigger-source-head">
              <b>{KIND_LABEL[s.kind]}</b>
              <span className="trigger-source-name">{s.name || "(unnamed)"}</span>
              <span className={`trigger-status trigger-status-${s.status}`}>{s.status}</span>
              <span className="trigger-last-poll">last poll: {timeAgo(s.last_poll_at)}</span>
            </div>
            {s.last_error && <p className="trigger-error" role="status">{s.last_error}</p>}
            <div className="trigger-source-actions">
              <label>
                <input type="checkbox" checked={s.enabled} onChange={() => void toggle(s)} /> Enabled
              </label>
              <button onClick={() => setOpen((o) => ({ ...o, [s.id]: !isOpen }))}>{isOpen ? "Hide settings" : "Edit"}</button>
              <button onClick={() => void test(s)}>Test connection</button>
              <button className="trigger-delete" onClick={() => void remove(s)}>
                Delete
              </button>
            </div>
            {testResults[s.id] && <p className="trigger-test-result" role="status">{testResults[s.id]}</p>}
            {isOpen && (
              <div className="trigger-source-edit">
                <label>
                  Name
                  <input value={d.name} onChange={(e) => setDrafts((ds) => ({ ...ds, [s.id]: { ...d, name: e.target.value } }))} />
                </label>
                <label>
                  Poll interval (seconds, GitHub/Linear only)
                  <input value={d.interval} onChange={(e) => setDrafts((ds) => ({ ...ds, [s.id]: { ...d, interval: e.target.value } }))} />
                </label>
                <label>
                  Config (JSON)
                  <textarea
                    className="trigger-config"
                    value={d.config}
                    onChange={(e) => setDrafts((ds) => ({ ...ds, [s.id]: { ...d, config: e.target.value } }))}
                  />
                </label>
                <label>
                  Secrets (JSON — {Object.entries(s.secrets).filter(([, v]) => v).length} of {Object.keys(s.secrets).length || 0} set;
                  leave blank to keep them unchanged)
                  <textarea
                    className="trigger-secrets"
                    placeholder={DEFAULT_SECRETS[s.kind]}
                    value={d.secrets === DEFAULT_SECRETS[s.kind] ? "" : d.secrets}
                    onChange={(e) => setDrafts((ds) => ({ ...ds, [s.id]: { ...d, secrets: e.target.value } }))}
                  />
                </label>
                <button onClick={() => void save(s)} disabled={busy}>
                  Save
                </button>
              </div>
            )}
          </article>
        );
      })}
      <h4>Recent events</h4>
      {events.length === 0 && <p>No events yet.</p>}
      <ul className="trigger-events">
        {events.map((e) => (
          <li className="trigger-event-row" key={e.id}>
            <span className="trigger-event-time">{timeAgo(e.created_at)}</span>
            <span className="trigger-event-kind">{e.kind}</span>
            <span className="trigger-event-author">{e.author || "—"}</span>
            <span className={`trigger-event-action trigger-event-${e.action}`}>
              {e.action === "task_created" && e.task_id
                ? `→ task #${e.task_id} (${e.task_status || "?"})`
                : e.reason || e.action}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}
