// The menu a right-click on a path or web address in the terminal opens
// (Engine.linkMenu): open it, download it, copy it, hand the path to the
// agent, or open it in a file pane beside the terminal. The native client
// offers the same actions through tmux (docs/terminal-client.md).
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { t, useLocale } from "../i18n";
import type { TerminalLink } from "./links";

export interface LinkActions {
  open: (link: TerminalLink) => void;
  download: (path: string) => void;
  copy: (text: string) => void;
  // A workspace path as a full path on the session's machine.
  absolute: (path: string) => string;
  send: (path: string) => void;
  // Present only inside the workspace, where a file pane can open beside.
  beside?: (path: string, line?: number, column?: number) => void;
}

export function LinkMenu({ link, point, actions, onClose }: { link: TerminalLink; point: { x: number; y: number }; actions: LinkActions; onClose: () => void }) {
  useLocale();
  const menu = useRef<HTMLDivElement>(null);
  const [place, setPlace] = useState(point);
  // Kept on screen: flipped left or up when it would run off the edge.
  useLayoutEffect(() => {
    const box = menu.current?.getBoundingClientRect();
    if (!box) return;
    setPlace({
      x: Math.max(4, point.x + box.width > innerWidth - 4 ? point.x - box.width : point.x),
      y: Math.max(4, point.y + box.height > innerHeight - 4 ? point.y - box.height : point.y),
    });
    menu.current?.querySelector<HTMLElement>("[role=menuitem]")?.focus();
  }, [point.x, point.y]);
  useEffect(() => {
    const close = (event: Event) => {
      if (event instanceof KeyboardEvent) {
        if (event.key !== "Escape") return;
        event.preventDefault();
      } else if (event.target instanceof Node && menu.current?.contains(event.target)) return;
      onClose();
    };
    document.addEventListener("pointerdown", close, true);
    document.addEventListener("keydown", close, true);
    window.addEventListener("blur", onClose);
    return () => {
      document.removeEventListener("pointerdown", close, true);
      document.removeEventListener("keydown", close, true);
      window.removeEventListener("blur", onClose);
    };
  }, [onClose]);
  const item = (id: string, label: string, run: () => void) => (
    <button
      key={id}
      id={"link-menu-" + id}
      role="menuitem"
      onClick={() => {
        onClose();
        run();
      }}
    >
      {label}
    </button>
  );
  const items =
    link.kind === "url"
      ? [item("open", t("files.link.openBrowser"), () => actions.open(link)), item("copy", t("files.link.copyLink"), () => actions.copy(link.url))]
      : [
          item("open", t("files.link.open"), () => actions.open(link)),
          item("download", t("files.link.download"), () => actions.download(link.path)),
          item("copy", t("files.link.copyPath"), () => actions.copy(actions.absolute(link.path))),
          item("send", t("files.link.send"), () => actions.send(link.path)),
          ...(actions.beside ? [item("beside", t("files.link.beside"), () => actions.beside!(link.path, link.line, link.column))] : []),
        ];
  return (
    <div
      ref={menu}
      id="link-menu"
      className="link-menu"
      role="menu"
      aria-label={link.kind === "url" ? link.url : link.path}
      data-back-overlay="45"
      style={{ left: place.x, top: place.y }}
      onKeyDown={(event) => {
        if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
        event.preventDefault();
        const buttons = [...(menu.current?.querySelectorAll<HTMLElement>("[role=menuitem]") || [])];
        const at = buttons.indexOf(document.activeElement as HTMLElement);
        buttons[(at + (event.key === "ArrowDown" ? 1 : buttons.length - 1)) % buttons.length]?.focus();
      }}
    >
      <p className="link-menu-target" title={link.kind === "url" ? link.url : link.path}>
        {link.kind === "url" ? link.url : link.path}
        {link.kind === "file" && link.external && <span className="link-menu-note"> · {t("files.outsideReadOnly")}</span>}
      </p>
      {items}
    </div>
  );
}
