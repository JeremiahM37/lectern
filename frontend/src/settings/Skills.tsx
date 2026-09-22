import { useEffect, useRef, useState } from "react";
import type { SettingsApi } from "./Settings";
interface Skill {
  id: string;
  name?: string;
  entry_name?: string;
  source?: string;
  description?: string;
}
interface Attachment {
  id: number;
  skill_id: string;
  entry_name?: string;
  source_id?: string;
}
export function Skills({
  api,
  projectId,
  defaultAgent,
  onNotice,
  sourceText,
  onSourceText,
}: {
  api: SettingsApi;
  projectId: number;
  defaultAgent: string;
  onNotice(t: string, e?: boolean): void;
  sourceText: string;
  onSourceText(v: string): void;
}) {
  const [agent, setAgent] = useState(
      defaultAgent === "codex" ? "codex" : "claude",
    ),
    [catalog, setCatalog] = useState<Skill[]>([]),
    [attached, setAttached] = useState<Attachment[]>([]),
    [query, setQuery] = useState(""),
    [busy, setBusy] = useState(false),
    [status, setStatus] = useState("Loading available skills…"),
    [sourceStatus, setSourceStatus] = useState("");
  const generation = useRef(0);
  async function load() {
    const mine = ++generation.current;
    setBusy(true);
    try {
      const [a, b] = await Promise.all([
        api.request<{ skills: Skill[] }>(
          `/skills?project_id=${projectId}&agent=${encodeURIComponent(agent)}`,
        ),
        api.request<{ attachments: Attachment[] }>(
          `/projects/${projectId}/skills?agent=${encodeURIComponent(agent)}`,
        ),
      ]);
      if (mine !== generation.current) return;
      setCatalog(a.skills || []);
      setAttached(b.attachments || []);
      const ordinarySkills = (a.skills || []).filter(
        (skill) => !skill.id.startsWith("lectern-workflow/") && skill.source !== "lectern-bundled",
      );
      const ordinaryAttachments = (b.attachments || []).filter(
        (attachment) => !attachment.source_id?.startsWith("lectern-bundled/"),
      );
      setStatus(`${ordinarySkills.length} available · ${ordinaryAttachments.length} attached`);
    } catch (e) {
      if (mine !== generation.current) return;
      setStatus(e instanceof Error ? e.message : String(e));
      onNotice(String(e), true);
    } finally {
      setBusy(false);
    }
  }
  useEffect(() => {
    void load();
  }, [agent, projectId]);
  async function attach(id: string) {
    setBusy(true);
    try {
      await api.request(`/projects/${projectId}/skills`, {
        method: "POST",
        body: { agent, skill_id: id },
      });
      await load();
      setStatus("Skill attached.");
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e);
      setStatus(message.includes("destination already exists") ? `Destination occupied: ${message}` : message);
      onNotice(String(e), true);
      setBusy(false);
    }
  }
  async function detach(id: number) {
    setBusy(true);
    try {
      await api.request(`/projects/${projectId}/skills/${id}`, {
        method: "DELETE",
      });
      await load();
      setStatus("Skill detached.");
    } catch (e) {
      onNotice(String(e), true);
      setBusy(false);
    }
  }
  const ordinaryAttached = attached.filter(
    (attachment) => !attachment.source_id?.startsWith("lectern-bundled/"),
  );
  const ids = new Set(ordinaryAttached.map((a) => a.skill_id));
  return (
    <section className="project-skills">
      <h4>Project skills</h4>
      <p>
        New launches use saved attachments; running processes are not restarted.
      </p>
      <p className="skills-status" role="status">{status}</p>
      <label>
        Provider
        <select className="skills-agent"
          aria-label="Skills provider"
          value={agent}
          onChange={(e) => setAgent(e.target.value)}
        >
          <option value="claude">Claude Code</option>
          <option value="codex">Codex</option>
        </select>
      </label>
      <button className="skills-reload" disabled={busy} onClick={() => void load()}>
        Reload
      </button>
      <label>
        Search catalog
        <input className="skills-search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="name, source, description"
        />
      </label>
      <h5>Attached</h5>
      {!ordinaryAttached.length && <p>No skills attached for this provider.</p>}
      {ordinaryAttached.map((a) => (
        <div className="attached-skill" key={a.id}>
          <b>{a.entry_name || a.skill_id}</b>
          <small>
            {a.source_id} · {a.skill_id}
          </small>
          <button className="skill-detach" disabled={busy} onClick={() => void detach(a.id)}>
            Detach
          </button>
        </div>
      ))}
      <h5>Available on target</h5>
      {catalog
        .filter(
          (s) =>
            !query ||
            `${s.name} ${s.entry_name} ${s.source} ${s.description} ${s.id}`
              .toLowerCase()
              .includes(query.toLowerCase()),
        )
        .map((s) => (
          <div className="catalog-skill" key={s.id}>
            <b>{s.name || s.entry_name || s.id}</b>
            <small>
              {s.source} · {s.description}
            </small>
            <button className="skill-attach"
              disabled={busy || ids.has(s.id)}
              onClick={() => void attach(s.id)}
            >
              {ids.has(s.id) ? "Attached" : "Attach"}
            </button>
          </div>
        ))}
      <details className="skills-sources">
        <summary>Skill source directories</summary>
        <textarea className="skills-source-input" aria-label="Skill source directories" value={sourceText} onChange={(e) => { onSourceText(e.target.value); setSourceStatus("Unsaved directory changes"); }} />
        <button className="skills-source-clear" onClick={() => { onSourceText(""); setSourceStatus("Unsaved directory changes"); }}>Clear</button>
        <button className="skills-source-save" onClick={() => void api.request(`/projects/${projectId}`, { method: "PATCH", body: { skill_sources: sourceText.split("\n").map((x) => x.trim()).filter(Boolean) } }).then(() => setSourceStatus("Directories saved. Reload the provider to discover them.")).catch((e) => setSourceStatus(e instanceof Error ? e.message : String(e)))}>Save directories</button>
        <p className="skills-source-status" role="status">{sourceStatus}</p>
      </details>
    </section>
  );
}
