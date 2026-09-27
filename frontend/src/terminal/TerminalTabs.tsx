import {useCallback,useEffect,useRef,useState} from 'react';
import {viewportSliceFor, visibleViewport, virtualKeyboard} from './viewport';
import {orderProjectsByRecency, readProjectPreference} from '../project-preference';
import './terminal-tabs.css';
// Helpers the workspace (workspace/Workspace.tsx) uses for its terminal panes:
// the address check, the embedded terminal frame, the swipe recogniser and the
// New terminal picker.
export interface TerminalTab {path:string;label:string}
export function terminalPath(url:string){const parsed=new URL(url,location.origin),path=parsed.pathname.replace(/^\/term\//,'/terminal/').replace(/\/$/,'');if(parsed.origin!==location.origin||!/^\/terminal\/(session|attempt|project)\/[1-9]\d*$/.test(path))throw new Error('This terminal does not have a valid Lectern address.');return path;}
export const hashFor=(path:string|null)=>path&&path.startsWith('/terminal/')?'#terminals/'+path.split('/').slice(-2).join('/'):'#terminals';
export const buttonID=(path:string)=>'terminal-tab-'+path.replace(/^\/terminal\//,'').replace(/[^A-Za-z0-9_-]+/g,'-');
export interface SwipeOptions {enabled:()=>boolean;relative:(delta:number)=>boolean;width:()=>number;selected:()=>boolean;blocked:(target:EventTarget|null)=>boolean}
// Keep a deliberate swipe reachable in a narrow tab strip. The terminal body
// retains its 72px minimum; compact controls can leave the strip less room.
export function bindSwipe(target:Window|HTMLElement,options:SwipeOptions){const abort=new AbortController();let start:{x:number;y:number;at:number;maxVertical:number}|undefined;const on=(type:string,listener:(event:TouchEvent)=>void,passive=true)=>target.addEventListener(type,listener as EventListener,{signal:abort.signal,passive});
 on('touchstart',event=>{const touch=event.touches[0];if(!options.enabled()||event.touches.length!==1||!touch||options.blocked(event.target)){start=undefined;return;}start={x:touch.clientX,y:touch.clientY,at:performance.now(),maxVertical:0};});
 on('touchmove',event=>{const touch=event.touches[0];if(!start||event.touches.length!==1||!touch){start=undefined;return;}start.maxVertical=Math.max(start.maxVertical,Math.abs(touch.clientY-start.y));});on('touchcancel',()=>{start=undefined;});
 on('touchend',event=>{const saved=start;start=undefined;const touch=event.changedTouches[0];if(!saved||!options.enabled()||event.changedTouches.length!==1||!touch)return;const dx=touch.clientX-saved.x,dy=touch.clientY-saved.y,elapsed=performance.now()-saved.at,width=options.width(),threshold=Math.max(Math.min(72,width*.45),Math.min(140,width*.22));const flick=elapsed<=550&&saved.maxVertical<Math.max(48,threshold*.55)&&Math.abs(dx)>=threshold&&Math.abs(dx)>=Math.abs(dy)*1.6;if(!flick||options.selected())return;if(options.relative(dx<0?1:-1))event.preventDefault();},false);return()=>abort.abort();
}
// One embedded terminal page. `shown` means its pane is on screen in the
// layout; `primary` means it is the pane swipes and the phone keyboard act on.
export interface FrameProps {tab:TerminalTab;shown:boolean;visible:boolean;mobile:boolean;compact:boolean;primary:boolean;relative:(delta:number)=>boolean;onFocus?:()=>void}
export function TerminalFrame(props:FrameProps){const frame=useRef<HTMLIFrameElement>(null),latest=useRef(props);latest.current=props;
 const notify=()=>{const value=latest.current;if(!value.shown||!value.visible)return;frame.current?.contentWindow?.postMessage({type:'lec-terminal-visible',mobile:value.mobile,compact:value.mobile&&value.compact},location.origin);};
 // The phone keyboard overlays the frame instead of resizing it, so the visible
 // slice of the frame is measured here and told to the terminal page. The frame
 // asks for one when it mounts: a message posted before it loads is lost.
 const measure=()=>{const value=latest.current,node=frame.current;if(!value.shown||!value.visible||!node)return;const win=node.contentWindow;if(!win)return;const slice=viewportSliceFor(node);win.postMessage({type:'lec-terminal-viewport',top:slice.top,height:slice.height},location.origin);};
 useEffect(()=>{notify();measure();},[props.shown,props.visible,props.mobile,props.compact,props.primary]);
 useEffect(()=>{const node=frame.current;if(!node)return;const abort=new AbortController(),signal=abort.signal,view=window.visualViewport;let settle:number|undefined;const run=()=>{measure();clearTimeout(settle);settle=window.setTimeout(measure,150);};window.addEventListener('resize',run,{signal});window.addEventListener('orientationchange',run,{signal});view?.addEventListener('resize',run,{signal});view?.addEventListener('scroll',run,{signal});virtualKeyboard()?.addEventListener('geometrychange',run,{signal});const observer=new ResizeObserver(run);observer.observe(node);return()=>{clearTimeout(settle);observer.disconnect();abort.abort();};},[]);
 // A deliberate horizontal flick on the terminal body is recognised inside the
 // frame, where scrolling, long-press selection and pinch already own the
 // gesture, and relayed here; negotiated mouse reporting no longer blocks it.
 useEffect(()=>{const onMessage=(event:MessageEvent)=>{const win=frame.current?.contentWindow;if(!win||event.source!==win||event.origin!==location.origin)return;const data:unknown=event.data;if(!data||typeof data!=='object'||!('type' in data))return;if(data.type==='lec-terminal-need-viewport'){measure();return;}if(data.type==='lec-terminal-focus'){latest.current.onFocus?.();return;}if(data.type!=='lec-terminal-swipe')return;const value=latest.current;if(!value.mobile||!value.visible||!value.shown||!value.primary)return;value.relative('direction' in data&&data.direction===-1?-1:1);};window.addEventListener('message',onMessage);return()=>window.removeEventListener('message',onMessage);},[]);
 return <iframe ref={frame} title={props.tab.label+' terminal'} src={props.tab.path+'?embed=1'} allow="clipboard-read; clipboard-write" onLoad={notify}/>;
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
export function NewTerminal({machines,projects,onNew}:{machines:TerminalMachine[];projects:TerminalProject[];onNew:(choice?:NewTerminalChoice)=>Promise<void>}){
 const [busy,setBusy]=useState(false),[query,setQuery]=useState(""),[recent,setRecent]=useState<number[]>(()=>readProjectPreference().recent),menu=useRef<HTMLDetailsElement>(null),search=useRef<HTMLInputElement>(null);
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
 useEffect(()=>{place();},[query,projects,machines,recent,place]);
 const start=(choice?:NewTerminalChoice)=>{if(menu.current)menu.current.open=false;setQuery("");setBusy(true);void onNew(choice).finally(()=>{setRecent(readProjectPreference().recent);setBusy(false);});};
 useEffect(()=>{const close=(event:PointerEvent)=>{if(menu.current?.open&&event.target instanceof Node&&!menu.current.contains(event.target))menu.current.open=false;};document.addEventListener('pointerdown',close);return()=>document.removeEventListener('pointerdown',close);},[]);
 const q=query.trim().toLowerCase(),matches=(...parts:string[])=>!q||parts.some(part=>part.toLowerCase().includes(q));
 const shownMachines=machines.filter(machine=>matches(machine.name));
 const shownProjects=projects.filter(project=>matches(project.name,project.repo_path,project.target_name||''));
 const caret=machines.length>1||projects.length>0;
 // Recent projects stay on top. The group is refreshed every time the menu
 // opens, so a shell opened since the last look is already in place.
 const {recent:recentProjects,rest:otherProjects}=orderProjectsByRecency(shownProjects,recent);
 const projectRow=(project:TerminalProject)=><button key={'project-'+project.id} role="menuitem" className="terminal-new-project" disabled={busy} onClick={()=>start({projectID:project.id})}><span className="terminal-new-project-name">{project.name}</span><span className="terminal-new-project-path">{project.repo_path}{project.target_name?' · '+project.target_name:''}</span></button>;
 return <div className="terminal-new"><button className="terminal-new-main" disabled={busy} aria-label="New terminal" title="Open a blank shell on the default machine" onClick={()=>start()}>{busy?'Opening…':<>＋<span className="terminal-new-label"> New terminal</span></>}</button>{caret&&<details className="terminal-new-machines" ref={menu} onToggle={()=>{if(menu.current?.open){place();setRecent(readProjectPreference().recent);if(matchMedia("(pointer:fine)").matches)search.current?.focus({preventScroll:true});}else setQuery("");}}><summary onClick={event=>{event.preventDefault();if(menu.current){menu.current.open=!menu.current.open;setRecent(readProjectPreference().recent);place();}}} aria-label="New terminal in a project or on another machine" title="New terminal in a project or on another machine">▾</summary><div className="terminal-actions-panel terminal-new-picker" role="menu"><input ref={search} className="terminal-new-search" type="search" value={query} aria-label="Search projects and machines" placeholder="Search projects and machines" onChange={event=>setQuery(event.target.value)}/>{recentProjects.length>0&&<div className="terminal-new-group" role="presentation">Recent</div>}{recentProjects.map(projectRow)}{otherProjects.length>0&&<div className="terminal-new-group" role="presentation">{recentProjects.length>0?'Other projects':'Projects'}</div>}{otherProjects.map(projectRow)}{shownMachines.length>0&&<div className="terminal-new-group" role="presentation">Machines</div>}{shownMachines.map(machine=><button key={'machine-'+machine.id} role="menuitem" disabled={busy} onClick={()=>start({machineID:machine.id})}>{machine.name}</button>)}{shownMachines.length===0&&shownProjects.length===0&&<div className="terminal-new-empty" role="presentation">No matching projects or machines</div>}</div></details>}</div>;
}
