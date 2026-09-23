import { useEffect, useState } from 'react';
import type { SessionView } from '../types';
import type { SessionsApi } from './Sessions';
import { Modal } from './Modal';
import {
  agentLabel,
  describeSwitch,
  requestSwitch,
  type SwitchRequest,
} from '../continuity/handoff';
import {
  loadFavorites,
  saveFavorites,
  toggleFavorite,
  type Favorite,
} from '../continuity/favorites';
import './quick-switch.css';

export { agentLabel };

type Agent = { name: string; model_flag?: string };
type Profile = { id: number; name: string; agent: string; model: string };
export const sessionModelLabel = (s: SessionView) => s.launch_profile || s.model || agentLabel(s.agent);

export function QuickSwitch({api, session, onClose, onStarted, onProfiles}: {
  api: SessionsApi; session: SessionView; onClose(): void;
  onStarted(session: SessionView, afterWrap: number, request: SwitchRequest): void; onProfiles(): void;
}) {
  const [agents, setAgents] = useState<Agent[]>([]), [models, setModels] = useState<Record<string,string[]>>({});
  const [profiles, setProfiles] = useState<Profile[]>([]), [error, setError] = useState(''), [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false), [customAgent, setCustomAgent] = useState(session.agent), [customModel, setCustomModel] = useState('');
  const [favorites, setFavorites] = useState<Favorite[]>(loadFavorites);
  async function load() {
    setLoading(true); setError('');
    try {
      const [a,m,p] = await Promise.all([api.request<Agent[]>('/agents'),api.request<Record<string,string[]>>('/models'),api.request<Profile[]>('/launch-profiles')]);
      setAgents(a); setModels(m); setProfiles(p.map(({id,name,agent,model})=>({id,name,agent,model})));
    } catch(e) { setError(String(e)); } finally { setLoading(false); }
  }
  useEffect(()=>{void load();},[]);
  function updateFavorites(next: Favorite[]) { setFavorites(next); saveFavorites(next); }
  const favored = (agent: string, model: string, profile: number) =>
    favorites.find(f=>f.profile===profile&&(profile!==0||(f.agent===agent&&f.model===model)));
  const favoriteReady = (f: Favorite) => f.profile
    ? profiles.some(p=>p.id===f.profile)
    : agents.some(a=>a.name===f.agent);
  const favoriteName = (f: Favorite) => {
    const profile = f.profile ? profiles.find(p=>p.id===f.profile) : undefined;
    return profile ? profile.name : (f.label || describeSwitch(f.agent, f.model));
  };
  const favoriteDetail = (f: Favorite) => {
    const profile = f.profile ? profiles.find(p=>p.id===f.profile) : undefined;
    if (f.profile) return profile ? `${agentLabel(profile.agent)}${profile.model ? ' · '+profile.model : ''}` : 'Provider unavailable';
    return f.model || 'Default model';
  };
  async function choose(request: SwitchRequest) {
    if(busy) return;
    setBusy(true); setError('');
    try {
      const result = await requestSwitch(api, session.id, request);
      onStarted(session,result.after_wrap_id,request); onClose();
    } catch(e) {setError(String(e)); setBusy(false);}
  }
  // A favorite stores an identity, not a frozen launch: if the provider behind
  // it was edited (or its agent changed), resolve the current metadata instead
  // of switching to the stale model the favorite was saved with.
  async function chooseFavorite(f: Favorite) {
    if(busy) return;
    if(f.profile){
      const profile = profiles.find(p=>p.id===f.profile);
      if(!profile){ setError('That provider is no longer available. Your original session is unchanged.'); return; }
      await choose({agent:profile.agent,model:profile.model,profile:profile.id,destination:profile.name});
      return;
    }
    await choose({agent:f.agent,model:f.model,profile:0,destination:favoriteName(f)});
  }
  const star = (entry: Favorite, label: string) => {
    const on = !!favored(entry.agent, entry.model, entry.profile);
    return <button type="button" className="switch-fav" aria-pressed={on} aria-label={on?`Remove ${label} from favorites`:`Add ${label} to favorites`} onClick={()=>updateFavorites(toggleFavorite(favorites, entry))}>{on?'★':'☆'}</button>;
  };
  return <Modal id="sheet" className="sheet quick-switch" aria-label="Switch agent" onCancel={()=>{if(!busy)onClose();}}>
    <div className="sheet-head"><h2>Switch agent</h2><button className="x" aria-label="Close switcher" disabled={busy} onClick={onClose}>✕</button></div>
    <p className="sub">Current: <b>{sessionModelLabel(session)}</b>. Pick the next agent or model.</p>
    <p className="sub">It gets a handoff in the same workspace and opens here when ready. Your original session stays available in Sessions.</p>
    {error && <p role="alert">{error} {!agents.length&&<button className="b" onClick={()=>void load()}>Retry</button>}</p>}
    {loading && <p role="status">Loading agents…</p>}
    {!!favorites.length && <section aria-label="Favorites"><h3>Favorites</h3><div className="switch-favorites">{favorites.map(f=>{
      const ready=favoriteReady(f);
      return <div className="switch-choice-wrap" key={`${f.profile}:${f.agent}:${f.model}`}>
        <button className="b switch-choice" disabled={busy||!ready} onClick={()=>void chooseFavorite(f)}><strong>{favoriteName(f)}</strong><small>{ready?favoriteDetail(f):'Unavailable on this device'}</small></button>
        {star(f, favoriteName(f))}
      </div>;
    })}</div></section>}
    {!!profiles.length && <section aria-label="Saved providers"><h3>Saved providers</h3><div className="switch-options">{profiles.map(p=>{
      const entry: Favorite = {agent:p.agent,model:p.model,profile:p.id,label:p.name};
      return <div className="switch-choice-wrap" key={p.id}><button className="b switch-choice" disabled={busy} onClick={()=>void choose({agent:p.agent,model:p.model,profile:p.id,destination:p.name})}><strong>{p.name}</strong><small>{agentLabel(p.agent)}{p.model?' · '+p.model:''}</small></button>{star(entry,p.name)}</div>;
    })}</div></section>}
    {agents.map(a=><section key={a.name} aria-label={agentLabel(a.name)}><h3>{agentLabel(a.name)}</h3><div className="switch-options">{['',...(a.model_flag?models[a.name]||[]:[])].map(model=>{
      const current=!session.launch_profile&&a.name===session.agent&&model===session.model;
      const entry: Favorite = {agent:a.name,model,profile:0,label:describeSwitch(a.name,model)};
      return <div className="switch-choice-wrap" key={model||'default'}><button className="b switch-choice" disabled={busy||current} aria-current={current?'true':undefined} onClick={()=>void choose({agent:a.name,model,profile:0,destination:entry.label})}><strong>{model||'Default model'}</strong>{current&&<small>Current</small>}</button>{star(entry,entry.label)}</div>;
    })}</div></section>)}
    {!!agents.length&&<details className="switch-custom"><summary>Another model…</summary><label className="f" htmlFor="switch-agent">Agent</label><select className="f" id="switch-agent" value={customAgent} onChange={e=>setCustomAgent(e.target.value)}>{agents.filter(a=>a.model_flag).map(a=><option key={a.name} value={a.name}>{agentLabel(a.name)}</option>)}</select><label className="f" htmlFor="switch-model">Model ID</label><input className="f" id="switch-model" value={customModel} onChange={e=>setCustomModel(e.target.value)} placeholder="Exact model ID supported by this agent"/><button className="b" disabled={busy||!customModel.trim()||!agents.find(a=>a.name===customAgent)?.model_flag} onClick={()=>void choose({agent:customAgent,model:customModel.trim(),profile:0,destination:describeSwitch(customAgent,customModel.trim())})}>Switch model</button></details>}
    <button className="b switch-providers" disabled={busy} onClick={onProfiles}>Add or manage a provider…</button>
  </Modal>;
}
