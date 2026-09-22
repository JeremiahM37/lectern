import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { authToken, createDeckApi, withToken } from "./api";
import type {
  Approval,
  LiveView,
  Media as MediaRow,
  Project,
  SessionView,
  Target,
  TaskView,
} from "./types";
import { Media } from "./media/Media";
import { Board } from "./board/Board";
import { Sessions } from "./sessions/Sessions";
import { Conversation } from "./sessions/Conversation";
import { NativeSearch } from "./sessions/SavedConversations";
import { LaunchProfiles } from "./settings/LaunchProfiles";
import { Settings } from "./settings/Settings";
import { Review } from "./terminal/Review";
import { TerminalTabs, useTerminalTabs } from "./terminal/TerminalTabs";
import { Palette, type Command } from "./shell/Palette";
import { Deck, Approvals } from "./shell/LiveViews";
import { Icon } from "./shell/Icon";
import { Modal } from "./sessions/Modal";
const tabs = [
  "board",
  "sessions",
  "terminals",
  "media",
  "deck",
  "approvals",
  "targets",
] as const;
type Tab = (typeof tabs)[number];
const labels: Record<Tab, string> = {
  board: "Board",
  sessions: "Sessions",
  terminals: "Terminals",
  media: "Media",
  deck: "Deck",
  approvals: "Approvals",
  targets: "Settings",
};
const isTab = (value: string): value is Tab =>
  tabs.some((tab) => tab === value);
// #media/<session id> narrows the feed to one session's posts.
const mediaSessionOf = (hash: string) => {
  const match = /^#?media\/([1-9]\d*)$/.exec(hash);
  return match ? Number(match[1]) : null;
};
export default function App() {
  const [view, setView] = useState<Tab>("board"),
    [projects, setProjects] = useState<Project[]>([]),
    [targets, setTargets] = useState<Target[]>([]),
    [tasks, setTasks] = useState<TaskView[]>([]),
    [sessions, setSessions] = useState<SessionView[]>([]),
    [approvals, setApprovals] = useState<Approval[]>([]),
    [media, setMedia] = useState<MediaRow[]>([]),
    [liveViews, setLiveViews] = useState<LiveView[]>([]),
    [liveEnabled, setLiveEnabled] = useState(false),
    [mediaSession, setMediaSession] = useState<number | null>(null),
    [version, setVersion] = useState(0),
    [connected, setConnected] = useState(false),
    [palette, setPalette] = useState(false),
    [search, setSearch] = useState(false),
    [unauthorized, setUnauthorized] = useState(false),
    [token, setToken] = useState(authToken),
    [authVersion, setAuthVersion] = useState(0),
    [authError, setAuthError] = useState(""),
    [toasts, setToasts] = useState<
      { id: number; text: string; error: boolean }[]
    >([]),
    [openTaskId, setOpenTaskId] = useState<number>(),
    [newTaskVersion, setNewTaskVersion] = useState(0),
    [openTaskVersion, setOpenTaskVersion] = useState(0),
    [routinesVersion, setRoutinesVersion] = useState(0),
    [sessionAction, setSessionAction] = useState<{
      kind: "new" | "discover";
      version: number;
    }>(),
    [section, setSection] = useState({ name: "machines", version: 0 }),
    [projectEdit, setProjectEdit] = useState<{ id: number; version: number }>(),
    [launchProfilesVersion, setLaunchProfilesVersion] = useState(0),
    [manageProfiles, setManageProfiles] = useState(false),
    [conversation, setConversation] = useState<{
      kind: "task" | "session";
      id: number;
      name: string;
    }>(),
    [review, setReview] = useState<SessionView>();
  const root = useRef<HTMLElement>(null),
    toastCounter = useRef(0),
    refreshGeneration = useRef(0),
    viewRef = useRef(view);
  viewRef.current = view;
  const notice = useCallback((text: string, error = false) => {
    const id = ++toastCounter.current;
    setToasts((old) => [...old, { id, text, error }]);
    window.setTimeout(
      () => setToasts((old) => old.filter((row) => row.id !== id)),
      4200,
    );
  }, []);
  const api = useMemo(
    () => createDeckApi({ onUnauthorized: () => setUnauthorized(true) }),
    [],
  );
  const navigate = useCallback((hash: string) => {
    const kind = hash.replace(/^#/, "").split("/")[0] || "board";
    if (isTab(kind)) {
      setView(kind);
      if (kind === "media") setMediaSession(mediaSessionOf(hash));
      try {
        localStorage.setItem("lec-last-view", kind);
      } catch {}
      history.replaceState(null, "", hash);
    }
  }, []);
  const terminals = useTerminalTabs(navigate);
  const mediaCounts = useMemo(() => {
    const counts: Record<number, number> = {};
    for (const row of media)
      if (row.session_id != null)
        counts[row.session_id] = (counts[row.session_id] || 0) + 1;
    return counts;
  }, [media]);
  const refresh = useCallback(async () => {
    const generation = ++refreshGeneration.current;
    const results = await Promise.allSettled([
      api.projects(),
      api.targets(),
      api.tasks(),
      api.request<SessionView[]>("/sessions?include_setup_failures=true"),
      api.request<Approval[]>("/approvals?status=pending"),
      api.request<MediaRow[]>("/media?limit=200"),
      api.request<{ enabled: boolean; views: LiveView[] }>("/live"),
    ]);
    if (generation !== refreshGeneration.current) return;
    const [p, t, j, s, a, m, l] = results;
    if (p.status === "fulfilled") setProjects(p.value);
    if (t.status === "fulfilled") setTargets(t.value);
    if (j.status === "fulfilled") setTasks(j.value);
    if (s.status === "fulfilled") setSessions(s.value);
    if (a.status === "fulfilled") setApprovals(a.value);
    if (m.status === "fulfilled") setMedia(m.value);
    if (l.status === "fulfilled") {
      setLiveViews(l.value.views);
      setLiveEnabled(l.value.enabled);
    }
    setVersion((old) => old + 1);
    const failed = results.find((result) => result.status === "rejected");
    if (failed?.status === "rejected") throw failed.reason;
  }, [api]);
  const openTerminal = useCallback(
    (url: string, label?: string) => {
      setConversation(undefined);
      terminals.open(url, label);
    },
    [terminals.open],
  );
  const attach = useCallback(
    async (id: number) => {
      try {
        const response = await api.request<{ url: string }>(
          `/sessions/${id}/terminal`,
          { method: "POST" },
        );
        openTerminal(response.url, sessions.find((row) => row.id === id)?.name);
      } catch (error) {
        notice(String(error), true);
      }
    },
    [api, openTerminal, sessions, notice],
  );
  const newTerminal = useCallback(
    async (machineID?: number) => {
      try {
        const shell = await api.request<SessionView>("/shells", {
          method: "POST",
          body: machineID ? { target_id: machineID } : {},
        });
        const response = await api.request<{ url: string }>(
          `/sessions/${shell.id}/terminal`,
          { method: "POST" },
        );
        openTerminal(response.url, shell.name);
      } catch (error) {
        notice(String(error), true);
      }
    },
    [api, openTerminal, notice],
  );
  const openTask = useCallback(
    (id: number) => {
      setOpenTaskId(id);
      setOpenTaskVersion((old) => old + 1);
      navigate("#board");
      setView("board");
      history.replaceState(null, "", `#task/${id}`);
    },
    [navigate],
  );
  const newTask = () => {
    navigate("#board");
    setNewTaskVersion((old) => old + 1);
  };
  const sessionCommand = (kind: "new" | "discover") => {
    navigate("#sessions");
    setSessionAction({ kind, version: Date.now() });
  };
  const settings = (name: string) => {
    setSection({ name, version: Date.now() });
    navigate("#targets");
  };
  useEffect(() => {
    const apply = () => {
      let raw = "";
      try {
        raw = decodeURIComponent(location.hash.slice(1));
      } catch {
        return;
      }
      const [kind, id, terminalID] = raw.split("/");
      if (
        kind === "terminals" &&
        /^(session|attempt|project)$/.test(id || "") &&
        /^[1-9]\d*$/.test(terminalID || "")
      ) {
        terminals.open(`/terminal/${id}/${terminalID}`);
        return;
      }
      if (kind === "task" && /^[1-9]\d*$/.test(id || "")) {
        setView("board");
        setOpenTaskId(Number(id));
        setOpenTaskVersion((old) => old + 1);
        return;
      }
      if (kind === "session") {
        setView("sessions");
        return;
      }
      if (kind && isTab(kind)) {
        setView(kind);
        if (kind === "media") setMediaSession(mediaSessionOf(raw));
        return;
      }
      let saved = "board";
      try {
        saved = localStorage.getItem("lec-last-view") || "board";
      } catch {}
      setView(
        isTab(saved)
          ? saved === "terminals" && !terminals.active
            ? "sessions"
            : saved
          : "board",
      );
    };
    apply();
    window.addEventListener("hashchange", apply);
    return () => window.removeEventListener("hashchange", apply);
  }, [terminals.open]);
  useEffect(() => {
    let alive = true;
    void refresh().catch((error) => {
      if (alive) notice(String(error), true);
    });
    const stream = new EventSource(withToken("/api/stream"));
    let opened = false;
    stream.onopen = () => {
      setConnected(true);
      if (opened) void refresh().catch((error) => notice(String(error), true));
      opened = true;
    };
    stream.onerror = () => setConnected(false);
    const update = () =>
      void refresh().catch((error) => notice(String(error), true));
    for (const event of [
      "task",
      "approval",
      "session",
      "session_dismissed",
      "task_deleted",
      "media",
      "media_deleted",
      "live",
    ])
      stream.addEventListener(event, update);
    stream.addEventListener("session_handoff", (event) => {
      try {
        const row = JSON.parse((event as MessageEvent<string>).data) as {
          ok?: boolean;
          successor?: boolean;
          remembered?: boolean;
          error?: string;
        };
        notice(
          row.ok
            ? "Handoff written" +
                (row.successor ? " — successor session started" : "") +
                (row.remembered ? " · remembered" : "")
            : "Handoff failed: " + (row.error || "unknown"),
          !row.ok,
        );
      } catch {
        notice("Could not read handoff result.", true);
      }
      update();
    });
    const visibility = () => {
      if (document.visibilityState === "visible") update();
    };
    document.addEventListener("visibilitychange", visibility);
    const interval = window.setInterval(() => {
      if (document.visibilityState === "visible") update();
    }, 30000);
    return () => {
      alive = false;
      refreshGeneration.current++;
      stream.close();
      clearInterval(interval);
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [api, authVersion, refresh, notice]);
  useEffect(() => {
    document.body.classList.toggle("terminals-open", view === "terminals");
    return () => document.body.classList.remove("terminals-open");
  }, [view]);
  useEffect(() => {
    const element = root.current;
    if (!element) return;
    const fit = () =>
      document.documentElement.style.setProperty(
        "--terminal-top",
        `${element.getBoundingClientRect().bottom}px`,
      );
    const observer = new ResizeObserver(fit);
    observer.observe(element);
    fit();
    window.addEventListener("resize", fit);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", fit);
    };
  }, []);
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if (
        (event.ctrlKey || event.metaKey) &&
        !event.altKey &&
        !event.isComposing &&
        event.key.toLowerCase() === "k"
      ) {
        event.preventDefault();
        setPalette((old) => !old);
      }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, []);
  useEffect(() => {
    if (navigator.serviceWorker)
      void navigator.serviceWorker
        .register("/sw.js")
        .catch((error) => notice("Offline support: " + String(error), true));
  }, [notice]);
  async function enablePush() {
    try {
      if (!navigator.serviceWorker || !window.Notification)
        throw new Error("Notifications require a supported secure browser.");
      const registration = await navigator.serviceWorker.ready;
      if ((await Notification.requestPermission()) !== "granted")
        throw new Error("Notifications not granted.");
      const { key } = await api.request<{ key: string }>("/push/vapid");
      const raw = atob(
          key.replace(/-/g, "+").replace(/_/g, "/") +
            "=".repeat((4 - (key.length % 4)) % 4),
        ),
        applicationServerKey = Uint8Array.from(raw, (char) =>
          char.charCodeAt(0),
        );
      const subscription =
        (await registration.pushManager.getSubscription()) ||
        (await registration.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey,
        }));
      const value = subscription.toJSON();
      await api.request("/push/subscribe", {
        method: "POST",
        body: {
          endpoint: value.endpoint || "",
          expirationTime: value.expirationTime ?? null,
          keys: value.keys || {},
        },
      });
      notice("Push enabled on this device");
    } catch (error) {
      notice("Push: " + String(error), true);
    }
  }
  async function saveToken() {
    setAuthError("");
    localStorage.setItem("lec-token", token);
    try {
      await api.projects();
      setUnauthorized(false);
      setAuthVersion((old) => old + 1);
    } catch (error) {
      setAuthError(String(error));
    }
  }
  const commands: Command[] = [
    {
      id: "routines",
      title: "Routines",
      category: "Actions",
      detail: "Saved jobs and active runs",
      keywords: "schedule takeover",
      run: () => {
        navigate("#board");
        setRoutinesVersion((old) => old + 1);
      },
    },
    {
      id: "new-session",
      title: "New session",
      category: "Actions",
      detail: "Start an interactive agent",
      keywords: "create launch",
      run: () => sessionCommand("new"),
    },
    {
      id: "new-task",
      title: "New task",
      category: "Actions",
      detail: "Plan or dispatch work",
      keywords: "create",
      run: newTask,
    },
    {
      id: "saved-search",
      title: "Search saved conversations",
      category: "Actions",
      keywords: "history messages content native",
      run: () => setSearch(true),
    },
    {
      id: "discover",
      title: "Find running agents",
      category: "Actions",
      keywords: "adopt restore untracked",
      run: () => sessionCommand("discover"),
    },
    {
      id: "launch-profiles",
      title: "Manage launch profiles",
      category: "Actions",
      keywords: "profiles accounts configuration",
      run: () => setManageProfiles(true),
    },
    ...tabs.map((tab) => ({
      id: `nav-${tab}`,
      title:
        tab === "board"
          ? "Task board"
          : tab === "terminals"
            ? "Open terminals"
            : labels[tab],
      category: "Navigate",
      keywords: "navigate view",
      run: () => navigate(tab === "terminals" ? terminals.hash : "#" + tab),
    })),
    ...[
      ["machines", "Targets", "ssh remote local machines"],
      ["projects", "Projects", "repositories workspaces"],
      ["notifications", "Notifications", "alerts push"],
      ["about", "Usage and about", "settings version costs"],
      ["agents", "Agents", "agent runners commands custom providers models"],
    ].map(([name, title, keywords]) => ({
      id: "settings-" + name,
      title: title!,
      category: "Navigate",
      detail: "Settings",
      keywords,
      run: () => settings(name!),
    })),
    ...sessions.map((session) => ({
      id: `session-${session.id}`,
      title: session.name || `Session ${session.id}`,
      category: "Sessions",
      detail: [
        session.status,
        session.agent,
        session.group_path,
        session.project_name,
        session.target_name,
        session.workdir,
      ]
        .filter(Boolean)
        .join(" · "),
      keywords: "attach terminal " + (session.workspace?.branch || ""),
      run: () => attach(session.id),
    })),
    ...tasks.map((task) => ({
      id: `task-${task.id}`,
      title: task.title || `Task ${task.id}`,
      category: "Tasks",
      detail: [task.status, task.project_name].join(" · "),
      keywords: `task ${task.id}`,
      run: () => openTask(task.id),
    })),
    ...projects.map((project) => ({
      id: `project-${project.id}`,
      title: `Edit project: ${project.name}`,
      category: "Projects",
      detail: project.repo_path,
      keywords: "repository workspace configuration",
      run: () => {
        settings("projects");
        setProjectEdit({ id: project.id, version: Date.now() });
      },
    })),
  ];
  const live = sessions.filter(
    (session) => session.status !== "dead" && !session.ended_at,
  ).length;
  return (
    <>
      <div id="aurora" aria-hidden="true" />
      <header id="topbar" ref={root}>
        <div className="brand">
          <span className="brand-mark" aria-hidden="true">
            <Icon name="brand" size={22} />
          </span>
          <h1>
            agent<b>deck</b>
          </h1>
          <span className="brand-sub">mission control</span>
        </div>
        <button
          id="command-open"
          aria-label="Search sessions and actions"
          aria-haspopup="dialog"
          title="Search (Ctrl+K or ⌘K)"
          onClick={() => setPalette(true)}
        >
          <Icon name="search" size={18} />
          <span className="command-label">Search anything…</span>
          <kbd>Ctrl K</kbd>
        </button>
        <div className="top-status">
          <span
            id="conn-led"
            className={`led ${connected ? "led-on" : "led-err"}`}
            title="live connection"
          />
          <span id="conn-label">{connected ? "LIVE" : "RECONNECTING"}</span>
        </div>
      </header>
      <main id="view" hidden={view === "terminals"}>
        {view === "board" && (
          <Board
            api={api}
            refreshVersion={version}
            openTaskId={openTaskId}
            newTaskVersion={newTaskVersion}
            openTaskVersion={openTaskVersion}
            routinesVersion={routinesVersion}
            onExternalActionConsumed={(kind) => {
              if (kind === "task") setOpenTaskId(undefined);
              if (kind === "new") setNewTaskVersion(0);
              if (kind === "routines") setRoutinesVersion(0);
            }}
            onOpenTask={(task) => {
              setOpenTaskId(task.id);
              history.replaceState(null, "", `#task/${task.id}`);
            }}
            onChat={(task) =>
              setConversation({ kind: "task", id: task.id, name: task.title })
            }
            onNotice={notice}
            onOpenSession={(id) =>
              setConversation({ kind: "session", id, name: sessions.find((session) => session.id === id)?.name || "Session" })
            }
            onOpenTerminal={openTerminal}
          />
        )}{" "}
        {view === "sessions" && (
          <Sessions
            api={api}
            refreshVersion={version}
            action={sessionAction}
            onActionConsumed={() => setSessionAction(undefined)}
            onMetadataRefresh={() =>
              void refresh().catch((error) => notice(String(error), true))
            }
            projects={projects}
            targets={targets}
            mediaCounts={mediaCounts}
            onMedia={(id) => navigate("#media/" + id)}
            onOpenTerminal={openTerminal}
            onReview={setReview}
            onNotice={notice}
          />
        )}{" "}
        {view === "media" && (
          <Media
            api={api}
            rows={media}
            live={liveViews}
            liveEnabled={liveEnabled}
            targets={targets}
            sessionFilter={mediaSession}
            onFilter={(id) => navigate(id == null ? "#media" : "#media/" + id)}
            onChanged={() =>
              void refresh().catch((error) => notice(String(error), true))
            }
            onNotice={notice}
          />
        )}{" "}
        {view === "deck" && <Deck tasks={tasks} api={api} onTask={openTask} />}{" "}
        {view === "approvals" && (
          <Approvals
            rows={approvals}
            api={api}
            onChanged={() =>
              void refresh().catch((error) => notice(String(error), true))
            }
            onNotice={notice}
          />
        )}{" "}
        {view === "targets" && (
          <Settings
            api={api}
            section={section}
            projectEdit={projectEdit}
            launchProfilesVersion={launchProfilesVersion}
            onOpenTerminal={(url, title) => terminals.open(url, title)}
            onMetadataRefresh={() =>
              void refresh().catch((error) => notice(String(error), true))
            }
            onNotice={notice}
            onEnablePush={() => void enablePush()}
          />
        )}
      </main>
      <TerminalTabs
        controller={terminals}
        visible={view === "terminals"}
        machines={targets}
        onNew={newTerminal}
        onBrowse={() => navigate("#sessions")}
        onSearch={() => setPalette(true)}
      />
      <button
        id="fab"
        title="new task"
        aria-label="New task"
        hidden={view !== "board"}
        onClick={newTask}
      >
        <Icon name="plus" size={24} />
        <span>New task</span>
      </button>
      <nav id="tabbar">
        {tabs.map((tab) => (
          <button
            key={tab}
            data-tab={tab}
            className={[
              "tab",
              view === tab ? "on" : "",
              tab === "deck" ? "desktop-only" : "",
            ]
              .filter(Boolean)
              .join(" ")}
            onClick={() =>
              navigate(tab === "terminals" ? terminals.hash : "#" + tab)
            }
          >
            <span className="tab-ic" aria-hidden="true">
              <Icon name={tab} />
            </span>
            {labels[tab]}
            {tab === "sessions" && (
              <b id="sess-badge" className="badge dim" hidden={!live}>
                {live}
              </b>
            )}
            {tab === "terminals" && (
              <b
                id="terminal-badge"
                className="badge dim"
                hidden={!terminals.tabs.length}
              >
                {terminals.tabs.length}
              </b>
            )}
            {tab === "media" && (
              <b
                id="media-badge"
                className={liveViews.length ? "badge" : "badge dim"}
                hidden={!media.length && !liveViews.length}
                title={liveViews.length ? `${liveViews.length} live` : undefined}
              >
                {media.length + liveViews.length}
              </b>
            )}
            {tab === "approvals" && (
              <b id="appr-badge" className="badge" hidden={!approvals.length}>
                {approvals.length}
              </b>
            )}
          </button>
        ))}
        <details
          id="nav-overflow"
          className={`action-menu ${["media", "deck", "approvals", "targets"].includes(view) ? "on" : ""}`}
        >
          <summary aria-label="More pages">
            <span aria-hidden="true">···</span>More
            <b id="more-badge" className="badge" hidden={!approvals.length}>
              {approvals.length}
            </b>
          </summary>
          <div className="action-menu-panel">
            {(["media", "deck", "approvals", "targets"] as const).map((tab) => (
              <button
                key={tab}
                data-nav-target={tab}
                onClick={(event) => {
                  navigate("#" + tab);
                  event.currentTarget
                    .closest("details")
                    ?.removeAttribute("open");
                }}
              >
                {tab === "deck" ? "Deck · live overview" : labels[tab]}
              </button>
            ))}
          </div>
        </details>
      </nav>
      <div id="toasts" aria-live="polite">
        {toasts.map((toast) => (
          <div key={toast.id} className={`toast ${toast.error ? "err" : ""}`}>
            {toast.text}
          </div>
        ))}
      </div>
      {palette && (
        <Palette
          items={commands}
          refresh={refresh}
          onClose={() => setPalette(false)}
          onError={(message) => notice(message, true)}
        />
      )}{" "}
      {manageProfiles && (
        <LaunchProfiles
          api={api}
          onClose={() => setManageProfiles(false)}
          onChange={() =>
            void refresh().catch((error) => notice(String(error), true))
          }
        />
      )}
      {search && (
        <NativeSearch
          api={api}
          targets={targets}
          onClose={() => setSearch(false)}
          onFork={(session) => {
            setSearch(false);
            void refresh().catch((error) => notice(String(error), true));
            if (session.setup_state === "creating") {
              navigate("#sessions");
              notice(
                "Fork workspace setup started. Follow progress in Sessions.",
              );
            } else void attach(session.id);
          }}
          onNotice={notice}
        />
      )}{" "}
      {conversation && (
        <Conversation
          key={`${conversation.kind}-${conversation.id}`}
          {...conversation}
          api={api}
          onClose={() => setConversation(undefined)}
          onNotice={notice}
        />
      )}{" "}
      {review && (
        <Review
          kind="session"
          id={String(review.id)}
          name={review.name}
          onClose={() => setReview(undefined)}
        />
      )}{" "}
      {unauthorized && (
        <Modal
          className="token-dialog"
          aria-label="Access token"
          onCancel={(event) => event.preventDefault()}
        >
          <h2>Access token</h2>
          <p>This Lectern requires an access token.</p>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void saveToken();
            }}
          >
            <label htmlFor="access-token">Access token</label>
            <input
              id="access-token"
              type="password"
              autoComplete="current-password"
              value={token}
              onChange={(event) => setToken(event.target.value)}
            />
            {authError && <p role="alert">{authError}</p>}
            <button className="b ok" type="submit">
              Connect
            </button>
          </form>
        </Modal>
      )}
    </>
  );
}
