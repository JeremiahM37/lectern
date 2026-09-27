// The pane types the app ships: attached terminals, a session's chat, a
// session's working-tree changes and its browser. Other features register their own (see
// registry.tsx); nothing here is special to the layout.
import { Conversation } from "../sessions/Conversation";
import { Review } from "../terminal/Review";
import { BrowserPane } from "../browser/BrowserPane";
import { TerminalFrame, terminalPath } from "../terminal/TerminalTabs";
import type { PaneRef } from "./layout";
import { PaneEmbedContext, registerPaneType } from "./registry";
import { t } from "../i18n";

export const terminalRef = (path: string, title: string): PaneRef => ({ id: path, kind: "terminal", title, params: { path } });
export const chatRef = (session: number, title: string): PaneRef => ({ id: `chat:session:${session}`, kind: "chat", title: t("panes.chatTitle", { name: title }), params: { session: String(session) } });
export const browserRef = (session: number, title: string): PaneRef => ({ id: `browser:session:${session}`, kind: "browser", title: t("panes.browserTitle", { name: title }), params: { session: String(session) } });
export const diffRef = (session: number, title: string): PaneRef => ({ id: `diff:session:${session}`, kind: "diff", title: t("panes.diffTitle", { name: title }), params: { session: String(session) } });

const sessionParam = (ref: PaneRef) => (/^[1-9]\d{0,12}$/.test(ref.params.session || "") ? Number(ref.params.session) : 0);

// The session a pane belongs to, when it belongs to one.
export function paneSession(ref: PaneRef | undefined): number {
  if (!ref) return 0;
  if (ref.kind === "terminal") {
    const match = /^\/terminal\/session\/(\d+)$/.exec(ref.id);
    return match ? Number(match[1]) : 0;
  }
  return sessionParam(ref);
}

let installed = false;
export function installBuiltinPanes() {
  if (installed) return;
  installed = true;
  registerPaneType({
    kind: "terminal",
    label: "Terminal",
    icon: "⌨",
    check: (ref) => {
      try {
        const path = terminalPath(ref.params.path || ref.id);
        return path === ref.id ? { ...ref, params: { path } } : null;
      } catch {
        return null;
      }
    },
    popout: (ref) => ref.id,
    render: ({ pane, shown, visible, mobile, compact, primary, relative, focus }) => (
      <TerminalFrame tab={{ path: pane.id, label: pane.title }} shown={shown} visible={visible} mobile={mobile} compact={compact} primary={primary} relative={relative} onFocus={focus} />
    ),
  });
  registerPaneType({
    kind: "chat",
    label: "Chat",
    icon: "💬",
    embedsDialog: true,
    check: (ref) => (sessionParam(ref) && ref.id === `chat:session:${sessionParam(ref)}` ? ref : null),
    render: ({ pane, services, close }) => {
      const id = sessionParam(pane);
      return (
        <PaneEmbedContext.Provider value={true}>
          <Conversation
            kind="session"
            id={id}
            name={services.sessions.find((row) => row.id === id)?.name || pane.title}
            api={services.api}
            session={services.sessions.find((row) => row.id === id)}
            onClose={close}
            onNotice={services.notice}
            onAttach={() => services.attach(id)}
            onOpenSession={(row) => services.openPane(chatRef(row.id, row.name))}
          />
        </PaneEmbedContext.Provider>
      );
    },
  });
  registerPaneType({
    kind: "browser",
    label: "Browser",
    icon: "◎",
    check: (ref) => (sessionParam(ref) && ref.id === `browser:session:${sessionParam(ref)}` ? ref : null),
    render: ({ pane, services, close }) => {
      const id = sessionParam(pane);
      return <BrowserPane api={services.api} sessionId={id} name={services.sessions.find((row) => row.id === id)?.name || pane.title} onClose={close} onNotice={services.notice} />;
    },
  });
  registerPaneType({
    kind: "diff",
    label: "Changes",
    icon: "±",
    embedsDialog: true,
    check: (ref) => (sessionParam(ref) && ref.id === `diff:session:${sessionParam(ref)}` ? ref : null),
    render: ({ pane, close }) => (
      <PaneEmbedContext.Provider value={true}>
        <Review kind="session" id={String(sessionParam(pane))} name={pane.title} onClose={close} />
      </PaneEmbedContext.Provider>
    ),
  });
}
