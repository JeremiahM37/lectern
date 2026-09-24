import { useEffect, useRef, useState } from "react";
import { Modal } from "../sessions/Modal";
import type { JsonValue } from "../api";
import {
  INSTRUCTIONS_HELP,
  draftFromPreset,
  draftFromProfile,
  parseEnvironment,
  profileRequestBody,
  type LaunchProfileRecord,
  type StarterPreset,
} from "./launchProfileForm";

export type LaunchProfile = LaunchProfileRecord;
type Agent = { name: string };
type Api = { request<T>(path: string, options?: { method?: string; body?: JsonValue }): Promise<T> };

export function LaunchProfiles({ api, onClose, onChange = () => {} }: { api: Api; onClose(): void; onChange?(profile?: LaunchProfile): void }) {
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
    setStatus(`“${preset.name}” starter loaded as an unsaved draft.`);
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
    if (!name.trim()) return setStatus("Name is required.");
    const body = profileRequestBody({ id: selected, name, agent, command, model, env_json: environment, description, instructions }, parsed.env!);
    setBusy(true); setStatus("Saving profile…");
    try {
      const saved = await api.request<LaunchProfile>(selected ? `/launch-profiles/${selected}` : "/launch-profiles", { method: selected ? "PUT" : "POST", body });
      await load(saved.id); setSelected(saved.id); setDraft(false); setStatus("Profile saved"); onChange(saved);
    } catch (e) { setStatus(String(e)); } finally { setBusy(false); }
  }
  async function remove() {
    if (!selected || !confirm(`Delete ${name}?`)) return;
    setBusy(true);
    try { await api.request(`/launch-profiles/${selected}`, { method: "DELETE" }); await load(0); setStatus("Profile deleted"); onChange(undefined); } catch (e) { setStatus(String(e)); } finally { setBusy(false); }
  }
  return <Modal open className="launch-profiles" aria-label="Launch profiles" onCancel={(e) => { if (busy) e.preventDefault(); else onClose(); }}>
    <header><h2>Launch profiles</h2><button disabled={busy} onClick={onClose}>Close</button></header>
    {presets.length > 0 && <section className="lp-starters" aria-label="Starter profiles">
      <h3>Starter profiles</h3>
      <p className="lp-hint">Pick a starter to open an editable draft with a ready purpose and briefing. Nothing is saved until you choose Save profile.</p>
      <div className="lp-starter-grid">
        {presets.map((preset) => <article className="lp-starter" data-preset={preset.key} key={preset.key}>
          <div className="lp-starter-head"><b className="lp-starter-name">{preset.name}</b><span className="lp-starter-agent">{preset.agent}</span></div>
          <p className="lp-starter-purpose">{preset.description}</p>
          <button type="button" className="lp-starter-use" disabled={busy} onClick={() => useStarter(preset)}>Use starter</button>
        </article>)}
      </div>
    </section>}
    <div className="lp-picker"><label>Saved profile<select aria-label="Saved profile" className="lp-select" disabled={busy} value={selected} onChange={(e) => choose(profiles.find((p) => p.id === Number(e.target.value)))}><option value={0}>New profile</option>{profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label><button type="button" disabled={busy} onClick={() => choose(undefined)}>New</button></div>
    {draft && <p className="lp-draft-note">Editing an unsaved draft — nothing is saved until you choose Save profile.</p>}
    <form ref={formRef} onSubmit={(e) => { e.preventDefault(); void save(); }}>
      <label>Name<input value={name} onChange={(e) => setName(e.target.value)} /></label>
      <label>Description<textarea aria-label="Description" className="lp-description" value={description} onChange={(e) => setDescription(e.target.value)} /></label>
      <label>Workflow instructions<textarea aria-label="Workflow instructions" className="lp-instructions" value={instructions} onChange={(e) => setInstructions(e.target.value)} /></label>
      <p className="lp-hint lp-instructions-help">{INSTRUCTIONS_HELP}</p>
      <label>Agent<select aria-label="Agent" className="lp-agent" value={agent} onChange={(e) => setAgent(e.target.value)}>{agents.map((a) => <option key={a.name} value={a.name}>{a.name}</option>)}</select></label>
      <h3 className="lp-advanced">Advanced · command, model and environment</h3>
      <label>Command override<input value={command} onChange={(e) => setCommand(e.target.value)} /></label>
      <label>Default model<input value={model} onChange={(e) => setModel(e.target.value)} /></label>
      <label>Environment (JSON)<textarea value={environment} onChange={(e) => setEnvironment(e.target.value)} /></label>
      <div className="lp-buttons"><button className="lp-save" disabled={busy}>Save profile</button><button className="lp-delete" type="button" disabled={busy || !selected} onClick={() => void remove()}>Delete profile</button></div>
      <p className="lp-status" role="status">{status}</p>
    </form>
  </Modal>;
}
