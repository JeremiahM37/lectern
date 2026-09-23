import { useEffect, useState } from 'react';
import type { SessionView } from '../types';
import type { SessionsApi } from './Sessions';
import { Modal } from './Modal';
import './quick-switch.css';

type Agent = { name: string; model_flag?: string };
type Profile = { id: number; name: string; agent: string; model: string };
export const agentLabel = (name: string) => ({claude:'Claude', codex:'Codex', gemini:'Gemini'}[name] || name);
export const sessionModelLabel = (s: SessionView) => s.launch_profile || s.model || agentLabel(s.agent);
export function QuickSwitch({api, session, onClose, onStarted, onProfiles}: {
  api: SessionsApi; session: SessionView; onClose(): void;
  onStarted(session: SessionView, afterWrap: number): void; onProfiles(): void;
}) {
  const [agents, setAgents] = useState<Agent[]>([]), [models, setModels] = useState<Record<string,string[]>>({});
  const [profiles, setProfiles] = useState<Profile[]>([]), [error, setError] = useState(''), [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false), [customAgent, setCustomAgent] = useState(session.agent), [customModel, setCustomModel] = useState('');
  async function load() {
    setLoading(true); setError('');
    try {
      const [a,m,p] = await Promise.all([api.request<Agent[]>('/agents'),api.request<Record<string,string[]>>('/models'),api.request<Profile[]>('/launch-profiles')]);
      setAgents(a); setModels(m); setProfiles(p.map(({id,name,agent,model})=>({id,name,agent,model})));
    } catch(e) { setError(String(e)); } finally { setLoading(false); }
  }
  useEffect(()=>{void load();},[]);
  async function choose(agent: string, model = '', profile = 0) {
    if(busy) return;
    setBusy(true); setError('');
    try {
      const result = await api.request<{after_wrap_id:number}>(`/sessions/${session.id}/handoff`, {method:'POST',body:{successor:true,kill_old:false,agent,model,profile_id:profile,quick_switch:true}});
      onStarted(session,result.after_wrap_id); onClose();
    } catch(e) {setError(String(e)); setBusy(false);}
  }
  return <Modal id="sheet" className="sheet quick-switch" aria-label="Switch agent" onCancel={()=>{if(!busy)onClose();}}>
    <div className="sheet-head"><h2>Switch agent</h2><button className="x" aria-label="Close switcher" disabled={busy} onClick={onClose}>✕</button></div>
    <p className="sub">Current: <b>{sessionModelLabel(session)}</b>. Pick the next agent or model.</p>
    <p className="sub">It gets a handoff in the same workspace and opens here when ready. Your original session stays available in Sessions.</p>
    {error && <p role="alert">{error} {!agents.length&&<button className="b" onClick={()=>void load()}>Retry</button>}</p>}
    {loading && <p role="status">Loading agents…</p>}
    {busy && <p role="status">Requesting switch…</p>}
    {!!profiles.length && <section aria-label="Saved providers"><h3>Saved providers</h3><div className="switch-options">{profiles.map(p=><button key={p.id} className="b switch-choice" disabled={busy} onClick={()=>void choose(p.agent,p.model,p.id)}><strong>{p.name}</strong><small>{agentLabel(p.agent)}{p.model?' · '+p.model:''}</small></button>)}</div></section>}
    {agents.map(a=><section key={a.name} aria-label={agentLabel(a.name)}><h3>{agentLabel(a.name)}</h3><div className="switch-options">{['',...(a.model_flag?models[a.name]||[]:[])].map(model=>{
      const current=!session.launch_profile&&a.name===session.agent&&model===session.model;
      return <button key={model} className="b switch-choice" disabled={busy||current} aria-current={current?'true':undefined} onClick={()=>void choose(a.name,model)}><strong>{model||'Default model'}</strong>{current&&<small>Current</small>}</button>;
    })}</div></section>)}
    {!!agents.length&&<details className="switch-custom"><summary>Another model…</summary><label className="f" htmlFor="switch-agent">Agent</label><select className="f" id="switch-agent" value={customAgent} onChange={e=>setCustomAgent(e.target.value)}>{agents.filter(a=>a.model_flag).map(a=><option key={a.name} value={a.name}>{agentLabel(a.name)}</option>)}</select><label className="f" htmlFor="switch-model">Model ID</label><input className="f" id="switch-model" value={customModel} onChange={e=>setCustomModel(e.target.value)} placeholder="Exact model ID supported by this agent"/><button className="b" disabled={busy||!customModel.trim()||!agents.find(a=>a.name===customAgent)?.model_flag} onClick={()=>void choose(customAgent,customModel.trim())}>Switch model</button></details>}
    <button className="b switch-providers" disabled={busy} onClick={onProfiles}>Add or manage a provider…</button>
  </Modal>;
}
