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
import { TasksHub } from "./trackers/TasksHub";
import { Evals } from "./evals/Evals";
import { TerminalTabs, useTerminalTabs } from "./workspace/Workspace";
import { FloatingTerminal } from "./workspace/FloatingTerminal";
import type { PaneServices } from "./workspace/registry";
import { loadPrefs } from "./prefs/store";
import { chordsFor, hasShortcutHandler, installForwardedShortcuts, installShortcutListener, runShortcut, useShortcuts } from "./shortcuts/dispatch";
import { displayChord } from "./shortcuts/chords";
import { SHORTCUTS } from "./shortcuts/registry";
import { currentAppearance, saveAppearance, uiZoom } from "./theme/appearance";
import { ACCENT_PRESETS, ZOOM_STEPS, resolveMode } from "./theme/app-theme";
import { t, useLocale } from "./i18n";
import { SECTIONS, settingsIndex } from "./settings/search-index";
import { loadPluginContributions, safeHref, usePluginContributions } from "./plugins/contributions";
import { Palette, type Command } from "./shell/Palette";
import { FirstRun } from "./shell/FirstRun";
import { Deck, Approvals } from "./shell/LiveViews";
import { Icon } from "./shell/Icon";
import { Modal } from "./sessions/Modal";
import { QuickSwitch, sessionModelLabel } from "./sessions/QuickSwitch";
import { rememberRecentProject } from "./project-preference";
import { requestSwitch, type SwitchRequest } from "./continuity/handoff";
import { SessionLineage } from "./continuity/SessionLineage";
import { SwitchProgressPanel, type PendingSwitch } from "./continuity/SwitchProgress";
import { envFromWindow, pushAvailability } from "./push";
import { inApp, nativeBridge } from "./native/bridge";
import { PUSH_EVENT, enableNativePush, syncNativePush } from "./native/push";
import { applyBadge, computeBadgeCount } from "./badge";
import type { NoticeAction } from "./types";
import { offlineCache } from "./api/offline";
import { OfflineBanner } from "./mobile/OfflineBanner";
import { PullToRefresh } from "./mobile/PullToRefresh";
import { noteView, setViewNavigator, useBackClose } from "./mobile/back";
import { viewerUnavailable } from "./terminal/viewer";
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
  "tasks",
  "terminals",
  "media",
  "deck",
  "approvals",
  "targets",
  "evals",
] as const;
type Tab = (typeof tabs)[number];
const labelKeys: Record<Tab, string> = {
  board: "nav.board",
  sessions: "nav.sessions",
  tasks: "nav.tasks",
  terminals: "nav.terminals",
  media: "nav.media",
  deck: "nav.deck",
  approvals: "nav.approvals",
  targets: "nav.settings",
  evals: "nav.evals",
};
const label = (tab: Tab) => t(labelKeys[tab]);
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
  useLocale();
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
      { id: number; text: string; error: boolean; action?: NoticeAction }[]
    >([]),
    [openTaskId, setOpenTaskId] = useState<number>(),
    [newTaskVersion, setNewTaskVersion] = useState(0),
    [openTaskVersion, setOpenTaskVersion] = useState(0),
    [routinesVersion, setRoutinesVersion] = useState(0),
    [sessionAction, setSessionAction] = useState<{
      kind: "new" | "discover";
      version: number;
    }>(),
    [section, setSection] = useState<{ name: string; version: number; focus?: string }>({ name: "machines", version: 0 }),
    [projectEdit, setProjectEdit] = useState<{ id: number; version: number }>(),
    [launchProfilesVersion, setLaunchProfilesVersion] = useState(0),
    [manageProfiles, setManageProfiles] = useState(false),
    [conversation, setConversation] = useState<{
      kind: "task" | "session";
      id: number;
      name: string;
      // Set only by a notification's "Reply" action — focuses the composer
      // the moment the chat opens instead of waiting for a tap.
      quickReply?: boolean;
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
    [pushPromptGone, setPushPromptGone] = useState(pushPromptDismissed),
    // A notification's "Open terminal"/"Reply" action, waiting for `sessions`
    // to actually contain that row (it may still be loading on a cold open).
    [sessionDeepLink, setSessionDeepLink] = useState<{
      id: number;
      action: "terminal" | "reply";
      version: number;
    }>();
  // The Android app pushes through UnifiedPush (native/push.ts), not the
  // browser's PushManager, so it is always able to.
  const pushAvail = useMemo(() => (inApp() ? { available: true } : pushAvailability(envFromWindow(window))), []);
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
  // A toast can carry one action (Undo after closing a session). It stays
  // long enough to be reached on a phone, and taking the action dismisses it.
  const notice = useCallback((text: string, error = false, action?: NoticeAction) => {
    const id = ++toastCounter.current;
    setToasts((old) => [...old, { id, text, error, action }]);
    window.setTimeout(
      () => setToasts((old) => old.filter((row) => row.id !== id)),
      action ? 10000 : 4200,
    );
  }, []);
  const api = useMemo(
    () =>
      createDeckApi({
        offline: offlineCache,
        onUnauthorized: () => {
          // Over the encrypted relay there is no token to type: a 401 means
          // this device was revoked, which the relay banner explains.
          if (window.__lecternRelay) return;
          // A device that was paired (frontend/src/pairing/Pair.tsx sets this
          // non-secret marker on success — the credential itself is an
          // HttpOnly cookie this code can't see) and has no static token
          // configured: a 401 here means the device was revoked, so the fix
          // is re-pairing, not typing an access token that was never issued
          // to it. See docs/remote-access.md.
          if (!authToken()) {
            let paired = false;
            try {
              paired = localStorage.getItem("lec-paired") === "1";
            } catch {}
            if (paired) {
              try {
                localStorage.removeItem("lec-paired");
              } catch {}
              window.location.href = "/pair";
              return;
            }
          }
          setUnauthorized(true);
        },
      }),
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
  // The Android back key (mobile/back.ts): overlays first, then the views
  // visited, and only then out of the app.
  useEffect(() => setViewNavigator(navigate), [navigate]);
  // Deferred, so the first view the app routes to on start is the first
  // one recorded, not the default it renders for a moment before that.
  useEffect(() => {
    const timer = window.setTimeout(() => noteView("#" + view), 0);
    return () => clearTimeout(timer);
  }, [view]);
  useBackClose(!!review, () => setReview(undefined));
  useBackClose(!!mergeReview, () => setMergeReview(undefined));
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
  // Pull to refresh and the offline banner's Retry (mobile/).
  const retryOffline = useCallback(
    () => void refresh().catch(() => undefined),
    [refresh],
  );
  const viewElement = useCallback(() => document.getElementById("view"), []);
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
      const response = await api.request<{url:string;notice?:string}>(`/sessions/${next.id}/terminal`,{method:'POST'});
      openTerminal(response.url,next.name);
      if (response.notice) notice(response.notice);
      terminals.close(`/terminal/session/${source}`);
      const remaining = {...switching.current}; delete remaining[String(source)]; saveSwitches(remaining);
      notice(t('app.switch.switched'));
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
      notice(t('app.switch.waiting'));
      void refresh().catch(error=>notice(String(error),true));
    } catch(error) { switchFailure(source, String(error)); }
  },[api,notice,refresh,saveSwitches,switchFailure]);
  const dismissSwitch = useCallback((source: number)=>{
    const remaining = {...switching.current}; delete remaining[String(source)]; saveSwitches(remaining);
  },[saveSwitches]);
  const attach = useCallback(
    async (id: number) => {
      try {
        const response = await api.request<{ url: string; notice?: string }>(
          `/sessions/${id}/terminal`,
          { method: "POST" },
        );
        openTerminal(response.url, sessions.find((row) => row.id === id)?.name);
        if (response.notice) notice(response.notice);
      } catch (error) {
        notice(viewerUnavailable(error) ?? String(error), true);
      }
    },
    [api, openTerminal, sessions, notice],
  );
  // Resolves a pending notification deep link once its session actually
  // shows up in `sessions` — on a cold open the first refresh may still be
  // in flight when the hash effect above fires. "reply" opens the same chat
  // Chat already opens, with the composer auto-focused; "terminal" reuses
  // attach's existing retry/error handling rather than a second copy.
  useEffect(() => {
    if (!sessionDeepLink) return;
    const session = sessions.find((row) => row.id === sessionDeepLink.id);
    if (!session) return;
    if (sessionDeepLink.action === "terminal") void attach(session.id);
    else setConversation({ kind: "session", id: session.id, name: session.name, quickReply: true });
    setSessionDeepLink(undefined);
  }, [sessionDeepLink, sessions, attach]);
  const newTerminal = useCallback(
    async (choice?: { machineID?: number; projectID?: number }) => {
      try {
        // A project shell is a fresh tracked session in that project's folder;
        // plain "New terminal" still opens a scratch shell on the default
        // machine. The picker never sends both.
        const body: Record<string, number> = choice?.projectID
          ? { project_id: choice.projectID }
          : choice?.machineID
            ? { target_id: choice.machineID }
            : {};
        const shell = await api.request<SessionView>("/shells", {
          method: "POST",
          body,
        });
        // A shell that really opened is what makes the project recent; a
        // failed creation must not reorder anyone's picker.
        if (choice?.projectID) rememberRecentProject(choice.projectID);
        const response = await api.request<{ url: string; notice?: string }>(
          `/sessions/${shell.id}/terminal`,
          { method: "POST" },
        );
        openTerminal(response.url, shell.name);
        if (response.notice) notice(response.notice);
      } catch (error) {
        notice(viewerUnavailable(error) ?? String(error), true);
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
  const settings = (name: string, focus?: string) => {
    setSection({ name, version: Date.now(), focus });
    navigate("#targets");
  };
  // What a workspace pane can reach (workspace/registry.tsx).
  const services = useMemo<PaneServices>(() => ({
    api,
    sessions,
    notice,
    attach: (id) => void attach(id),
    openPane: (ref, options) => {
      setConversation(undefined);
      terminals.openPane(ref, options);
    },
    closePane: (id) => terminals.close(id),
  }), [api, sessions, notice, attach, terminals.openPane, terminals.close]);
  const setTheme = (theme: "system" | "dark" | "light") => saveAppearance({ theme });
  const zoomStep = (delta: number) => {
    const zoom = currentAppearance().zoom;
    const index = ZOOM_STEPS.findIndex((step) => step >= zoom - 1e-6);
    saveAppearance({ zoom: ZOOM_STEPS[Math.max(0, Math.min(ZOOM_STEPS.length - 1, (index < 0 ? 2 : index) + delta))]! });
  };
  const goto = (tab: Tab) => () => navigate(tab === "terminals" ? terminals.hash : "#" + tab);
  useShortcuts({
    "palette.open": () => setPalette((old) => !old),
    "search.saved": () => setSearch(true),
    "settings.open": () => settings(section.name === "machines" ? "machines" : section.name),
    "settings.shortcuts": () => settings("shortcuts"),
    "settings.search": () => settings(section.name, "search"),
    "theme.toggle": () => setTheme(resolveMode(currentAppearance().theme, matchMedia("(prefers-color-scheme: dark)").matches) === "dark" ? "light" : "dark"),
    "theme.system": () => setTheme("system"),
    "theme.dark": () => setTheme("dark"),
    "theme.light": () => setTheme("light"),
    "zoom.in": () => zoomStep(1),
    "zoom.out": () => zoomStep(-1),
    "zoom.reset": () => saveAppearance({ zoom: 1 }),
    "accent.next": () => {
      const index = ACCENT_PRESETS.findIndex((preset) => preset.value === currentAppearance().accent);
      saveAppearance({ accent: ACCENT_PRESETS[(index + 1) % ACCENT_PRESETS.length]!.value });
    },
    "nav.board": goto("board"),
    "nav.sessions": goto("sessions"),
    "nav.tasks": goto("tasks"),
    "nav.terminals": goto("terminals"),
    "nav.media": goto("media"),
    "nav.deck": goto("deck"),
    "nav.approvals": goto("approvals"),
    "nav.targets": goto("targets"),
    "nav.evals": () => setShowEvals(true),
    ...Object.fromEntries(["machines", "projects", "notifications", "devices", "about", "budgets", "accounts", "agents", "plugins", "appearance", "workspace"].map((name) => [`settings.${name}`, () => settings(name)])),
    "session.new": () => sessionCommand("new"),
    "session.discover": () => sessionCommand("discover"),
    "task.new": () => newTask(),
    "routines.open": () => { navigate("#board"); setRoutinesVersion((old) => old + 1); },
    "profiles.manage": () => setManageProfiles(true),
    "terminal.new": () => void newTerminal(),
  });
  useEffect(() => {
    const apply = () => {
      let raw = "";
      try {
        raw = decodeURIComponent(location.hash.slice(1));
      } catch {
        return;
      }
      const [kind, id, terminalID] = raw.split("/");
      // #settings/<section>: a plugin's palette command, or a link, opening
      // one Settings section.
      if (kind === "settings" && id && SECTIONS.some(([name]) => name === id)) {
        settings(id);
        return;
      }
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
      // #sessions/new: `lectern up` lands here, on "Start an agent".
      if (kind === "sessions" && id === "new") {
        sessionCommand("new");
        return;
      }
      if (kind === "session") {
        setView("sessions");
        // A notification's "Open terminal"/"Reply" action (sw-actions.ts
        // actionURL) lands here as #session/<id>/terminal|reply. A plain
        // #session/<id> (NeedsYou's fallback link for a row not present in
        // this browser's own state yet) has no third segment and just picks
        // the Sessions tab, same as before.
        if (/^[1-9]\d*$/.test(id || "") && (terminalID === "terminal" || terminalID === "reply"))
          setSessionDeepLink({ id: Number(id), action: terminalID, version: Date.now() });
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
    // Offline, the banner already says so; a toast per poll would not help.
    const failed = (error: unknown) => {
      if (alive && !offlineCache.state.stale) notice(String(error), true);
    };
    void refresh().catch(failed);
    // Preferences that follow this person between devices (theme, shortcuts,
    // layouts, quick commands); the stream says when another device changed one.
    void loadPrefs().catch(() => {});
    // What enabled plugins add to the browser (themes, quick commands,
    // palette commands); the stream says when a plugin changed.
    void loadPluginContributions().catch(() => {});
    const stream = new EventSource(withToken("/api/stream"));
    stream.addEventListener("ui_prefs", () => void loadPrefs().catch(() => {}));
    stream.addEventListener("plugins", () => void loadPluginContributions().catch(() => {}));
    let opened = false;
    stream.onopen = () => {
      setConnected(true);
      if (opened) void refresh().catch(failed);
      opened = true;
    };
    stream.onerror = () => setConnected(false);
    const update = () => void refresh().catch(failed);
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
      "ci",
      "target_reach",
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
          else switchFailure(row.session_id,row.error || t('app.switch.incomplete'));
          update();
          return;
        }
        notice(
          row.ok
            ? t("app.handoff.written") +
                (row.successor ? t("app.handoff.successorStarted") : "") +
                (row.remembered ? t("app.handoff.remembered") : "")
            : t("app.handoff.failed", { error: row.error || t("app.handoff.unknown") }),
          !row.ok,
        );
      } catch {
        notice(t("app.handoff.unreadable"), true);
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
        `${element.getBoundingClientRect().bottom / uiZoom()}px`,
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
  // One listener serves every registered shortcut (shortcuts/registry.ts);
  // terminal frames post the app chords pressed inside them.
  const viewRefForKeys = useRef(view);
  viewRefForKeys.current = view;
  useEffect(() => {
    const off = installShortcutListener({
      contexts: ["global", "workspace"],
      active: (context) => context !== "workspace" || viewRefForKeys.current === "terminals",
    });
    const forwarded = installForwardedShortcuts();
    return () => {
      off();
      forwarded();
    };
  }, []);
  useEffect(() => {
    // The Android app's shell is its signed APK; it installs no worker.
    if (navigator.serviceWorker && !inApp())
      void navigator.serviceWorker
        .register("/sw.js")
        .catch((error) => notice(t("app.offlineSupportError", { error: String(error) }), true));
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
    if (inApp()) {
      // Re-send the app's endpoint whenever the distributor issues a new one.
      const sync = () =>
        void syncNativePush(api.request)
          .then((endpoint) => !cancelled && setPushEndpoint(endpoint ?? null))
          .catch(() => !cancelled && setPushEndpoint(null));
      sync();
      window.addEventListener(PUSH_EVENT, sync);
      return () => {
        cancelled = true;
        window.removeEventListener(PUSH_EVENT, sync);
      };
    }
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
  // Glanceable count for the app icon's badge (the PWA analogue of a lock-
  // screen count) — every SSE-driven refresh recomputes it from the same
  // rows already on screen, and tells the service worker the fresh total so
  // a later push (which cannot see this state) can bump from a value that
  // was actually current a moment ago rather than an unbounded guess.
  useEffect(() => {
    const waitingSessions = sessions.filter(
      (session) =>
        session.status === "waiting" &&
        session.archived_at == null &&
        session.ended_at == null,
    ).length;
    const count = computeBadgeCount({ approvals: approvals.length, waitingSessions });
    applyBadge(navigator, count);
    navigator.serviceWorker?.controller?.postMessage({ type: "lec-badge-count", count });
  }, [approvals, sessions]);
  async function enablePush() {
    if (inApp()) {
      try {
        setPushEndpoint(await enableNativePush(api.request));
        setPushPromptGone(true);
        dismissPushPrompt();
        notice(t("app.pushPrompt.enabled"));
        await api.request("/settings/test-notification", { method: "POST" });
      } catch (error) {
        notice(t("app.pushPrompt.error", { error: String(error) }), true);
      }
      return;
    }
    try {
      if (!navigator.serviceWorker || !window.Notification)
        throw new Error(t("app.pushPrompt.unsupported"));
      const registration = await navigator.serviceWorker.ready;
      if ((await Notification.requestPermission()) !== "granted")
        throw new Error(t("app.pushPrompt.notGranted"));
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
      notice(t("app.pushPrompt.enabled"));
      // So the owner sees, right away, that it actually works — rather than
      // finding out for the first time when a real alert silently fails to
      // arrive.
      try {
        await api.request("/settings/test-notification", { method: "POST" });
      } catch (error) {
        notice(t("app.pushPrompt.testError", { error: String(error) }), true);
      }
    } catch (error) {
      notice(t("app.pushPrompt.error", { error: String(error) }), true);
    }
  }
  // Removes a device's subscription server-side; when it is this browser's
  // own, also tears down the live PushManager subscription so re-enabling
  // starts clean instead of handing the server back the same dead endpoint.
  async function unsubscribePush(endpoint: string) {
    try {
      await api.request("/push/subscribe", { method: "DELETE", body: { endpoint } });
      if (pushEndpoint === endpoint && inApp()) {
        nativeBridge()?.disablePush();
        setPushEndpoint(null);
      } else if (pushEndpoint === endpoint) {
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
      notice(t("app.pushPrompt.unsubscribed"));
    } catch (error) {
      notice(t("app.pushPrompt.unsubscribeError", { error: String(error) }), true);
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
  const pluginUI = usePluginContributions();
  const commands: Command[] = [
    {
      id: "routines",
      shortcut: chordsFor("routines.open"),
      title: t("app.commands.routines"),
      category: t("palette.actions"),
      detail: t("app.commands.routinesDetail"),
      keywords: "schedule takeover",
      run: () => {
        navigate("#board");
        setRoutinesVersion((old) => old + 1);
      },
    },
    {
      id: "new-session",
      shortcut: chordsFor("session.new"),
      title: t("app.commands.newSession"),
      category: t("palette.actions"),
      detail: t("app.commands.newSessionDetail"),
      keywords: "create launch",
      run: () => sessionCommand("new"),
    },
    {
      id: "new-task",
      shortcut: chordsFor("task.new"),
      title: t("app.newTask"),
      category: t("palette.actions"),
      detail: t("app.commands.newTaskDetail"),
      keywords: "create",
      run: newTask,
    },
    {
      id: "saved-search",
      shortcut: chordsFor("search.saved"),
      title: t("app.commands.savedSearch"),
      category: t("palette.actions"),
      keywords: "history messages content native",
      run: () => setSearch(true),
    },
    {
      id: "discover",
      shortcut: chordsFor("session.discover"),
      title: t("app.commands.discover"),
      category: t("palette.actions"),
      keywords: "adopt restore untracked",
      run: () => sessionCommand("discover"),
    },
    {
      id: "launch-profiles",
      shortcut: chordsFor("profiles.manage"),
      title: t("app.commands.launchProfiles"),
      category: t("palette.actions"),
      keywords: "profiles accounts configuration",
      run: () => setManageProfiles(true),
    },
    ...tabs.map((tab) => ({
      id: `nav-${tab}`,
      title:
        tab === "board"
          ? t("app.commands.taskBoard")
          : tab === "terminals"
            ? t("app.commands.openTerminals")
            : label(tab),
      category: t("app.commands.navigate"),
      keywords: "navigate view",
      shortcut: chordsFor("nav." + tab),
      run: () => navigate(tab === "terminals" ? terminals.hash : "#" + tab),
    })),
    ...[
      ["machines", t("app.commands.targets"), "ssh remote local machines"],
      ["projects", t("app.commands.projects"), "repositories workspaces"],
      ["notifications", t("app.commands.notifications"), "alerts push"],
      ["devices", t("app.commands.devices"), "pair phone tunnel qr code pairing"],
      ["about", t("app.commands.about"), "settings version costs"],
      ["agents", t("app.commands.agents"), "agent runners commands custom providers models"],
      ["plugins", t("app.commands.plugins"), "plugins extensions marketplace install skills mcp hooks themes"],
    ].map(([name, title, keywords]) => ({
      id: "settings-" + name,
      title: title!,
      category: t("app.commands.navigate"),
      detail: t("palette.settings"),
      keywords,
      run: () => settings(name!),
    })),
    // Every registered action that can run here, with its chord, except the
    // ones the palette already lists above under their own names.
    ...SHORTCUTS.filter((row) => row.context !== "terminal" && !row.id.startsWith("nav.") && !["palette.open", "search.saved", "session.new", "session.discover", "task.new", "routines.open", "profiles.manage"].includes(row.id) && !row.id.startsWith("settings.") && hasShortcutHandler(row.id)).map((row) => ({
      id: "action-" + row.id,
      title: t("shortcut." + row.id, undefined, row.title),
      category: t("palette.actions"),
      detail: row.category,
      keywords: row.keywords,
      shortcut: chordsFor(row.id),
      run: () => {
        if (row.context === "workspace" && view !== "terminals") navigate(terminals.hash);
        requestAnimationFrame(() => runShortcut(row.id));
      },
    })),
    // What enabled plugins add to the palette: a Lectern view or an https page.
    ...pluginUI.palette_commands.flatMap((row) => {
      const href = safeHref(row.href);
      if (!href) return [];
      return [{
        id: "plugin-" + row.id,
        title: row.title,
        category: t("app.commands.plugins"),
        detail: row.plugin,
        keywords: "plugin " + row.plugin,
        run: () => {
          // Through the hash, so every deep link the app reads works here too.
          if (href.startsWith("#")) location.hash = href;
          else window.open(href, "_blank", "noopener,noreferrer");
        },
      }];
    }),
    // Individual settings, so "accent" or "push" lands on the control itself.
    ...settingsIndex().filter((entry) => ["appearance", "workspace", "shortcuts"].includes(entry.section)).map((entry) => ({
      id: "setting-" + entry.id,
      title: entry.label,
      category: t("palette.settings"),
      detail: entry.sectionLabel,
      keywords: entry.keywords,
      run: () => settings(entry.section, entry.id),
    })),
    ...sessions.map((session) => ({
      id: `session-${session.id}`,
      title: session.name || t("app.lineage.session", { id: session.id }),
      category: t("app.commands.sessions"),
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
      title: task.title || t("app.commands.task", { id: task.id }),
      category: t("app.commands.tasks"),
      detail: [task.status, task.project_name].join(" · "),
      keywords: `task ${task.id}`,
      run: () => openTask(task.id),
    })),
    ...projects.map((project) => ({
      id: `project-${project.id}`,
      title: t("app.commands.editProject", { name: project.name }),
      category: t("app.commands.projectsCategory"),
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
          <span className="brand-sub">{t("shell.brandSub")}</span>
        </div>
        <button
          id="command-open"
          aria-label={t("shell.searchLabel")}
          aria-haspopup="dialog"
          title={t("shell.searchTitle")}
          onClick={() => setPalette(true)}
        >
          <Icon name="search" size={18} />
          <span className="command-label">{t("shell.searchAnything")}</span>
          {chordsFor("palette.open")[0] && <kbd>{displayChord(chordsFor("palette.open")[0]!)}</kbd>}
        </button>
        <div className="top-status">
          <span
            id="conn-led"
            className={`led ${connected ? "led-on" : "led-err"}`}
            title={t("app.liveConnection")}
          />
          <span id="conn-label">{connected ? t("shell.live") : t("shell.reconnecting")}</span>
        </div>
      </header>
      <main id="view" hidden={view === "terminals"}>
        <OfflineBanner onRetry={retryOffline} />
        <PullToRefresh target={viewElement} onRefresh={refresh} />
        {version > 0 && projects.length === 0 && sessions.length === 0 && (
          <FirstRun
            request={api.request}
            hasProject={projects.length > 0}
            hasSession={sessions.length > 0}
            hasTarget={targets.length > 0}
            onSetupTarget={() => settings("machines")}
            onStartSession={() => sessionCommand("new")}
            onOpenConnectTools={() => settings("machines")}
          />
        )}
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
              setConversation({ kind: "session", id, name: sessions.find((session) => session.id === id)?.name || t("app.continuity.session") })
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
        {view === "tasks" && (
          <TasksHub
            api={api}
            projects={projects}
            refreshVersion={version}
            onNotice={notice}
            onOpenSession={(id, name) => setConversation({ kind: "session", id, name })}
            onOpenTask={openTask}
            onSettings={(id) => {
              settings("projects");
              setProjectEdit({ id, version: Date.now() });
            }}
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
            pushUnavailableReasonKind={pushAvail.reasonKind}
            pushEndpoint={pushEndpoint}
            onUnsubscribePush={unsubscribePush}
          />
        )}
      </main>
      <TerminalTabs
        controller={terminals}
        visible={view === "terminals"}
        machines={targets}
        projects={projects}
        onNew={newTerminal}
        onBrowse={() => navigate("#sessions")}
        onSearch={() => setPalette(true)}
        services={services}
        switcher={(()=>{
          const active = sessions.find(s=>terminals.active===`/terminal/session/${s.id}` && s.agent!=='shell' && !s.ended_at && s.status!=='dead' && s.setup_state!=='creating');
          if(!active) return null;
          const busy = active.id in pendingSwitches || active.handoff_in_flight;
          return <>
            <button className="b terminal-agent-switch" aria-label={t("app.switcher.label")} title={t("app.switcher.title", { model: sessionModelLabel(active) })} disabled={busy} onClick={()=>setSwitchSession(active)}>{busy?t("app.switcher.switching"):`⇄ ${sessionModelLabel(active)} ▾`}</button>
            <SessionLineage api={api} session={active} pending={busy} onOpen={(session)=>{ if(session.ended_at) setConversation({kind:'session',id:session.id,name:session.name}); else void attach(session.id); }} onOpenChat={(session)=>setConversation({kind:'session',id:session.id,name:session.name})}/>
          </>;
        })()}
      />
      <FloatingTerminal services={services} onDock={(tab) => terminals.open(tab.path, tab.label)} />
      {switchSession && <QuickSwitch api={api} session={switchSession} onClose={()=>setSwitchSession(undefined)} onStarted={(source,afterWrap,request:SwitchRequest)=>{
        saveSwitches({...switching.current,[String(source.id)]:{after:afterWrap,generation:1,destination:request.destination,agent:request.agent,model:request.model,profile:request.profile}});
        notice(t('app.switch.waiting'));
        void refresh().catch(error=>notice(String(error),true));
      }} onProfiles={()=>{setSwitchSession(undefined);setManageProfiles(true);}}/>}
      <SwitchProgressPanel api={api} pending={pendingSwitches} onReady={completeSwitch} onFailed={switchFailure} onRetry={(source)=>void retrySwitch(source)} onReopen={reopenSwitch} onDismiss={dismissSwitch}/>
      <button
        id="fab"
        title={t("app.fabTitle")}
        aria-label={t("app.newTask")}
        hidden={view !== "board"}
        onClick={newTask}
      >
        <Icon name="plus" size={24} />
        <span>{t("board.newTask")}</span>
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
            {label(tab)}
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
                title={liveViews.length ? t("app.liveCount", { n: liveViews.length }) : undefined}
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
          <summary aria-label={t("app.morePages")}>
            <span aria-hidden="true">···</span>{t("nav.more")}
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
                {tab === "deck" ? t("nav.deckOverview") : label(tab)}
              </button>
            ))}
          </div>
        </details>
      </nav>
      <div id="toasts" aria-live="polite">
        {toasts.map((toast) => (
          <div key={toast.id} className={`toast ${toast.error ? "err" : ""}${toast.action ? " has-action" : ""}`}>
            <span>{toast.text}</span>
            {toast.action && (
              <button
                className="toast-action"
                onClick={() => {
                  const action = toast.action!;
                  setToasts((old) => old.filter((row) => row.id !== toast.id));
                  void action.run();
                }}
              >
                {toast.action.label}
              </button>
            )}
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
                t("app.forkStarted"),
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
          aria-label={t("app.token.title")}
          onCancel={(event) => event.preventDefault()}
        >
          <h2>{t("app.token.title")}</h2>
          <p>{t("app.token.required")}</p>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void saveToken();
            }}
          >
            <label htmlFor="access-token">{t("app.token.title")}</label>
            <input
              id="access-token"
              type="password"
              autoComplete="current-password"
              value={token}
              onChange={(event) => setToken(event.target.value)}
            />
            {authError && <p role="alert">{authError}</p>}
            <button className="b ok" type="submit">
              {t("app.token.connect")}
            </button>
          </form>
        </Modal>
      )}
    </>
  );
}
