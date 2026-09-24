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
import { SessionReview } from "./review/SessionReview";
import { Evals } from "./evals/Evals";
import { TerminalTabs, useTerminalTabs } from "./terminal/TerminalTabs";
import { Palette, type Command } from "./shell/Palette";
import { Deck, Approvals } from "./shell/LiveViews";
import { Icon } from "./shell/Icon";
import { Modal } from "./sessions/Modal";
import { QuickSwitch, sessionModelLabel } from "./sessions/QuickSwitch";
import { requestSwitch, type SwitchRequest } from "./continuity/handoff";
import { SessionLineage } from "./continuity/SessionLineage";
import { SwitchProgressPanel, type PendingSwitch } from "./continuity/SwitchProgress";
import { envFromWindow, pushAvailability } from "./push";
const SWITCH_STORAGE = 'lec-pending-switches';
const PUSH_PROMPT_DISMISSED = 'lec-push-prompt-dismissed';
// The Needs-you push prompt is one-time and dismissible: once a person taps
// "Not now" it must not come back on every visit. Per-device (localStorage),
// like every other UI preference this app keeps client-side.
function pushPromptDismissed(): boolean {
  try {
    return localStorage.getItem(PUSH_PROMPT_DISMISSED) === '1';
  } catch {
    return false;
  }
}
function dismissPushPrompt() {
  try {
    localStorage.setItem(PUSH_PROMPT_DISMISSED, '1');
  } catch {
    /* best-effort — a private window losing the dismissal just re-shows it */
  }
}
// A pending switch remembers where the context is going as well as the wrap it
// started after, so a reload can keep showing progress and offer a retry.
function savedSwitches(): Record<string, PendingSwitch> {
  try {
    const value = JSON.parse(sessionStorage.getItem(SWITCH_STORAGE) || '{}');
    const out: Record<string, PendingSwitch> = {};
    for (const [id, raw] of Object.entries(value)) {
      if (!/^\d+$/.test(id)) continue;
      if (typeof raw === 'number') {
        out[id] = { after: raw, generation: 0, destination: '', agent: '', model: '', profile: 0 };
        continue;
      }
      if (!raw || typeof raw !== 'object') continue;
      const row = raw as Partial<PendingSwitch>;
      const successor = row.successor && Number(row.successor.id)
        ? { id: Number(row.successor.id), name: String(row.successor.name || '') }
        : undefined;
      out[id] = {
        after: typeof row.after === 'number' ? row.after : 0,
        generation: Number(row.generation) || 0,
        destination: String(row.destination || ''),
        agent: String(row.agent || ''),
        model: String(row.model || ''),
        profile: Number(row.profile) || 0,
        error: row.error ? String(row.error) : undefined,
        successor,
      };
    }
    return out;
  } catch { return {}; }
}
const tabs = [
  "board",
  "sessions",
  "terminals",
  "media",
  "deck",
  "approvals",
  "targets",
  "evals",
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
  evals: "Agent tests",
};
// "evals" opens a modal over the current view rather than a page of its own
// (see showEvals below) — everywhere a tab click would otherwise navigate,
// it toggles that modal instead. Kept out of `view`/`isTab`'s routing so an
// evals modal never fights the board/sessions/etc. hash it was opened over.
const opensModal = (tab: Tab) => tab === "evals";
const isTab = (value: string): value is Tab =>
  tabs.some((tab) => tab === value);
// #media/<session id> narrows the feed to one session's posts.
const mediaSessionOf = (hash: string) => {
  const match = /^#?media\/([1-9]\d*)$/.exec(hash);
  return match ? Number(match[1]) : null;
};
export default function App() {
  const [view, setView] = useState<Tab>("board"),
    [showEvals, setShowEvals] = useState(false),
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
    [review, setReview] = useState<SessionView>(),
    [mergeReview, setMergeReview] = useState<SessionView>(),
    [switchSession, setSwitchSession] = useState<SessionView>(),
    [pendingSwitches, setPendingSwitches] = useState(savedSwitches),
    // This device's current push subscription endpoint, or null once it is
    // known there isn't one. Undefined (the initial value) means "not
    // checked yet" — the Needs-you prompt stays hidden until it is, so it
    // never flashes on for a device that turns out to already be subscribed.
    [pushEndpoint, setPushEndpoint] = useState<string | null | undefined>(undefined),
    [pushPromptGone, setPushPromptGone] = useState(pushPromptDismissed);
  const pushAvail = useMemo(() => pushAvailability(envFromWindow(window)), []);
  const switching = useRef(pendingSwitches), completingSwitches = useRef(new Set<number>());
  const saveSwitches = useCallback((next: Record<string,PendingSwitch>)=>{
    switching.current = next; setPendingSwitches(next);
    try { sessionStorage.setItem(SWITCH_STORAGE, JSON.stringify(next)); } catch {}
  },[]);
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
    if (isTab(kind) && opensModal(kind)) {
      setShowEvals(true);
      return;
    }
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
  const completeSwitch = useCallback(async (source: number, next: {id:number;name:string})=>{
    const current = switching.current[String(source)];
    if (!current || completingSwitches.current.has(source)) return;
    completingSwitches.current.add(source);
    try {
      const response = await api.request<{url:string}>(`/sessions/${next.id}/terminal`,{method:'POST'});
      openTerminal(response.url,next.name);
      terminals.close(`/terminal/session/${source}`);
      const remaining = {...switching.current}; delete remaining[String(source)]; saveSwitches(remaining);
      notice('Switched. The original session is still available in Sessions.');
    } catch(error) {
      // The successor exists even though this browser could not open it. Keep
      // it so the operator can retry the attach — and never ask the agent to
      // hand off a second time.
      const latest = switching.current[String(source)] || current;
      saveSwitches({...switching.current,[String(source)]:{...latest,successor:{id:next.id,name:next.name},error:String(error)}});
      notice(String(error),true);
    }
    finally {
      completingSwitches.current.delete(source);
    }
  },[api,openTerminal,terminals.close,notice,saveSwitches]);
  const reopenSwitch = useCallback((source: number)=>{
    const current = switching.current[String(source)];
    if (!current?.successor) return;
    void completeSwitch(source, current.successor);
  },[completeSwitch]);
  const switchFailure = useCallback((source: number, error: string)=>{
    const current = switching.current[String(source)];
    if (!current) return;
    saveSwitches({...switching.current,[String(source)]:{...current,error}});
    notice(error,true);
  },[notice,saveSwitches]);
  const retrySwitch = useCallback(async (source: number)=>{
    const current = switching.current[String(source)];
    // A successor already exists: the only retry is opening it, never another
    // handoff.
    if (!current || current.successor) return;
    try {
      const result = await requestSwitch(api, source, {agent:current.agent,model:current.model,profile:current.profile,destination:current.destination});
      const latest = switching.current[String(source)];
      // Only an accepted request clears the previous failure and restarts the
      // progress surface; a rejected retry keeps showing why it failed.
      saveSwitches({...switching.current,[String(source)]:{...(latest||current),after:result.after_wrap_id,generation:((latest||current).generation||0)+1,error:undefined,successor:undefined}});
      notice('Switching… waiting for the current agent to save its handoff.');
      void refresh().catch(error=>notice(String(error),true));
    } catch(error) { switchFailure(source, String(error)); }
  },[api,notice,refresh,saveSwitches,switchFailure]);
  const dismissSwitch = useCallback((source: number)=>{
    const remaining = {...switching.current}; delete remaining[String(source)]; saveSwitches(remaining);
  },[saveSwitches]);
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
      if (kind && isTab(kind) && opensModal(kind)) {
        setShowEvals(true);
        return;
      }
      if (kind && isTab(kind)) {
        setView(kind);
        if (kind === "media") setMediaSession(mediaSessionOf(raw));
        return;
      }
      // Fresh phone loads land on Sessions, where the work is; an explicit
      // hash, or a view the operator already chose, still wins.
      let fallback = "board";
      try {
        if (matchMedia("(max-width: 1023px)").matches) fallback = "sessions";
      } catch {}
      let saved = fallback;
      try {
        saved = localStorage.getItem("lec-last-view") || fallback;
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
      "session.check",
    ])
      stream.addEventListener(event, update);
    stream.addEventListener("session_handoff", (event) => {
      try {
        const row = JSON.parse((event as MessageEvent<string>).data) as {
          ok?: boolean;
          session_id?: number;
          successor?: {id:number;name:string};
          remembered?: boolean;
          error?: string;
        };
        if(row.session_id && row.session_id in switching.current) {
          if(row.ok && row.successor) void completeSwitch(row.session_id,row.successor);
          else switchFailure(row.session_id,row.error || 'The switch did not complete. Your original session is still available.');
          update();
          return;
        }
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
  }, [api, authVersion, refresh, notice, completeSwitch, switchFailure]);
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
  // Learn whether this device already has a live push subscription, so the
  // Needs-you prompt and the Settings state both reflect reality on load
  // rather than assuming "not subscribed" until someone taps the button.
  useEffect(() => {
    if (!pushAvail.available) {
      setPushEndpoint(null);
      return;
    }
    let cancelled = false;
    void (async () => {
      try {
        const registration = await navigator.serviceWorker.getRegistration();
        const subscription = await registration?.pushManager.getSubscription();
        if (!cancelled) setPushEndpoint(subscription?.endpoint ?? null);
      } catch {
        if (!cancelled) setPushEndpoint(null);
      }
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pushAvail.available]);
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
      setPushEndpoint(value.endpoint || null);
      setPushPromptGone(true);
      dismissPushPrompt();
      notice("Push enabled on this device — sending a test notification");
      // So the owner sees, right away, that it actually works — rather than
      // finding out for the first time when a real alert silently fails to
      // arrive.
      try {
        await api.request("/settings/test-notification", { method: "POST" });
      } catch (error) {
        notice("Test notification: " + String(error), true);
      }
    } catch (error) {
      notice("Push: " + String(error), true);
    }
  }
  // Removes a device's subscription server-side; when it is this browser's
  // own, also tears down the live PushManager subscription so re-enabling
  // starts clean instead of handing the server back the same dead endpoint.
  async function unsubscribePush(endpoint: string) {
    try {
      await api.request("/push/subscribe", { method: "DELETE", body: { endpoint } });
      if (pushEndpoint === endpoint) {
        try {
          const registration = await navigator.serviceWorker?.getRegistration();
          const subscription = await registration?.pushManager.getSubscription();
          if (subscription && subscription.endpoint === endpoint) await subscription.unsubscribe();
        } catch {
          /* server-side removal already succeeded; a stale local subscription
             object is harmless — the next enable overwrites it */
        }
        setPushEndpoint(null);
      }
      notice("Unsubscribed");
    } catch (error) {
      notice("Unsubscribe: " + String(error), true);
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
            lec<b>tern</b>
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
            onMergeReview={setMergeReview}
            onOpenTask={openTask}
            onSwitch={setSwitchSession}
            onNotice={notice}
            pushPrompt={{
              show: pushAvail.available && pushEndpoint === null && !pushPromptGone,
              onEnable: () => void enablePush(),
              onDismiss: () => {
                setPushPromptGone(true);
                dismissPushPrompt();
              },
            }}
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
            pushAvailable={pushAvail.available}
            pushUnavailableReason={pushAvail.reason}
            pushEndpoint={pushEndpoint}
            onUnsubscribePush={unsubscribePush}
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
        switcher={(()=>{
          const active = sessions.find(s=>terminals.active===`/terminal/session/${s.id}` && s.agent!=='shell' && !s.ended_at && s.status!=='dead' && s.setup_state!=='creating');
          if(!active) return null;
          const busy = active.id in pendingSwitches || active.handoff_in_flight;
          return <>
            <button className="b terminal-agent-switch" aria-label="Switch agent or model" title={`Switch ${sessionModelLabel(active)}`} disabled={busy} onClick={()=>setSwitchSession(active)}>{busy?'Switching…':`⇄ ${sessionModelLabel(active)} ▾`}</button>
            <SessionLineage api={api} session={active} pending={busy} onOpen={(session)=>{ if(session.ended_at) setConversation({kind:'session',id:session.id,name:session.name}); else void attach(session.id); }} onOpenChat={(session)=>setConversation({kind:'session',id:session.id,name:session.name})}/>
          </>;
        })()}
      />
      {switchSession && <QuickSwitch api={api} session={switchSession} onClose={()=>setSwitchSession(undefined)} onStarted={(source,afterWrap,request:SwitchRequest)=>{
        saveSwitches({...switching.current,[String(source.id)]:{after:afterWrap,generation:1,destination:request.destination,agent:request.agent,model:request.model,profile:request.profile}});
        notice('Switching… waiting for the current agent to save its handoff.');
        void refresh().catch(error=>notice(String(error),true));
      }} onProfiles={()=>{setSwitchSession(undefined);setManageProfiles(true);}}/>}
      <SwitchProgressPanel api={api} pending={pendingSwitches} onReady={completeSwitch} onFailed={switchFailure} onRetry={(source)=>void retrySwitch(source)} onReopen={reopenSwitch} onDismiss={dismissSwitch}/>
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
            {(["media", "deck", "approvals", "targets", "evals"] as const).map((tab) => (
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
          session={conversation.kind === "session" ? sessions.find(row => row.id === conversation.id) : undefined}
          onOpenSession={(row) => setConversation({kind: "session", id: row.id, name: row.name})}
          onAttach={conversation.kind === "session" ? () => void attach(conversation.id) : undefined}
          api={api}
          onClose={() => setConversation(undefined)}
          onNotice={notice}
          onSwitch={(()=>{
            const session = conversation.kind==='session' && sessions.find(s=>s.id===conversation.id && s.agent!=='shell' && !s.ended_at && s.status!=='dead');
            return session ? ()=>{setConversation(undefined);setSwitchSession(session);} : undefined;
          })()}
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
      {mergeReview && (
        <SessionReview
          api={api}
          sessionId={mergeReview.id}
          name={mergeReview.name}
          onClose={() => setMergeReview(undefined)}
          onNotice={notice}
        />
      )}{" "}
      {showEvals && (
        <Evals
          api={api}
          projects={projects}
          onClose={() => setShowEvals(false)}
          onNotice={notice}
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
