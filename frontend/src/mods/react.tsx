// React's side of mods: where their elements are drawn (docs/mods.md).
import { useEffect, useState, useSyncExternalStore, type ReactNode } from "react";
import { modHost, type ModElement, type RenderResult } from "./host";
import { safeHref } from "../plugins/contributions";

export function useModsVersion() {
  return useSyncExternalStore(modHost.subscribe, modHost.snapshot);
}

/**
 * What mods hide or add to one component. Drawn without them first; their
 * part arrives a moment later, and again whenever a mod asks to redraw.
 */
export function useModRender(component: string, props: Record<string, unknown>, owner?: string): RenderResult | null {
  const version = useModsVersion();
  const [result, setResult] = useState<RenderResult | null>(null);
  const key = JSON.stringify(props);
  useEffect(() => {
    if (!modHost.has("ui.render")) {
      setResult(null);
      return;
    }
    let alive = true;
    void modHost.render(component, JSON.parse(key), owner).then((r) => alive && setResult(r.hidden || r.append.length ? r : null));
    return () => { alive = false; };
  }, [component, key, owner, version]);
  return result;
}

const TONES = new Set(["dim", "accent", "warn", "danger", "ok"]);
const tone = (p: Record<string, unknown>) => (typeof p.tone === "string" && TONES.has(p.tone) ? ` mod-${p.tone}` : "");
const str = (v: unknown) => (v == null ? "" : String(v));

function ModElementView({ el }: { el: ModElement }): ReactNode {
  const p = el.props ?? {};
  const children = Array.isArray(p.children) ? (p.children as ModElement[]) : [];
  switch (el.type) {
    case "Box":
      return (
        <span className={"mod-box" + (p.direction === "column" ? " mod-col" : "")} style={{ gap: Number(p.gap) ? `${Number(p.gap) * 4}px` : undefined }}>
          {children.map((c, i) => <ModElementView key={i} el={c} />)}
        </span>
      );
    case "Text":
      return <span className={"mod-text" + tone(p) + (p.bold ? " mod-bold" : "") + (p.mono ? " mod-mono" : "")}>{str(p.text ?? p.children)}</span>;
    case "Badge":
      return <span className={"mod-badge" + tone(p)}>{str(p.text ?? p.children)}</span>;
    case "Button":
      return (
        <button type="button" className="b mod-button" onClick={(event) => { event.stopPropagation(); modHost.press(p.onPress); }}>
          {str(p.label ?? p.children)}
        </button>
      );
    case "Link": {
      const href = safeHref(str(p.href));
      if (!href) return <span className="mod-text">{str(p.label)}</span>;
      return <a className="mod-link" href={href} target={href.startsWith("#") ? undefined : "_blank"} rel="noreferrer" onClick={(event) => event.stopPropagation()}>{str(p.label ?? href)}</a>;
    }
  }
  return null;
}

export function ModElements({ elements, className = "" }: { elements: ModElement[]; className?: string }) {
  if (!elements.length) return null;
  return <span className={"mod-slot " + className} data-mod-slot>{elements.map((el, i) => <ModElementView key={i} el={el} />)}</span>;
}

/** The status component: each mod's $.ui.status text and its render. */
export function ModStatus() {
  useModsVersion();
  const rendered = useModRender("status", {});
  const texts = modHost.statusTexts();
  if (!texts.length && !rendered?.append.length) return null;
  return (
    <span className="mod-status" data-mod-status>
      {texts.map((s) => <span key={s.key} className="mod-text" title={s.key}>{s.text}</span>)}
      {rendered && <ModElements elements={rendered.append} />}
    </span>
  );
}

function Pane({ pane }: { pane: { key: string; id: string; title: string } }) {
  const rendered = useModRender("pane", { id: pane.id }, pane.key);
  return (
    <section className="mod-pane" data-mod-pane={pane.key + "/" + pane.id} aria-label={pane.title}>
      <header>
        <b>{pane.title}</b>
        <button type="button" className="b mod-close" aria-label="Close" onClick={() => modHost.closePane(pane.key, pane.id)}>×</button>
      </header>
      <div className="mod-pane-body">{rendered ? <ModElements elements={rendered.append} className="mod-col" /> : null}</div>
    </section>
  );
}

/** Panes mods opened with $.ui.open, docked on the right. */
export function ModPanes() {
  useModsVersion();
  if (!modHost.panes.length) return null;
  return <div className="mod-panes">{modHost.panes.map((p) => <Pane key={p.key + "/" + p.id} pane={p} />)}</div>;
}
