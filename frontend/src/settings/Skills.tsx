import { useEffect, useRef, useState } from "react";
import type { SettingsApi } from "./Settings";
import { t, useLocale } from "../i18n";
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
// Agents whose CLI reads Agent Skills; mirrors internal/skills.SupportedAgents.
// Claude Code reads .claude/skills, the rest the shared .agents/skills.
const SKILL_PROVIDERS: [string, string][] = [
  ["claude", "Claude Code"],
  ["codex", "Codex"],
  ["gemini", "Gemini CLI"],
  ["qwen", "Qwen Code"],
  ["opencode", "OpenCode"],
  ["copilot", "GitHub Copilot CLI"],
];

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
  useLocale();
  const [agent, setAgent] = useState(
      SKILL_PROVIDERS.some(([name]) => name === defaultAgent) ? defaultAgent : "claude",
    ),
    [catalog, setCatalog] = useState<Skill[]>([]),
    [attached, setAttached] = useState<Attachment[]>([]),
    [query, setQuery] = useState(""),
    [busy, setBusy] = useState(false),
    [status, setStatus] = useState(() => t("agentSettings.skills.loading")),
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
      setStatus(t("agentSettings.skills.summary", { available: ordinarySkills.length, attached: ordinaryAttachments.length }));
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
      setStatus(t("agentSettings.skills.attachedStatus"));
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e);
      setStatus(message.includes("destination already exists") ? t("agentSettings.skills.destinationOccupied", { message }) : message);
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
      setStatus(t("agentSettings.skills.detachedStatus"));
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
    <section className="project-skills" data-setting="projects.skills">
      <h4>{t("agentSettings.skills.title")}</h4>
      <p>
        {t("agentSettings.skills.intro")}
      </p>
      <p className="skills-status" role="status">{status}</p>
      <label>
        {t("agentSettings.skills.provider")}
        <select className="skills-agent"
          aria-label={t("agentSettings.skills.providerLabel")}
          value={agent}
          onChange={(e) => setAgent(e.target.value)}
        >
          {SKILL_PROVIDERS.map(([name, label]) => (
            <option key={name} value={name}>
              {label}
            </option>
          ))}
        </select>
      </label>
      <button className="skills-reload" disabled={busy} onClick={() => void load()}>
        {t("agentSettings.skills.reload")}
      </button>
      <label>
        {t("agentSettings.skills.searchCatalog")}
        <input className="skills-search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("agentSettings.skills.searchPlaceholder")}
        />
      </label>
      <h5>{t("agentSettings.skills.attachedHeading")}</h5>
      {!ordinaryAttached.length && <p>{t("agentSettings.skills.noneAttached")}</p>}
      {ordinaryAttached.map((a) => (
        <div className="attached-skill" key={a.id}>
          <b>{a.entry_name || a.skill_id}</b>
          <small>
            {a.source_id} · {a.skill_id}
          </small>
          <button className="skill-detach" disabled={busy} onClick={() => void detach(a.id)}>
            {t("agentSettings.skills.detach")}
          </button>
        </div>
      ))}
      <h5>{t("agentSettings.skills.available")}</h5>
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
              {ids.has(s.id) ? t("agentSettings.skills.attachedButton") : t("agentSettings.skills.attach")}
            </button>
          </div>
        ))}
      <details className="skills-sources">
        <summary>{t("agentSettings.skills.sourceDirs")}</summary>
        <textarea className="skills-source-input" aria-label={t("agentSettings.skills.sourceDirs")} value={sourceText} onChange={(e) => { onSourceText(e.target.value); setSourceStatus(t("agentSettings.skills.unsavedDirs")); }} />
        <button className="skills-source-clear" onClick={() => { onSourceText(""); setSourceStatus(t("agentSettings.skills.unsavedDirs")); }}>{t("agentSettings.skills.clear")}</button>
        <button className="skills-source-save" onClick={() => void api.request(`/projects/${projectId}`, { method: "PATCH", body: { skill_sources: sourceText.split("\n").map((x) => x.trim()).filter(Boolean) } }).then(() => setSourceStatus(t("agentSettings.skills.dirsSaved"))).catch((e) => setSourceStatus(e instanceof Error ? e.message : String(e)))}>{t("agentSettings.skills.saveDirs")}</button>
        <p className="skills-source-status" role="status">{sourceStatus}</p>
      </details>
    </section>
  );
}
