import { useEffect, useRef, useState } from "react";
import { Modal } from "../sessions/Modal";
import type { JsonValue } from "../api";
import {
  draftFromPreset,
  draftFromProfile,
  instructionsHelp,
  parseEnvironment,
  profileRequestBody,
  type LaunchProfileRecord,
  type StarterPreset,
} from "./launchProfileForm";
import { t, useLocale } from "../i18n";

export type LaunchProfile = LaunchProfileRecord;
type Agent = { name: string };
type Api = { request<T>(path: string, options?: { method?: string; body?: JsonValue }): Promise<T> };

export function LaunchProfiles({ api, onClose, onChange = () => {} }: { api: Api; onClose(): void; onChange?(profile?: LaunchProfile): void }) {
  useLocale();
  const formRef = useRef<HTMLFormElement>(null);
  const [profiles, setProfiles] = useState<LaunchProfile[]>([]), [agents, setAgents] = useState<Agent[]>([]), [presets, setPresets] = useState<StarterPreset[]>([]);
  const [selected, setSelected] = useState(0), [name, setName] = useState(""), [agent, setAgent] = useState("claude"), [command, setCommand] = useState(""), [model, setModel] = useState(""), [environment, setEnvironment] = useState("{}"), [description, setDescription] = useState(""), [instructions, setInstructions] = useState(""), [draft, setDraft] = useState(true), [busy, setBusy] = useState(false), [status, setStatus] = useState("");
  function apply(d: ReturnType<typeof draftFromProfile>) {
    setSelected(d.id); setName(d.name); setAgent(d.agent); setCommand(d.command); setModel(d.model); setEnvironment(d.env_json); setDescription(d.description); setInstructions(d.instructions);
  }
  function choose(p?: LaunchProfile) {
    const next = draftFromProfile(p, agents[0]?.name || "claude");
    apply(next); setDraft(!p); setStatus("");
  }
  function useStarter(preset: StarterPreset) {
    apply(draftFromPreset(preset, agents[0]?.name || "claude")); setDraft(true);
    setStatus(t("agentSettings.profiles.starterLoaded", { name: preset.name }));
    requestAnimationFrame(() => formRef.current?.scrollIntoView({ block: "start", behavior: "smooth" }));
  }
  async function load(preferred = selected) {
    const [p, a, s] = await Promise.all([api.request<LaunchProfile[]>("/launch-profiles"), api.request<Agent[]>("/agents"), api.request<StarterPreset[]>("/launch-profile-presets").catch(() => [] as StarterPreset[])]);
    setProfiles(p); setAgents(a); setPresets(Array.isArray(s) ? s : []);
    const found = p.find((row) => row.id === preferred);
    if (found) choose(found); else if (!preferred) choose(undefined);
    return p;
  }
  useEffect(() => { void load().catch((e) => setStatus(String(e))); }, []);
  async function save() {
    if (busy) return;
    const parsed = parseEnvironment(environment);
    if (parsed.error) return setStatus(parsed.error);
    if (!name.trim()) return setStatus(t("agentSettings.profiles.nameRequired"));
    const body = profileRequestBody({ id: selected, name, agent, command, model, env_json: environment, description, instructions }, parsed.env!);
    setBusy(true); setStatus(t("agentSettings.profiles.saving"));
    try {
      const saved = await api.request<LaunchProfile>(selected ? `/launch-profiles/${selected}` : "/launch-profiles", { method: selected ? "PUT" : "POST", body });
      await load(saved.id); setSelected(saved.id); setDraft(false); setStatus(t("agentSettings.profiles.saved")); onChange(saved);
    } catch (e) { setStatus(String(e)); } finally { setBusy(false); }
  }
  async function remove() {
    if (!selected || !confirm(t("agentSettings.profiles.confirmDelete", { name }))) return;
    setBusy(true);
    try { await api.request(`/launch-profiles/${selected}`, { method: "DELETE" }); await load(0); setStatus(t("agentSettings.profiles.deleted")); onChange(undefined); } catch (e) { setStatus(String(e)); } finally { setBusy(false); }
  }
  return <Modal open className="launch-profiles" aria-label={t("agentSettings.profiles.title")} onCancel={(e) => { if (busy) e.preventDefault(); else onClose(); }}>
    <header><h2>{t("agentSettings.profiles.title")}</h2><button disabled={busy} onClick={onClose}>{t("agentSettings.editor.close")}</button></header>
    {presets.length > 0 && <section className="lp-starters" data-setting="agents.starters" aria-label={t("agentSettings.profiles.starters")}>
      <h3>{t("agentSettings.profiles.starters")}</h3>
      <p className="lp-hint">{t("agentSettings.profiles.startersHint")}</p>
      <div className="lp-starter-grid">
        {presets.map((preset) => <article className="lp-starter" data-preset={preset.key} key={preset.key}>
          <div className="lp-starter-head"><b className="lp-starter-name">{preset.name}</b><span className="lp-starter-agent">{preset.agent}</span></div>
          <p className="lp-starter-purpose">{preset.description}</p>
          <button type="button" className="lp-starter-use" disabled={busy} onClick={() => useStarter(preset)}>{t("agentSettings.profiles.useStarter")}</button>
        </article>)}
      </div>
    </section>}
    <div className="lp-picker"><label>{t("agentSettings.profiles.savedProfile")}<select aria-label={t("agentSettings.profiles.savedProfile")} className="lp-select" disabled={busy} value={selected} onChange={(e) => choose(profiles.find((p) => p.id === Number(e.target.value)))}><option value={0}>{t("agentSettings.profiles.newProfile")}</option>{profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label><button type="button" disabled={busy} onClick={() => choose(undefined)}>{t("agentSettings.profiles.new")}</button></div>
    {draft && <p className="lp-draft-note">{t("agentSettings.profiles.draftNote")}</p>}
    <form ref={formRef} onSubmit={(e) => { e.preventDefault(); void save(); }}>
      <label>{t("agentSettings.editor.name")}<input value={name} onChange={(e) => setName(e.target.value)} /></label>
      <label>{t("agentSettings.profiles.description")}<textarea aria-label={t("agentSettings.profiles.description")} className="lp-description" value={description} onChange={(e) => setDescription(e.target.value)} /></label>
      <label>{t("agentSettings.profiles.instructions")}<textarea aria-label={t("agentSettings.profiles.instructions")} className="lp-instructions" value={instructions} onChange={(e) => setInstructions(e.target.value)} /></label>
      <p className="lp-hint lp-instructions-help">{instructionsHelp()}</p>
      <label>{t("agentSettings.profiles.agent")}<select aria-label={t("agentSettings.profiles.agent")} className="lp-agent" value={agent} onChange={(e) => setAgent(e.target.value)}>{agents.map((a) => <option key={a.name} value={a.name}>{a.name}</option>)}</select></label>
      <h3 className="lp-advanced">{t("agentSettings.profiles.advanced")}</h3>
      <label>{t("agentSettings.profiles.commandOverride")}<input value={command} onChange={(e) => setCommand(e.target.value)} /></label>
      <label>{t("agentSettings.profiles.defaultModel")}<input value={model} onChange={(e) => setModel(e.target.value)} /></label>
      <label>{t("agentSettings.profiles.environment")}<textarea value={environment} onChange={(e) => setEnvironment(e.target.value)} /></label>
      <div className="lp-buttons"><button className="lp-save" disabled={busy}>{t("agentSettings.profiles.save")}</button><button className="lp-delete" type="button" disabled={busy || !selected} onClick={() => void remove()}>{t("agentSettings.profiles.delete")}</button></div>
      <p className="lp-status" role="status">{status}</p>
    </form>
  </Modal>;
}
