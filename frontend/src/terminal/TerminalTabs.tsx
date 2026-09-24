import type React from 'react';
import {useCallback,useEffect,useRef,useState} from 'react';
import {viewportSliceFor, visibleViewport, virtualKeyboard} from './viewport';
import './terminal-tabs.css';
const STORAGE='lec-terminal-tabs-v1';
const DURABLE_STORAGE='lec-terminal-tabs-durable-v1';
const SPLIT_STORAGE='lec-terminal-split-ratio';
export type PaneSlot='primary'|'secondary';
export interface TerminalTab {path:string;label:string}
interface TabsState {tabs:TerminalTab[];active:string|null;secondary:string|null}
export function terminalPath(url:string){const parsed=new URL(url,location.origin),path=parsed.pathname.replace(/^\/term\//,'/terminal/').replace(/\/$/,'');if(parsed.origin!==location.origin||!/^\/terminal\/(session|attempt|project)\/[1-9]\d*$/.test(path))throw new Error('This terminal does not have a valid Lectern address.');return path;}
const hashFor=(path:string|null)=>path?'#terminals/'+path.split('/').slice(-2).join('/'):'#terminals';
const buttonID=(path:string)=>'terminal-tab-'+path.split('/').slice(-2).join('-');
function restore():TabsState{const tabs:TerminalTab[]=[];try{const saved:unknown=JSON.parse(sessionStorage.getItem(STORAGE)||localStorage.getItem(DURABLE_STORAGE)||'{}');if(saved&&typeof saved==='object'&&'tabs'in saved&&Array.isArray(saved.tabs)){for(const row of saved.tabs.slice(0,30)){try{if(!row||typeof row!=='object'||typeof row.path!=='string')continue;const path=terminalPath(row.path);if(!tabs.some(tab=>tab.path===path))tabs.push({path,label:String(row.label||'Terminal').slice(0,160)});}catch{}}const active='active'in saved&&typeof saved.active==='string'&&tabs.some(tab=>tab.path===saved.active)?saved.active:tabs[0]?.path||null;const secondary='secondary'in saved&&typeof saved.secondary==='string'&&saved.secondary!==active&&tabs.some(tab=>tab.path===saved.secondary)?saved.secondary:null;return {tabs,active,secondary};}}catch{}return {tabs,active:tabs[0]?.path||null,secondary:null};}
export interface TerminalTabsController extends TabsState {open:(url:string,label?:string)=>void;select:(path:string)=>void;assign:(slot:PaneSlot,path:string)=>void;setSplit:(on:boolean)=>void;close:(path:string)=>void;hash:string}
export function useTerminalTabs(onActivate:(hash:string)=>void):TerminalTabsController{
 const [state,setState]=useState(restore),latest=useRef(state),activate=useRef(onActivate);latest.current=state;activate.current=onActivate;
 const commit=useCallback((next:TabsState)=>{if(next.secondary===next.active)next={...next,secondary:null};latest.current=next;setState(next);try{sessionStorage.setItem(STORAGE,JSON.stringify(next));}catch{}try{localStorage.setItem(DURABLE_STORAGE,JSON.stringify(next));}catch{}activate.current(hashFor(next.active));},[]);
 const open=useCallback((url:string,label?:string)=>{const path=terminalPath(url),old=latest.current,existing=old.tabs.find(tab=>tab.path===path),title=String(label||existing?.label||'Terminal').slice(0,160);commit({...old,active:path,secondary:old.secondary===path?old.active:old.secondary,tabs:existing?old.tabs.map(tab=>tab.path===path?{path,label:title}:tab):[...old.tabs,{path,label:title}]});},[commit]);
 // Sending a tab to the pane that already holds the other one swaps the two,
 // so a tab is never asked to render in both panes at once.
 const assign=useCallback((slot:PaneSlot,path:string)=>{const old=latest.current;if(!old.tabs.some(tab=>tab.path===path))return;if(slot==='primary'){commit({...old,active:path,secondary:old.secondary===path?old.active:old.secondary});return;}if(path===old.active){if(old.secondary)commit({...old,active:old.secondary,secondary:path});return;}commit({...old,secondary:path});},[commit]);
 const select=useCallback((path:string)=>assign('primary',path),[assign]);
 const setSplit=useCallback((on:boolean)=>{const old=latest.current;if(!on){commit({...old,secondary:null});return;}const companion=old.tabs.find(tab=>tab.path!==old.active);if(companion)commit({...old,secondary:companion.path});},[commit]);
 const close=useCallback((path:string)=>{const old=latest.current,index=old.tabs.findIndex(tab=>tab.path===path);if(index<0)return;commit({tabs:old.tabs.filter(tab=>tab.path!==path),secondary:old.secondary===path?null:old.secondary,active:old.active===path?old.tabs[index+1]?.path||old.tabs[index-1]?.path||null:old.active});requestAnimationFrame(()=>{const active=latest.current.active;if(active)document.getElementById(buttonID(active))?.focus({preventScroll:true});});},[commit]);
 return {...state,open,select,assign,setSplit,close,hash:hashFor(state.active)};
}
interface SwipeOptions {enabled:()=>boolean;relative:(delta:number)=>boolean;width:()=>number;selected:()=>boolean;blocked:(target:EventTarget|null)=>boolean}
// Keep a deliberate swipe reachable in a narrow tab strip. The terminal body
// retains its 72px minimum; compact controls can leave the strip less room.
function bindSwipe(target:Window|HTMLElement,options:SwipeOptions){const abort=new AbortController();let start:{x:number;y:number;at:number;maxVertical:number}|undefined;const on=(type:string,listener:(event:TouchEvent)=>void,passive=true)=>target.addEventListener(type,listener as EventListener,{signal:abort.signal,passive});
 on('touchstart',event=>{const touch=event.touches[0];if(!options.enabled()||event.touches.length!==1||!touch||options.blocked(event.target)){start=undefined;return;}start={x:touch.clientX,y:touch.clientY,at:performance.now(),maxVertical:0};});
 on('touchmove',event=>{const touch=event.touches[0];if(!start||event.touches.length!==1||!touch){start=undefined;return;}start.maxVertical=Math.max(start.maxVertical,Math.abs(touch.clientY-start.y));});on('touchcancel',()=>{start=undefined;});
 on('touchend',event=>{const saved=start;start=undefined;const touch=event.changedTouches[0];if(!saved||!options.enabled()||event.changedTouches.length!==1||!touch)return;const dx=touch.clientX-saved.x,dy=touch.clientY-saved.y,elapsed=performance.now()-saved.at,width=options.width(),threshold=Math.max(Math.min(72,width*.45),Math.min(140,width*.22));const flick=elapsed<=550&&saved.maxVertical<Math.max(48,threshold*.55)&&Math.abs(dx)>=threshold&&Math.abs(dx)>=Math.abs(dy)*1.6;if(!flick||options.selected())return;if(options.relative(dx<0?1:-1))event.preventDefault();},false);return()=>abort.abort();
}
interface FrameProps {tab:TerminalTab;active:boolean;visible:boolean;mobile:boolean;compact:boolean;relative:(delta:number)=>boolean;slot?:PaneSlot;focused?:boolean;onFocusPane?:()=>void;onClosePane?:()=>void}
function Frame(props:FrameProps){const frame=useRef<HTMLIFrameElement>(null),latest=useRef(props);latest.current=props;
 const notify=()=>{const value=latest.current;if(!value.active||!value.visible)return;frame.current?.contentWindow?.postMessage({type:'lec-terminal-visible',mobile:value.mobile,compact:value.mobile&&value.compact},location.origin);};
 // The phone keyboard overlays the frame instead of resizing it, so the visible
 // slice of the frame is measured here and told to the terminal page. The frame
 // asks for one when it mounts: a message posted before it loads is lost.
 const measure=()=>{const value=latest.current,node=frame.current;if(!value.active||!value.visible||!node)return;const win=node.contentWindow;if(!win)return;const slice=viewportSliceFor(node);win.postMessage({type:'lec-terminal-viewport',top:slice.top,height:slice.height},location.origin);};
 useEffect(()=>{notify();measure();},[props.active,props.visible,props.mobile,props.compact,props.slot]);
 useEffect(()=>{const node=frame.current;if(!node)return;const abort=new AbortController(),signal=abort.signal,view=window.visualViewport;let settle:number|undefined;const run=()=>{measure();clearTimeout(settle);settle=window.setTimeout(measure,150);};window.addEventListener('resize',run,{signal});window.addEventListener('orientationchange',run,{signal});view?.addEventListener('resize',run,{signal});view?.addEventListener('scroll',run,{signal});virtualKeyboard()?.addEventListener('geometrychange',run,{signal});const observer=new ResizeObserver(run);observer.observe(node);return()=>{clearTimeout(settle);observer.disconnect();abort.abort();};},[]);
 // A deliberate horizontal flick on the terminal body is recognised inside the
 // frame, where scrolling, long-press selection and pinch already own the
 // gesture, and relayed here; negotiated mouse reporting no longer blocks it.
 useEffect(()=>{const onMessage=(event:MessageEvent)=>{const win=frame.current?.contentWindow;if(!win||event.source!==win||event.origin!==location.origin)return;const data:unknown=event.data;if(!data||typeof data!=='object'||!('type' in data))return;if(data.type==='lec-terminal-need-viewport'){measure();return;}if(data.type!=='lec-terminal-swipe')return;const value=latest.current;if(!value.mobile||!value.visible||!value.active||value.slot)return;value.relative('direction' in data&&data.direction===-1?-1:1);};window.addEventListener('message',onMessage);return()=>window.removeEventListener('message',onMessage);},[]);
 return <div className="terminal-tabpanel" data-slot={props.slot} data-focused={props.slot?props.focused===true:undefined} id={buttonID(props.tab.path)+'-panel'} role="tabpanel" aria-labelledby={buttonID(props.tab.path)} hidden={!props.active} inert={!props.active||!props.visible} onPointerDownCapture={props.slot?props.onFocusPane:undefined}>{props.slot&&<div className="terminal-pane-label"><span className="terminal-pane-name" title={props.tab.label}>{props.tab.label}</span><button className="terminal-pane-close" aria-label={'Remove '+props.tab.label+' from the split view'} title="Remove this pane from the split view" onClick={props.onClosePane}>×</button></div>}<iframe ref={frame} title={props.tab.label+' terminal'} src={props.tab.path+'?embed=1'} allow="clipboard-read; clipboard-write" onLoad={notify}/></div>;
}
export interface TerminalMachine {id:number;name:string}
// A project only needs what the picker shows: a name and where it lives.
export interface TerminalProject {id:number;name:string;repo_path:string;target_name?:string}
// What the caret can launch: a scratch room on a machine, or a tracked shell
// that opens in a project's own repository.
export type NewTerminalChoice={machineID?:number;projectID?:number};
// A blank shell is the quickest way into a machine: no project, agent or
// worktree to choose. The main button still opens a scratch shell on the
// server's default machine; the caret is where a specific project or another
// machine is chosen. There is no launch form: picking a row launches and
// attaches immediately.
function NewTerminal({machines,projects,onNew}:{machines:TerminalMachine[];projects:TerminalProject[];onNew:(choice?:NewTerminalChoice)=>Promise<void>}){
 const [busy,setBusy]=useState(false),[query,setQuery]=useState(""),menu=useRef<HTMLDetailsElement>(null),search=useRef<HTMLInputElement>(null);
 const place=useCallback(()=>{
  const details=menu.current,panel=details?.querySelector<HTMLElement>('.terminal-new-picker'),summary=details?.querySelector('summary');
  if(!details?.open||!panel||!summary)return;
  const view=visibleViewport(),left=visualViewport?.offsetLeft??0,width=visualViewport?.width??innerWidth,edge=8;
  panel.style.width=Math.min(340,Math.max(1,width-2*edge))+'px';
  panel.style.maxHeight=Math.min(440,Math.max(1,view.height-2*edge))+'px';
  const anchor=summary.getBoundingClientRect(),box=panel.getBoundingClientRect();
  panel.style.left=Math.max(left+edge,Math.min(anchor.right-box.width,left+width-edge-box.width))+'px';
  const below=anchor.bottom+6,above=anchor.top-6-box.height;
  panel.style.top=Math.max(view.top+edge,Math.min(below+box.height<=view.top+view.height-edge?below:above,view.top+view.height-edge-box.height))+'px';
 },[]);
 useEffect(()=>{
  const abort=new AbortController(),signal=abort.signal;
  addEventListener('resize',place,{signal});addEventListener('scroll',place,{signal,capture:true});
  visualViewport?.addEventListener('resize',place,{signal});visualViewport?.addEventListener('scroll',place,{signal});virtualKeyboard()?.addEventListener('geometrychange',place,{signal});
  document.addEventListener('keydown',event=>{if(event.key==='Escape'&&menu.current?.open){event.preventDefault();menu.current.open=false;menu.current.querySelector('summary')?.focus();}},{signal});
  return()=>abort.abort();
 },[place]);
 useEffect(()=>{place();},[query,projects,machines,place]);
 const start=(choice?:NewTerminalChoice)=>{if(menu.current)menu.current.open=false;setQuery("");setBusy(true);void onNew(choice).finally(()=>setBusy(false));};
 useEffect(()=>{const close=(event:PointerEvent)=>{if(menu.current?.open&&event.target instanceof Node&&!menu.current.contains(event.target))menu.current.open=false;};document.addEventListener('pointerdown',close);return()=>document.removeEventListener('pointerdown',close);},[]);
 const q=query.trim().toLowerCase(),matches=(...parts:string[])=>!q||parts.some(part=>part.toLowerCase().includes(q));
 const shownMachines=machines.filter(machine=>matches(machine.name));
 const shownProjects=projects.filter(project=>matches(project.name,project.repo_path,project.target_name||''));
 const caret=machines.length>1||projects.length>0;
 return <div className="terminal-new"><button className="terminal-new-main" disabled={busy} aria-label="New terminal" title="Open a blank shell on the default machine" onClick={()=>start()}>{busy?'Opening…':<>＋<span className="terminal-new-label"> New terminal</span></>}</button>{caret&&<details className="terminal-new-machines" ref={menu} onToggle={()=>{if(menu.current?.open){place();if(matchMedia("(pointer:fine)").matches)search.current?.focus({preventScroll:true});}else setQuery("");}}><summary aria-label="New terminal in a project or on another machine" title="New terminal in a project or on another machine">▾</summary><div className="terminal-actions-panel terminal-new-picker" role="menu"><input ref={search} className="terminal-new-search" type="search" value={query} aria-label="Search projects and machines" placeholder="Search projects and machines" onChange={event=>setQuery(event.target.value)}/>{shownMachines.length>0&&<div className="terminal-new-group" role="presentation">Machines</div>}{shownMachines.map(machine=><button key={'machine-'+machine.id} role="menuitem" disabled={busy} onClick={()=>start({machineID:machine.id})}>{machine.name}</button>)}{shownProjects.length>0&&<div className="terminal-new-group" role="presentation">Projects</div>}{shownProjects.map(project=><button key={'project-'+project.id} role="menuitem" className="terminal-new-project" disabled={busy} onClick={()=>start({projectID:project.id})}><span className="terminal-new-project-name">{project.name}</span><span className="terminal-new-project-path">{project.repo_path}{project.target_name?' · '+project.target_name:''}</span></button>)}{shownMachines.length===0&&shownProjects.length===0&&<div className="terminal-new-empty" role="presentation">No matching projects or machines</div>}</div></details>}</div>;
}
export function TerminalTabs({controller,visible,machines,projects,onNew,onBrowse,onSearch,switcher}:{controller:TerminalTabsController;visible:boolean;machines:TerminalMachine[];projects:TerminalProject[];onNew:(choice?:NewTerminalChoice)=>Promise<void>;onBrowse:()=>void;onSearch:()=>void;switcher?:React.ReactNode}){
 const [mobile,setMobile]=useState(()=>matchMedia('(max-width:1023px)').matches),[compact,setCompact]=useState(()=>{try{return localStorage.getItem('lec-terminal-compact')!=='0';}catch{return true;}}),[mounted,setMounted]=useState<string[]>([]);const list=useRef<HTMLDivElement>(null),actions=useRef<HTMLDetailsElement>(null),panels=useRef<HTMLDivElement>(null),latest=useRef({controller,visible,mobile});latest.current={controller,visible,mobile};
 // Place the overflow menu in the visible viewport, including keyboard pan.
 const placeActions=useCallback(()=>{
  const details=actions.current,panel=details?.querySelector<HTMLElement>('.terminal-actions-panel'),summary=details?.querySelector('summary');
  if(!details?.open||!panel||!summary)return;
  const view=window.visualViewport,usable=visibleViewport(),edge=8,left=view?.offsetLeft??0,top=usable.top,width=view?.width??innerWidth,height=usable.height;
  const availableWidth=Math.max(1,width-2*edge),availableHeight=Math.max(1,height-2*edge);
  panel.style.width=Math.min(240,availableWidth)+'px';panel.style.maxHeight=availableHeight+'px';
  const anchor=summary.getBoundingClientRect(),box=panel.getBoundingClientRect();
  panel.style.left=Math.max(left+edge,Math.min(anchor.right-box.width,left+width-edge-box.width))+'px';
  const below=anchor.bottom+6,above=anchor.top-6-box.height;
  panel.style.top=Math.max(top+edge,Math.min(below+box.height<=top+height-edge?below:above,top+height-edge-box.height))+'px';
 },[]);
 useEffect(()=>{
  const abort=new AbortController(),signal=abort.signal;
  addEventListener('resize',placeActions,{signal});addEventListener('scroll',placeActions,{signal,capture:true});
  visualViewport?.addEventListener('resize',placeActions,{signal});visualViewport?.addEventListener('scroll',placeActions,{signal});virtualKeyboard()?.addEventListener('geometrychange',placeActions,{signal});
  document.addEventListener('pointerdown',event=>{if(actions.current?.open&&event.target instanceof Node&&!actions.current.contains(event.target))actions.current.open=false;},{signal});
  document.addEventListener('keydown',event=>{if(event.key==='Escape'&&actions.current?.open){event.preventDefault();actions.current.open=false;actions.current.querySelector('summary')?.focus();}},{signal});
  return()=>abort.abort();
 },[placeActions]);
 useEffect(()=>{if(actions.current)actions.current.open=false;},[controller.active,visible]);
 const [focus,setFocus]=useState<PaneSlot>('primary');
 const [ratio,setRatio]=useState(()=>{try{const saved=Number(localStorage.getItem(SPLIT_STORAGE));return saved>=20&&saved<=80?saved:50;}catch{return 50;}});const ratioRef=useRef(ratio);ratioRef.current=ratio;
 const relative=useCallback((delta:number)=>{const value=latest.current.controller,index=value.tabs.findIndex(tab=>tab.path===value.active),next=value.tabs[index+delta];if(!next)return false;value.select(next.path);return true;},[]);
 useEffect(()=>{const query=matchMedia('(max-width:1023px)'),changed=()=>setMobile(query.matches);query.addEventListener('change',changed);return()=>query.removeEventListener('change',changed);},[]);
 const focused=mobile&&compact&&visible&&!!controller.active;
 // Two panes need room, so a narrow viewport keeps the single-pane layout and
 // remembers the split for when there is width for it again.
 const split=!mobile&&!!controller.secondary&&controller.tabs.some(tab=>tab.path===controller.secondary);
 useEffect(()=>{document.body.classList.toggle('terminal-compact',focused);return()=>document.body.classList.remove('terminal-compact');},[focused]);
 useEffect(()=>{if(!visible)return;const paths=[controller.active,split?controller.secondary:null].filter((path):path is string=>!!path);if(paths.length)setMounted(old=>paths.every(path=>old.includes(path))?old:[...old,...paths.filter(path=>!old.includes(path))]);if(controller.active)document.getElementById(buttonID(controller.active))?.scrollIntoView({block:'nearest',inline:'nearest'});},[visible,controller.active,controller.secondary,split]);
 useEffect(()=>{if(!split&&focus!=='primary')setFocus('primary');},[split,focus]);
 // Focus moving into a pane blurs this document, and the iframe it entered is
 // what the browser reports as active — that is how a pane claims the tab bar.
 useEffect(()=>{if(!split)return;const claim=()=>{const element=document.activeElement;if(!(element instanceof HTMLIFrameElement))return;const slot=element.closest('.terminal-tabpanel')?.getAttribute('data-slot');if(slot==='primary'||slot==='secondary')setFocus(slot);};addEventListener('blur',claim);return()=>removeEventListener('blur',claim);},[split]);
 useEffect(()=>{const strip=list.current;if(!strip)return;return bindSwipe(strip,{enabled:()=>latest.current.mobile&&latest.current.visible&&!!latest.current.controller.active,relative,width:()=>strip.clientWidth||innerWidth,selected:()=>false,blocked:target=>target instanceof Element&&!!target.closest('.terminal-tab-close,.terminal-actions,.terminal-search,.terminal-focus,.terminal-split,.terminal-new,a,input,select,textarea,[contenteditable="true"]')});},[relative]);
 const resize=useCallback((event:React.PointerEvent<HTMLElement>)=>{const host=panels.current;if(!host)return;event.preventDefault();const handle=event.currentTarget;handle.setPointerCapture(event.pointerId);host.classList.add('resizing');
  const move=(moved:PointerEvent)=>{const box=host.getBoundingClientRect();if(box.width)setRatio(Math.min(80,Math.max(20,((moved.clientX-box.left)/box.width)*100)));};
  const stop=()=>{removeEventListener('pointermove',move);removeEventListener('pointerup',stop);removeEventListener('pointercancel',stop);host.classList.remove('resizing');try{handle.releasePointerCapture(event.pointerId);}catch{}try{localStorage.setItem(SPLIT_STORAGE,String(Math.round(ratioRef.current)));}catch{}};
  addEventListener('pointermove',move);addEventListener('pointerup',stop);addEventListener('pointercancel',stop);},[]);
 const nudge=useCallback((delta:number)=>{setRatio(old=>{const next=Math.min(80,Math.max(20,old+delta));try{localStorage.setItem(SPLIT_STORAGE,String(Math.round(next)));}catch{}return next;});},[]);
 const active=controller.tabs.find(tab=>tab.path===controller.active);
 const slotFor=(path:string):PaneSlot|undefined=>!split?undefined:path===controller.active?'primary':path===controller.secondary?'secondary':undefined;
 return <section id="terminal-workspace" hidden={!visible} inert={!visible}><div className="terminal-tabbar"><div className="terminal-tablist" ref={list} role="tablist" aria-multiselectable={split||undefined} aria-label="Open terminals">{controller.tabs.map((tab,index)=>{const duplicate=controller.tabs.filter(other=>other.label===tab.label).length>1,label=tab.label+(duplicate?' #'+tab.path.split('/').at(-1):''),slot=slotFor(tab.path);return <div className="terminal-tab-item" role="presentation" key={tab.path}><button className="terminal-tab" title={split?label+(slot?' — showing in the '+(slot==='primary'?'left':'right')+' pane':'; opens in the '+(focus==='primary'?'left':'right')+' pane'):label} id={buttonID(tab.path)} role="tab" data-slot={slot} aria-selected={!!slot||tab.path===controller.active} aria-controls={buttonID(tab.path)+'-panel'} tabIndex={tab.path===controller.active?0:-1} onClick={()=>controller.assign(split?focus:'primary',tab.path)} onKeyDown={event=>{let next:TerminalTab|undefined;if(event.key==='ArrowRight')next=controller.tabs[(index+1)%controller.tabs.length];if(event.key==='ArrowLeft')next=controller.tabs[(index+controller.tabs.length-1)%controller.tabs.length];if(event.key==='Home')next=controller.tabs[0];if(event.key==='End')next=controller.tabs.at(-1);if(event.key==='Delete'){event.preventDefault();controller.close(tab.path);return;}if(next){event.preventDefault();controller.select(next.path);document.getElementById(buttonID(next.path))?.focus({preventScroll:true});}}}>{label}</button><button className="terminal-tab-close" aria-label={'Close terminal view: '+label} title="Close this view; the agent keeps running" onClick={()=>controller.close(tab.path)}>×</button></div>;})}</div>{switcher}<NewTerminal machines={machines} projects={projects} onNew={onNew}/><button className="b terminal-split" aria-pressed={split} hidden={!active||mobile} disabled={controller.tabs.length<2} title={controller.tabs.length<2?'Open a second terminal to show two side by side':split?'Show one terminal at a time':'Show two terminals side by side'} onClick={()=>controller.setSplit(!controller.secondary)}>{split?'◫ Unsplit':'◫ Split'}</button><a className="b terminal-popout" target="_blank" rel="noopener" title="Open this terminal in a separate browser tab" hidden={!active} href={active?.path}>Pop out ↗</a><button className="terminal-search" aria-label="Search sessions and actions" title="Search sessions and actions" onClick={onSearch}>⌕</button><details className="terminal-actions" ref={actions} hidden={!active} onToggle={placeActions}><summary aria-label="Terminal actions" title="Terminal actions" onClick={event=>{event.preventDefault();if(actions.current){actions.current.open=!actions.current.open;placeActions();}}}>⋯</summary><div className="terminal-actions-panel" role="menu"><a className="terminal-menu-popout" target="_blank" rel="noopener" role="menuitem" href={active?.path}>Open in new tab</a><button className="terminal-menu-close" role="menuitem" onClick={()=>{if(active)controller.close(active.path);if(actions.current)actions.current.open=false;}}>Close this view</button></div></details><button className="terminal-focus" aria-label={focused?'Show navigation':'Focus terminal'} title={focused?'Show navigation':'Focus terminal'} aria-pressed={focused} hidden={!active} onClick={()=>{setCompact(!compact);try{localStorage.setItem('lec-terminal-compact',compact?'0':'1');}catch{}}}>{focused?'☰':'⤢'}</button></div><div className={split?'terminal-panels split':'terminal-panels'} ref={panels} style={{'--split-ratio':ratio+'%'} as React.CSSProperties} hidden={!controller.tabs.length}>{controller.tabs.filter(tab=>mounted.includes(tab.path)||(visible&&tab.path===controller.active)).map(tab=>{const slot=slotFor(tab.path);return <Frame key={tab.path} tab={tab} active={tab.path===controller.active||slot==='secondary'} visible={visible} mobile={mobile} compact={compact} relative={relative} slot={slot} focused={focus===slot} onFocusPane={()=>slot&&setFocus(slot)} onClosePane={()=>{if(slot==='secondary')controller.setSplit(false);else if(controller.secondary){controller.assign('primary',controller.secondary);controller.setSplit(false);}}}/>;})}{split&&<div className="terminal-split-handle" role="separator" aria-label="Resize the split terminals" aria-orientation="vertical" aria-valuenow={Math.round(ratio)} aria-valuemin={20} aria-valuemax={80} tabIndex={0} onPointerDown={resize} onKeyDown={event=>{if(event.key==='ArrowLeft'){event.preventDefault();nudge(-2);}if(event.key==='ArrowRight'){event.preventDefault();nudge(2);}}}/>}</div><div className="terminal-empty" hidden={!!controller.tabs.length}><h2>No open terminals</h2><p>Open a blank terminal, or attach a session. Either stays here while you switch around Lectern.</p><div className="terminal-empty-actions"><NewTerminal machines={machines} projects={projects} onNew={onNew}/><button className="b" onClick={onBrowse}>Open sessions</button></div></div></section>;
}
