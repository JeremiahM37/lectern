import "./conversation-react.css";
import "./session-home.css";
import { splitRecall } from "./recall";
import { SessionLineage } from "../continuity/SessionLineage";
import { CheckBadge } from "./CheckBadge";
import { Modal } from "./Modal";
import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import type {
  Approval,
  Event as AgentEvent,
  SessionView,
  TaskView,
} from "../types";
import type { SessionsApi } from "./Sessions";
import { SessionReview } from "../review/SessionReview";
import { CompactionWarning, ContextBadge, CostBadge, LinesBadge } from "./UsageBadges";
import { AwarenessOverlapChip } from "./AwarenessOverlapChip";
import { MemoryDeliveries } from "./MemoryDeliveries";
import { SessionClaims } from "../claims/SessionClaims";
import { useDictation } from "../voice";
import { ApprovalCard, decisionBody, type ApprovalDecisionOptions } from "./ApprovalCard";
import { readerText } from "./reader-text";
import { sessionState, stateText } from "./status";
import { ENTER_PREF, enterSends, shouldSend, touchOnly, type EnterMode } from "./enter";
import { usePref } from "../prefs/store";
import { FileLinksContext, Markdown, type FileLinks } from "./markdown";
import { FileApi } from "../files/api";
import { existenceCheck } from "../files/linkCheck";
import { openExternal } from "../mobile/open";
import { filesContext, openFilePane } from "../workspace/files-provider";
import { paneType } from "../workspace/registry";
import { buildChatCards, type ConversationItem } from "./tool-views/chatCards";
import { ToolCardView } from "./tool-views/ToolCard";
import { VoiceMode } from "./VoiceMode";
import { BrowserPane } from "../browser/BrowserPane";
import { t, useLocale } from "../i18n";
import { usePhone } from "../mobile/usePhone";
interface Attachment {
  name: string;
  path: string;
  size: number;
}
interface Draft {
  text: string;
  interrupt: boolean;
  attachments: Attachment[];
  request_id: string;
}
interface Message {
  id: number;
  text: string;
  status: string;
  error?: string;
  attempt_id?: number;
  created_at: number;
}
interface Row {
  id: string;
  time: number;
  role: "operator" | "agent" | "detail";
  text: string;
  label: string;
}
interface Changes {
  branch: string;
  files: { path: string; status: string; working: boolean; staged: boolean }[];
  repositories?: { id: number; name: string }[];
  truncated: boolean;
}
/** GET /sessions/{id}/conversation/live's wire shape. */
interface LivePage {
  conversation_id: string;
  items: ConversationItem[];
  cursor: number;
  truncated: boolean;
}
const uid = () =>
  globalThis.crypto?.randomUUID?.() ||
  `${Date.now()}-${Math.random().toString(36).slice(2)}`;
const emptyDraft = (): Draft => ({
  text: "",
  interrupt: false,
  attachments: [],
  request_id: uid(),
});
function readDraft(key: string): Draft {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(key) || "null");
    if (!value || typeof value !== "object") return emptyDraft();
    const row = value as Partial<Draft>;
    return {
      text: typeof row.text === "string" ? row.text : "",
      interrupt: row.interrupt === true,
      request_id: typeof row.request_id === "string" ? row.request_id : uid(),
      attachments: Array.isArray(row.attachments)
        ? row.attachments.filter(
            (file): file is Attachment =>
              !!file &&
              typeof file.name === "string" &&
              typeof file.path === "string" &&
              typeof file.size === "number",
          )
        : [],
    };
  } catch {
    return emptyDraft();
  }
}
function display(value: unknown) {
  return typeof value === "string" ? value : JSON.stringify(value ?? {});
}
export function Conversation({
  kind,
  id,
  name,
  api,
  onClose,
  onNotice,
  onAttach,
  onSwitch,
  session,
  onOpenSession,
  quickReply,
}: {
  kind: "session" | "task";
  id: number;
  name: string;
  api: SessionsApi;
  onClose(): void;
  onNotice(text: string, error?: boolean): void;
  session?: SessionView;
  onOpenSession?(session: SessionView): void;
  onAttach?(): void;
  onSwitch?(): void;
  /** Set when this chat was opened from a notification's Reply action — focuses the composer immediately instead of waiting for a tap. */
  quickReply?: boolean;
}) {
  useLocale();
  const phone = usePhone();
  const [enterMode] = usePref<EnterMode>(ENTER_PREF, "auto");
  const touch = touchOnly();
  const key = `lec-draft-${kind}-${id}`;
  const [draft, setDraft] = useState(() => readDraft(key)),
    [rows, setRows] = useState<Row[]>([]),
    [sessionText, setSessionText] = useState(""),
    [sessionAgent, setSessionAgent] = useState(""),
    [status, setStatus] = useState(() => t("conversation.chat.connecting")),
    [error, setError] = useState(""),
    [reachable, setReachable] = useState<boolean>(),
    [online, setOnline] = useState(() => navigator.onLine),
    [changes, setChanges] = useState<Changes>(),
    [changesBusy, setChangesBusy] = useState(false),
    [changesError, setChangesError] = useState(false),
    [receipt, setReceipt] = useState(""),
    [uploadStatus, setUploadStatus] = useState(""),
    [unavailable, setUnavailable] = useState(false),
    [task, setTask] = useState<TaskView>(),
    [approvals, setApprovals] = useState<Approval[]>([]),
    [sending, setSending] = useState(false),
    [uploading, setUploading] = useState(false),
    [drag, setDrag] = useState(false),
    [showMergeReview, setShowMergeReview] = useState(false),
    [showBrowser, setShowBrowser] = useState(false),
    // The header's ⋯: usage, checks, agent switching, text size and the
    // memory record, out of the way until someone asks for them.
    [showDetails, setShowDetails] = useState(false),
    [showMemory, setShowMemory] = useState(false),
    [font, setFont] = useState(() =>
      Math.max(
        16,
        Math.min(24, Number(localStorage.getItem("lec-reader-font")) || 17),
      ),
    ),
    [viewport, setViewport] = useState({
      height: visualViewport?.height || innerHeight,
      top: visualViewport?.offsetTop || 0,
    }),
    [isRenaming, setIsRenaming] = useState(false),
    [renamingValue, setRenamingValue] = useState(name),
    [currentName, setCurrentName] = useState(name),
    // Structured chat (docs/mobile-sessions.md "Chat cards"): the default
    // view for a session whose native conversation log can be located.
    // "terminal" is the raw pane view, one tap away and the automatic
    // fallback once the structured read 409s (an agent without a JSONL/
    // rollout reader, or a shell tracked session).
    [liveItems, setLiveItems] = useState<ConversationItem[]>([]),
    [liveUnavailable, setLiveUnavailable] = useState(false),
    [viewMode, setViewMode] = useState<"cards" | "terminal">("cards");
  const current = useRef(draft),
    closed = useRef(false),
    busy = useRef(false),
    sendingRef = useRef(false),
    uploadingRef = useRef(false),
    follow = useRef(true),
    log = useRef<HTMLDivElement>(null),
    input = useRef<HTMLTextAreaElement>(null),
    files = useRef<HTMLInputElement>(null),
    abort = useRef(new AbortController()),
    takeoverTasks = useRef<number[]>([]),
    loadingChanges = useRef(false),
    liveConversationId = useRef<string | undefined>(undefined),
    liveCursor = useRef<string | undefined>(undefined),
    liveItemsAccum = useRef<ConversationItem[]>([]);
  current.current = draft;
  // A notification's Reply action already brought the person to this exact
  // chat; put the caret in the composer too, rather than making that a
  // second tap. One-shot: a re-render (a new message arriving, say) must not
  // steal focus back from something the person is doing elsewhere.
  useEffect(() => {
    if (quickReply) requestAnimationFrame(() => input.current?.focus());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  // A successful read is live; a failed read is offline only when the browser
  // agrees, and stale otherwise. Both are said out loud rather than hidden
  // behind a spinner.
  const connection =
    reachable === true
      ? "live"
      : reachable === false
        ? online
          ? "stale"
          : "offline"
        : "connecting";
  const changedFiles = Array.isArray(changes?.files) ? changes.files : [];
  function change(update: (old: Draft) => Draft) {
    const next = update(current.current);
    current.current = next;
    setDraft(next);
    try {
      localStorage.setItem(key, JSON.stringify(next));
    } catch {}
  }
  useEffect(() => {
    localStorage.setItem("lec-reader-font", String(font));
  }, [font]);
  useEffect(() => {
    // Reaching the server is the evidence; the browser's own flag only
    // invalidates. A phone can be wrong, and a hermetic namespace reports
    // offline with a perfectly reachable server, so a successful read always
    // wins and an offline event stops us claiming live before the next read.
    const up = () => {
      setOnline(true);
      void refresh();
    };
    const down = () => {
      setOnline(false);
      setReachable(false);
    };
    window.addEventListener("online", up);
    window.addEventListener("offline", down);
    return () => {
      window.removeEventListener("online", up);
      window.removeEventListener("offline", down);
    };
  }, []);
  // Approvals belong to task attempts. A session that took a task over is that
  // task's interactive half, so its pending approvals belong in this chat.
  useEffect(() => {
    if (kind !== "session") return;
    const controller = new AbortController();
    void api
      .request<TaskView[]>("/tasks", { signal: controller.signal })
      .then((rows) => {
        if (controller.signal.aborted || !Array.isArray(rows)) return;
        takeoverTasks.current = rows
          .filter((row) => row.takeover?.session_id === id)
          .map((row) => row.id);
      })
      .catch(() => {});
    return () => controller.abort();
  }, [api, kind, id]);
  useEffect(() => {
    const fit = () =>
      setViewport({
        height: visualViewport?.height || innerHeight,
        top: visualViewport?.offsetTop || 0,
      });
    visualViewport?.addEventListener("resize", fit);
    visualViewport?.addEventListener("scroll", fit);
    window.addEventListener("resize", fit);
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      closed.current = true;
      abort.current.abort();
      stopDictation();
      document.body.style.overflow = overflow;
      visualViewport?.removeEventListener("resize", fit);
      visualViewport?.removeEventListener("scroll", fit);
      window.removeEventListener("resize", fit);
    };
  }, []);
  // Polls GET /sessions/{id}/conversation/live for the structured turn
  // stream. Re-resolves the active conversation on every call (the cost
  // /reader already pays for its own tmux read) and only carries the byte
  // cursor over when the resolved conversation still matches what the last
  // call returned — dropping it otherwise so a resumed/forked session
  // starts its cards fresh instead of reading the wrong file's offsets.
  // Failure (409: unsupported agent, sandboxed session, nothing
  // identifiable) marks the stream unavailable, which switches the render
  // below to the terminal-text fallback without the person doing anything.
  async function refreshLive() {
    if (closed.current) return;
    try {
      const params = new URLSearchParams();
      if (liveCursor.current) params.set("since", liveCursor.current);
      if (liveConversationId.current) params.set("last_cid", liveConversationId.current);
      const qs = params.toString();
      const data = await api.request<LivePage>(
        `/sessions/${id}/conversation/live${qs ? `?${qs}` : ""}`,
        { signal: abort.current.signal },
      );
      if (closed.current) return;
      // A server (or, in the sessions-harness fixture, a stub api.request)
      // that doesn't recognize this path still resolves with `{}` rather
      // than rejecting — treat that the same as a real 409, not as an empty
      // but valid conversation, or buildChatCards would be fed a hole.
      if (typeof data.conversation_id !== "string" || !Array.isArray(data.items))
        throw new Error("malformed conversation/live response");
      if (data.conversation_id !== liveConversationId.current) liveItemsAccum.current = [];
      liveConversationId.current = data.conversation_id;
      liveCursor.current = String(data.cursor);
      liveItemsAccum.current = liveItemsAccum.current.concat(data.items);
      setLiveItems(liveItemsAccum.current);
      setLiveUnavailable(false);
    } catch {
      if (!closed.current) setLiveUnavailable(true);
    }
  }
  async function refresh() {
    if (closed.current || busy.current || document.hidden) return;
    busy.current = true;
    try {
      if (kind === "session") {
        const data = await api.request<{
          session: SessionView;
          text: string;
          ended: boolean;
        }>(`/sessions/${id}/reader`, { signal: abort.current.signal });
        if (closed.current) return;
        setStatus(
          t("conversation.chat.sessionStatus", {
            agent: data.session.agent,
            // The same words as the card (sessions/status.ts), not the raw
            // screen status.
            state: data.ended ? t("status.ended") : stateText(sessionState(data.session)),
          }),
        );
        setSessionAgent(data.session.agent);
        setUnavailable(data.ended || data.session.status === "dead");
        setSessionText(readerText(data.text || "") || t("conversation.chat.waitingForOutput"));
        void refreshLive();
        // A session's own PermissionRequest approvals (session_id set,
        // no task) belong here too, not only the ones inherited from a
        // task this session took over — this chat is where a person
        // actually is when the agent asks.
        const pending = await api
          .request<Approval[]>("/approvals?status=pending", {
            signal: abort.current.signal,
          })
          .catch(() => []);
        if (closed.current) return;
        setApprovals(
          (Array.isArray(pending) ? pending : []).filter(
            (row) =>
              row.session_id === id ||
              (!!row.task_id && takeoverTasks.current.includes(row.task_id)),
          ),
        );
      } else {
        const [next, events, messages, pending] = await Promise.all([
          api.request<TaskView>(`/tasks/${id}`, {
            signal: abort.current.signal,
          }),
          api.request<AgentEvent[]>(`/tasks/${id}/events`, {
            signal: abort.current.signal,
          }),
          api.request<Message[]>(`/tasks/${id}/messages`, {
            signal: abort.current.signal,
          }),
          api.request<Approval[]>("/approvals?status=pending", {
            signal: abort.current.signal,
          }),
        ]);
        if (closed.current) return;
        setTask(next);
        setUnavailable(next.target_kind === "sandbox" || !!next.takeover);
        setStatus(
          next.attempt
            ? t("conversation.chat.taskStatusTurn", { agent: next.agent, status: next.status, n: next.attempt.n })
            : t("conversation.chat.taskStatus", { agent: next.agent, status: next.status }),
        );
        if (
          (next.status !== "running" || next.target_kind === "sandbox") &&
          current.current.interrupt
        )
          change((old) => ({ ...old, interrupt: false, request_id: uid() }));
        const latest = messages.at(-1);
        if (
          latest &&
          !sendingRef.current &&
          !current.current.text &&
          latest.status !== "pending"
        )
          setReceipt(
            latest.status === "failed"
              ? t("conversation.chat.receiptNotDelivered", { error: String(latest.error) })
              : latest.attempt_id
                ? t("conversation.chat.receiptDelivered")
                : t("conversation.chat.receiptAdded"),
          );
        const nextRows: Row[] = [
          {
            id: "prompt",
            time: next.created_at || 0,
            role: "operator",
            text: next.prompt || next.title,
            label: t("conversation.chat.rowTask"),
          },
          ...messages.map((message) => ({
            id: `message-${message.id}`,
            time: message.created_at,
            role: "operator" as const,
            text: message.text,
            label:
              message.status === "pending"
                ? t("conversation.chat.rowQueued")
                : message.status === "failed"
                  ? t("conversation.chat.rowNotDelivered", { error: String(message.error) })
                  : message.attempt_id
                    ? t("conversation.chat.rowDelivered")
                    : t("conversation.chat.rowAdded"),
          })),
        ];
        for (const event of events) {
          const p = event.payload;
          if (event.type === "text" || event.type === "result")
            nextRows.push({
              id: `event-${event.id}`,
              time: event.ts,
              role: "agent",
              text: display(
                event.type === "text" ? p.text : p.result || p.text || p,
              ),
              label: t(event.type === "text" ? "conversation.chat.rowAgent" : "conversation.chat.rowResult", { n: event.attempt_n }),
            });
          else if (["tool_use", "tool_result", "verify"].includes(event.type))
            nextRows.push({
              id: `event-${event.id}`,
              time: event.ts,
              role: "detail",
              text:
                typeof p.content === "string"
                  ? p.content
                  : JSON.stringify(p, null, 2),
              label:
                event.type === "tool_use"
                  ? display(p.name || t("conversation.chat.rowTool"))
                  : event.type === "tool_result"
                    ? t("conversation.chat.rowToolResult")
                    : event.type === "verify"
                      ? t("conversation.chat.rowVerify")
                      : event.type.replace("_", " "),
            });
        }
        nextRows.sort((a, b) => a.time - b.time);
        setRows((old) =>
          JSON.stringify(old) === JSON.stringify(nextRows) ? old : nextRows,
        );
        setApprovals(pending.filter((row) => row.task_id === id));
      }
      setReachable(true);
      setError("");
    } catch (error) {
      if (!closed.current) {
        setReachable(false);
        setError(
          t("conversation.chat.refreshFailed", { error: String(error) }),
        );
      }
    } finally {
      busy.current = false;
    }
  }
  // The name can change underneath an open chat (a session named from its
  // first prompt); follow it unless a rename is being typed here.
  useEffect(() => {
    setCurrentName(name);
  }, [name]);
  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 2000);
    const visible = () => {
      if (!document.hidden) void refresh();
    };
    document.addEventListener("visibilitychange", visible);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [kind, id]);
  useLayoutEffect(() => {
    if (follow.current && log.current)
      log.current.scrollTop = log.current.scrollHeight;
    // liveItems too: the structured chat cards are what most sessions show,
    // and a chat should open at its latest message, not its first.
    // approvals too: a card appearing below the log shrinks it, which must
    // not leave the newest line under the fold.
  }, [rows, sessionText, liveItems, approvals.length]);
  // Anything that resizes the log (the keyboard, an approval card, the
  // composer growing) or changes what is in it (the reader switching between
  // cards and screen text) keeps a following view on its newest line.
  useEffect(() => {
    const node = log.current;
    if (!node) return;
    const stick = () => {
      if (follow.current) node.scrollTop = node.scrollHeight;
    };
    const resized = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(stick);
    resized?.observe(node);
    const changed = new MutationObserver(stick);
    changed.observe(node, { childList: true, subtree: true, characterData: true });
    return () => {
      resized?.disconnect();
      changed.disconnect();
    };
  }, []);
  async function upload(selected: File[]) {
    if (
      uploadingRef.current ||
      sendingRef.current ||
      unavailable ||
      !selected.length
    )
      return;
    uploadingRef.current = true;
    setUploading(true);
    try {
      for (const file of selected) {
        if (closed.current) return;
        if (current.current.attachments.length >= 10)
          throw new Error(t("conversation.chat.tooManyFiles"));
        if (file.size > 25 * 1024 * 1024)
          throw new Error(t("conversation.chat.fileTooLarge", { name: file.name }));
        setUploadStatus(t("conversation.chat.uploading", { name: file.name }));
        const body = new FormData();
        body.append("file", file);
        const attachment = await api.request<Attachment>(
          `/${kind === "session" ? "sessions" : "tasks"}/${id}/attachments`,
          { method: "POST", body, signal: abort.current.signal },
        );
        if (closed.current) return;
        change((old) => ({
          ...old,
          attachments: [...old.attachments, attachment],
          request_id: uid(),
        }));
      }
      setUploadStatus(t("conversation.chat.filesReady"));
    } catch (error) {
      if (!closed.current)
        setUploadStatus(
          t("conversation.chat.uploadFailed", { error: String(error) }),
        );
    } finally {
      uploadingRef.current = false;
      if (!closed.current) {
        setUploading(false);
        if (files.current) files.current.value = "";
      }
    }
  }
  // The same endpoint the Review dialog reads. It runs Git on the target, so it
  // happens when the file list is opened or refreshed — not on every poll.
  async function loadChanges() {
    if (kind !== "session" || loadingChanges.current) return;
    loadingChanges.current = true;
    setChangesBusy(true);
    try {
      const next = await api.request<Changes>(
        `/term/session/${id}/changes?scope=working`,
        { signal: abort.current.signal },
      );
      if (closed.current) return;
      setChanges(next);
      setChangesError(false);
    } catch {
      if (!closed.current) {
        setChanges(undefined);
        setChangesError(true);
      }
    } finally {
      loadingChanges.current = false;
      if (!closed.current) setChangesBusy(false);
    }
  }
  async function send() {
    if (
      sendingRef.current ||
      uploadingRef.current ||
      (!current.current.text.trim() && !current.current.attachments.length) ||
      (kind === "session" && unavailable)
    )
      return;
    if (connection === "offline") {
      setReceipt(
        t("conversation.chat.offlineReceipt"),
      );
      return;
    }
    const submitted = current.current;
    const text =
      submitted.text +
      (submitted.attachments.length
        ? "\n\nUse these attached files as context (paths on this agent’s machine):\n" +
          JSON.stringify(
            submitted.attachments.map((file) => ({
              name: file.name,
              path: file.path,
            })),
            null,
            2,
          )
        : "");
    if (new TextEncoder().encode(text).length > 32000) {
      setError(
        t("conversation.chat.tooLong"),
      );
      return;
    }
    sendingRef.current = true;
    setSending(true);
    setReceipt(t("conversation.chat.sending"));
    try {
      const result = await api.request<{ status?: string }>(
        kind === "session" ? `/sessions/${id}/send` : `/tasks/${id}/messages`,
        {
          method: "POST",
          body:
            kind === "session"
              ? { text }
              : {
                  text,
                  interrupt: submitted.interrupt,
                  request_id: submitted.request_id,
                },
          signal: abort.current.signal,
        },
      );
      if (closed.current) return;
      change((old) => ({
        ...old,
        text: old.text === submitted.text ? "" : old.text,
        attachments: old.attachments.filter(
          (file) =>
            !submitted.attachments.some((sent) => sent.path === file.path),
        ),
        request_id: uid(),
      }));
      setUploadStatus("");
      setReceipt(
        kind === "session"
          ? t("conversation.chat.sentToSession")
          : result.status === "delivered"
            ? t("conversation.chat.alreadyDelivered")
            : submitted.interrupt
              ? t("conversation.chat.savedInterrupting")
              : t("conversation.chat.savedWaiting"),
      );
      void refresh();
    } catch (error) {
      if (!closed.current)
        setReceipt(
          t("conversation.chat.deliveryUnconfirmed", { error: String(error) }),
        );
    } finally {
      sendingRef.current = false;
      if (!closed.current) setSending(false);
    }
  }
  async function decide(
    approval: Approval,
    decision: "approved" | "denied",
    opts?: ApprovalDecisionOptions,
  ) {
    try {
      await api.request(`/approvals/${approval.id}/decision`, {
        method: "POST",
        body: decisionBody(decision, opts),
      });
      void refresh();
    } catch (error) {
      setError(String(error));
    }
  }
  // Interim words appear in the box as Speech hears them (never duplicated
  // once finalized) and nothing is ever sent on their own — the person still
  // reviews and taps Send, exactly as if they had typed it.
  const {
    supported: dictationSupported,
    dictating,
    transcribing,
    engine: dictationEngine,
    toggle: toggleDictation,
    stop: stopDictation,
  } = useDictation({
    onChange: (text) => change((old) => ({ ...old, text, request_id: uid() })),
    onNotice,
  });
  function dictate() {
    toggleDictation(draft.text);
    // On a phone, focusing the box raises the keyboard over half the
    // screen while the person is talking; they tap the box to edit.
    if (!window.matchMedia?.("(pointer: coarse)").matches) input.current?.focus();
  }
  const hint =
    kind === "session"
      ? enterSends(enterMode, touch)
        ? t("conversation.chat.hintSessionEnter")
        : t("conversation.chat.hintSession")
      : task?.takeover
        ? t("conversation.chat.hintTakeover")
        : task?.target_kind === "sandbox" && task.status !== "backlog"
          ? t("conversation.chat.hintSandbox")
          : task?.status === "backlog"
            ? t("conversation.chat.hintBacklog")
            : task?.status === "running"
              ? t("conversation.chat.hintRunning")
              : task?.status === "queued"
                ? t("conversation.chat.hintQueued")
                : t("conversation.chat.hintDefault");
  function openTerminal() {
    // The card's attach flow already retries the transient 503, reports the
    // permanent ones and opens the tab. Reuse it instead of a second copy.
    onClose();
    onAttach?.();
  }
  async function handleRename() {
    const trimmed = renamingValue.trim();
    if (!trimmed) {
      setIsRenaming(false);
      return;
    }
    try {
      await api.request(`/sessions/${id}`, { method: "PATCH", body: { name: trimmed } });
      setCurrentName(trimmed);
      setIsRenaming(false);
    } catch (err) {
      onNotice(String(err), true);
    }
  }
  function cancelRename() {
    setRenamingValue(currentName);
    setIsRenaming(false);
  }
  // Paths an agent writes open as they do in the terminal (markdown.tsx).
  const workdir = kind === "session" ? session?.workdir || "" : "";
  const fileLinks = useMemo<FileLinks | null>(() => {
    if (!workdir.startsWith("/")) return null;
    const files = new FileApi(`/api/term/session/${id}`);
    const exists = existenceCheck(files);
    return {
      workdir,
      exists,
      open: (link) => {
        if (link.kind === "url") return openExternal(link.url);
        void (async () => {
          try {
            if (link.external) {
              const stat = await files.outsideStat(link.path);
              if (!stat.exists) return onNotice(t("files.link.missing", { path: link.path }), true);
              if (!stat.regular) return onNotice(t("files.link.notFile", { path: stat.path }), true);
            } else if (!(await exists(link.path))) return onNotice(t("files.link.missing", { path: link.path }), true);
          } catch (error) {
            return onNotice(t("files.link.failed", { path: link.path, message: error instanceof Error ? error.message : String(error) }), true);
          }
          const context = filesContext();
          if (context && paneType("file")) openFilePane({ ...context, session: id }, link.path, link.line, link.column);
          else window.open(`/terminal/session/${id}?open=${encodeURIComponent(link.path)}${link.line ? "#L" + link.line : ""}`, "_blank", "noopener");
        })();
      },
    };
  }, [workdir, id, onNotice]);
  const chatCards = kind === "session" ? buildChatCards(liveItems) : [];
  const sessionActions = kind === "session" && onAttach && !unavailable && (
    <div className="conversation-session-actions" id="conversation-session-actions">
      <button
        type="button"
        className="b"
        id="conversation-terminal"
        aria-label={phone ? t("conversation.chat.openTerminal") : undefined}
        onClick={openTerminal}
      >
        {t(phone ? "conversation.chat.openTerminalShort" : "conversation.chat.openTerminal")}
      </button>
      <button
        type="button"
        className="b"
        id="conversation-merge-review"
        aria-label={phone ? t("conversation.chat.reviewMerge") : undefined}
        onClick={() => setShowMergeReview(true)}
      >
        {t(phone ? "conversation.chat.reviewMergeShort" : "conversation.chat.reviewMerge")}
      </button>
    </div>
  );
  return (
    <FileLinksContext.Provider value={fileLinks}>
    <Modal
      id="conversation"
      className={showBrowser ? "conversation with-browser" : "conversation"}
      aria-label={t("conversation.chat.dialogLabel", { name })}
      style={
        {
          height: viewport.height,
          top: viewport.top,
          "--reader-font": `${font}px`,
        } as CSSProperties
      }
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
    >
      <header className="conversation-head">
        <div>
          {isRenaming ? (
            <div className="conversation-title-edit">
              <input
                type="text"
                value={renamingValue}
                onChange={(e) => setRenamingValue(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    e.preventDefault();
                    e.stopPropagation();
                    void handleRename();
                  } else if (e.key === "Escape") {
                    e.preventDefault();
                    e.stopPropagation();
                    cancelRename();
                  }
                }}
                onBlur={handleRename}
                autoFocus
              />
            </div>
          ) : (
            <>
              <h2 id="conversation-title">
                {currentName}
                <button
                  className="b rename-btn"
                  aria-label={t("conversation.chat.renameSession")}
                  onClick={() => {
                    setRenamingValue(currentName);
                    setIsRenaming(true);
                  }}
                  title={t("conversation.chat.rename")}
                >
                  ✎
                </button>
              </h2>
            </>
          )}
          <p id="conversation-status" role="status" data-connection={connection}>
            {connection === "offline"
              ? t("conversation.chat.offlineStatus")
              : connection === "stale"
                ? t("conversation.chat.reconnectingStatus", { status })
                : status}
          </p>
          {session && <CompactionWarning session={session} />}
        </div>
        <button
          type="button"
          className="b conversation-details-toggle"
          id="conversation-details-toggle"
          aria-expanded={showDetails}
          aria-controls="conversation-details"
          aria-label={t("conversation.chat.detailsLabel")}
          title={t("conversation.chat.detailsLabel")}
          onClick={() => setShowDetails((old) => !old)}
        >
          ⋯
        </button>
        <button
          className="b"
          id="conversation-close"
          aria-label={t("conversation.chat.close")} data-close
          onClick={onClose}
        >
          ✕
        </button>
      </header>
      {showDetails && (
        <div className="conversation-details" id="conversation-details">
          {session && (
            <div className="conversation-usage">
              {session.model && <span className="chip">{session.model}</span>}
              <ContextBadge session={session} />
              <CostBadge session={session} />
              <LinesBadge session={session} />
              <AwarenessOverlapChip session={session} />
              <SessionClaims session={session} request={api.request} onNotice={onNotice} />
            </div>
          )}
          {kind === "session" && session && (
            <div className="conversation-checks">
              <CheckBadge session={session} api={api} onNotice={onNotice} />
            </div>
          )}
          <div className="conversation-details-actions">
            {onSwitch && (
              <button type="button" className="b" id="conversation-switch" onClick={onSwitch}>
                {t("conversation.chat.switch")}
              </button>
            )}
            {kind === "session" && onAttach && !unavailable && (
              <button
                type="button"
                className="b"
                id="conversation-browser"
                aria-pressed={showBrowser}
                onClick={() => setShowBrowser(!showBrowser)}
              >
                {t("conversation.chat.browser")}
              </button>
            )}
            {kind === "session" && (
              <button
                type="button"
                className="b"
                id="conversation-memory-toggle"
                aria-pressed={showMemory}
                onClick={() => setShowMemory((old) => !old)}
              >
                {t("sessions.memoryDeliveries.summary")}
              </button>
            )}
            <span className="conversation-text-size" role="group" aria-label={t("conversation.chat.textSize")}>
              <button
                className="b"
                id="reader-smaller"
                aria-label={t("conversation.chat.smallerText")}
                onClick={() => setFont((old) => Math.max(16, old - 1))}
              >
                A−
              </button>
              <button
                className="b"
                id="reader-larger"
                aria-label={t("conversation.chat.largerText")}
                onClick={() => setFont((old) => Math.min(24, old + 1))}
              >
                A+
              </button>
            </span>
          </div>
        </div>
      )}
      {session && onOpenSession && <SessionLineage api={api} session={session} onOpen={onOpenSession} className="conversation-lineage" />}
      {/* A phone has one row for these: the session's actions and the view
          controls, with short labels; the full names stay as accessible names. */}
      <div className="reader-controls">
        {!phone && (
          <span>
            {kind === "session"
              ? viewMode === "cards" && !liveUnavailable
                ? t("conversation.chat.viewChat")
                : t("conversation.chat.viewLiveOutput")
              : t("conversation.chat.viewTask")}
          </span>
        )}
        {phone && sessionActions}
        {kind === "session" && !liveUnavailable && (
          <button
            type="button"
            className="b"
            id="conversation-view-toggle"
            aria-label={phone ? (viewMode === "cards" ? t("conversation.chat.showTerminalText") : t("conversation.chat.showChatCards")) : undefined}
            onClick={() => setViewMode((old) => (old === "cards" ? "terminal" : "cards"))}
          >
            {viewMode === "cards"
              ? t(phone ? "conversation.chat.showTerminalTextShort" : "conversation.chat.showTerminalText")
              : t(phone ? "conversation.chat.showChatCardsShort" : "conversation.chat.showChatCards")}
          </button>
        )}
        <button
          className="b"
          id="reader-latest"
          aria-label={phone ? t("conversation.chat.latest") : undefined}
          onClick={() => {
            follow.current = true;
            if (log.current) log.current.scrollTop = log.current.scrollHeight;
          }}
        >
          {phone ? "↓" : t("conversation.chat.latest")}
        </button>
      </div>
      <div id="conversation-error" role="status" hidden={!error}>
        {error}
      </div>
      {!phone && sessionActions}
      {kind === "session" && showBrowser && (
        <BrowserPane
          api={api}
          sessionId={id}
          name={currentName}
          onClose={() => setShowBrowser(false)}
          onNotice={onNotice}
        />
      )}
      {showMergeReview && (
        <SessionReview
          api={api}
          sessionId={id}
          name={name}
          onClose={() => setShowMergeReview(false)}
          onNotice={onNotice}
        />
      )}
      {kind === "session" && (
        <details
          id="conversation-changes"
          className="conversation-changes"
          onToggle={(event) => {
            if (event.currentTarget.open) void loadChanges();
          }}
        >
          <summary>
            {changedFiles.length
              ? t("conversation.chat.changedFilesCount", { count: changedFiles.length })
              : t("conversation.chat.changedFiles")}
          </summary>
          <div className="changes-body">
            {changesBusy && <p className="sub">{t("conversation.chat.readingChanges")}</p>}
            {!changesBusy && changesError && (
              <p className="sub">
                {t("conversation.chat.changesUnavailable")}
              </p>
            )}
            {!changesBusy && changes && (
              <>
                <p className="sub">
                  {t("conversation.chat.branchWorking", { branch: changes.branch || t("conversation.chat.workspace") })}
                  {changes.truncated ? t("conversation.chat.truncated") : ""}
                </p>
                {changedFiles.length ? (
                  <ul className="changes-files">
                    {changedFiles.map((file) => (
                      <li key={file.path} data-status={file.status}>
                        <code>{file.path}</code>
                        <span>{file.status}</span>
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className="sub">{t("conversation.chat.noChanges")}</p>
                )}
              </>
            )}
            <button
              type="button"
              className="b"
              disabled={changesBusy}
              onClick={() => void loadChanges()}
            >
              {t("conversation.chat.refresh")}
            </button>
          </div>
        </details>
      )}
      {kind === "session" && showMemory && (
        <MemoryDeliveries api={api} kind="session" id={id} onNotice={onNotice} />
      )}
      <div
        id="conversation-log"
        ref={log}
        tabIndex={0}
        aria-label={t("conversation.chat.agentOutput")}
        onScroll={() => {
          if (log.current)
            follow.current =
              log.current.scrollHeight -
                log.current.clientHeight -
                log.current.scrollTop <
              70;
        }}
      >
        {kind === "session" && sessionAgent === "shell" ? (
          <div id="conversation-output-error">
            <p>{t("conversation.chat.shellSession")}</p>
          </div>
        ) : kind === "session" && error && !sessionText ? (
          <div id="conversation-output-error">
            <p>
              {unavailable
                ? t("conversation.chat.outputUnreadableEnded")
                : t("conversation.chat.outputUnreadable")}
            </p>
            <p>
              {unavailable
                ? t("conversation.chat.restoreTracking")
                : t("conversation.chat.messagesStillGo")}
            </p>
          </div>
        ) : kind === "session" ? (
          <>
            {!!draft.text.trim() && (
              <article
                className="reader-message operator queued"
                id="conversation-draft-row"
                data-status="draft"
              >
                <div className="reader-speaker">{t("conversation.chat.draftRow")}</div>
                <div className="reader-text">{draft.text}</div>
              </article>
            )}
            {viewMode === "cards" && !liveUnavailable ? (
              <div id="conversation-cards">
                {chatCards.length === 0 && (
                  <p className="sub" id="conversation-cards-empty">
                    {t("conversation.chat.waitingToStart")}
                  </p>
                )}
                {chatCards.map((card) =>
                  card.kind === "tool" ? (
                    <ToolCardView key={card.id} card={card} />
                  ) : card.kind === "thinking" ? (
                    <details key={card.id} className="reader-message thinking" data-event={card.id}>
                      <summary>{t("conversation.chat.thinking")}</summary>
                      <div className="reader-text">{card.text}</div>
                    </details>
                  ) : (
                    <article key={card.id} className={`reader-message ${card.role}`}>
                      <div className="reader-speaker">{card.role === "user" ? t("conversation.chat.you") : t("conversation.chat.agent")}</div>
                      <MessageText role={card.role} text={card.text} />
                    </article>
                  ),
                )}
              </div>
            ) : (
              <pre className="session-reader">{sessionText || t("conversation.chat.loading")}</pre>
            )}
          </>
        ) : (
          rows.map((row) =>
            row.role === "detail" ? (
              <details
                key={row.id}
                className="reader-message detail"
                data-event={row.id}
              >
                <summary>{row.label}</summary>
                <pre>{row.text}</pre>
              </details>
            ) : (
              <article key={row.id} className={`reader-message ${row.role}`}>
                <div className="reader-speaker">{row.label}</div>
                <div className="reader-text">{row.text}</div>
              </article>
            ),
          )
        )}
      </div>
      <div id="conversation-approvals">
        {approvals.map((approval) => (
          <ApprovalCard
            key={approval.id}
            approval={approval}
            onDecide={(decision, opts) => decide(approval, decision, opts)}
            onOpenTask={(taskId) => {
              onClose();
              location.hash = `#task/${taskId}`;
            }}
          />
        ))}
      </div>
      {kind === "session" && !unavailable && (
        <VoiceMode
          api={api}
          sessionId={id}
          sessionText={sessionText}
          approvals={approvals}
          onNotice={onNotice}
        />
      )}
      <form
        id="conversation-compose"
        className={drag ? "file-drag" : ""}
        onSubmit={(event) => {
          event.preventDefault();
          void send();
        }}
        onDragOver={(event) => {
          if ([...event.dataTransfer.types].includes("Files")) {
            event.preventDefault();
            setDrag(true);
          }
        }}
        onDragLeave={() => setDrag(false)}
        onDrop={(event) => {
          if (event.dataTransfer.files.length) {
            event.preventDefault();
            setDrag(false);
            void upload([...event.dataTransfer.files]);
          }
        }}
      >
        <label htmlFor="conversation-input">
          {kind === "session" ? t("conversation.chat.messageAgent") : t("conversation.chat.messageTask")}
        </label>
        <textarea
          id="conversation-input"
          ref={input}
          rows={3}
          maxLength={32000}
          placeholder={t(phone ? "conversation.chat.placeholderShort" : "conversation.chat.placeholder")}
          value={draft.text}
          onChange={(event) => {
            const text = event.target.value;
            change((old) => ({ ...old, text, request_id: uid() }));
          }}
          onKeyDown={(event) => {
            if (shouldSend({ ...event, key: event.key, isComposing: event.nativeEvent.isComposing }, enterMode, touch)) {
              event.preventDefault();
              void send();
            }
          }}
          onPaste={(event) => {
            if (event.clipboardData.files.length) {
              event.preventDefault();
              void upload([...event.clipboardData.files]);
            }
          }}
        />
        <input
          ref={files}
          type="file"
          id="conversation-files"
          multiple
          hidden
          disabled={sending || uploading || unavailable}
          onChange={(event) => void upload([...(event.target.files || [])])}
        />
        <div id="conversation-attachments" aria-label={t("conversation.chat.attachedFiles")}>
          {draft.attachments.map((file) => (
            <div key={file.path} className="context-attachment">
              <span title={file.path}>
                {t("conversation.chat.attachmentSize", { name: file.name, size: Math.max(1, Math.ceil(file.size / 1024)) })}
              </span>
              <button
                type="button"
                className="b"
                aria-label={t("conversation.chat.removeFile", { name: file.name })}
                disabled={sending}
                onClick={() =>
                  change((old) => ({
                    ...old,
                    attachments: old.attachments.filter(
                      (row) => row.path !== file.path,
                    ),
                    request_id: uid(),
                  }))
                }
              >
                ×
              </button>
            </div>
          ))}
        </div>
        <p id="conversation-upload-status" role="status">
          {uploadStatus}
        </p>
        {!!draft.text.trim() && !sending && (
          <p id="conversation-draft" role="status">
            {t("conversation.chat.draftSaved")}
          </p>
        )}
        <p id="conversation-hint">{hint}</p>
        <div className="compose-actions">
          <button
            type="button"
            className="b"
            id="conversation-attach"
            title={
              unavailable
                ? t("conversation.chat.attachUnavailable")
                : t("conversation.chat.attachHint")
            }
            disabled={sending || uploading || unavailable}
            onClick={() => files.current?.click()}
          >
            {t("conversation.chat.attach")}
          </button>
          {kind === "task" ? (
            <label className="interrupt-option">
              <input
                id="conversation-interrupt"
                type="checkbox"
                checked={draft.interrupt}
                disabled={
                  task?.status !== "running" || task.target_kind === "sandbox"
                }
                onChange={(event) => {
                  const interrupt = event.target.checked;
                  change((old) => ({ ...old, interrupt, request_id: uid() }));
                }}
              />{" "}
              {t("conversation.chat.interruptAndSend")}
            </label>
          ) : (
            <button
              type="button"
              className="b warn"
              id="conversation-interrupt-session"
              disabled={unavailable}
              onClick={() => {
                void api
                  .request(`/sessions/${id}/send`, {
                    method: "POST",
                    body: { key: "escape" },
                  })
                  .then(() => {
                    setReceipt(
                      t("conversation.chat.interruptSent"),
                    );
                    void refresh();
                  })
                  .catch((error) => setError(String(error)));
              }}
            >
              {t("conversation.chat.interrupt")}
            </button>
          )}
          <button
            type="button"
            className={dictating ? "b mic-recording" : "b"}
            id="conversation-mic"
            aria-label={transcribing ? t("conversation.chat.transcribingLabel") : dictating ? t("conversation.chat.stopDictating") : t("conversation.chat.dictate")}
            title={dictationEngine === "host" ? t("conversation.chat.onLectern") : undefined}
            hidden={!dictationSupported}
            aria-pressed={dictating}
            aria-busy={transcribing}
            onClick={dictate}
          >
            {transcribing ? t("conversation.chat.transcribing") : dictating ? (dictationEngine === "host" ? t("conversation.chat.recording") : t("conversation.chat.listening")) : "🎙"}
          </button>
          <button
            type="submit"
            className="b ok grow"
            id="conversation-send"
            data-sending={sending ? "true" : undefined}
            disabled={
              sending ||
              uploading ||
              connection === "offline" ||
              (kind === "session" && unavailable)
            }
          >
            {t("conversation.chat.send")}
          </button>
        </div>
        {connection === "offline" && (
          <p id="conversation-offline" className="conversation-notice" role="status">
            {t("conversation.chat.offlineNotice")}
          </p>
        )}
        <p id="conversation-receipt" role="status">
          {receipt}
        </p>
      </form>
    </Modal>
    </FileLinksContext.Provider>
  );
}


function MessageText({ role, text }: { role: string; text: string }) {
  const { recall, rest } = role === "user" ? splitRecall(text) : { recall: [], rest: text };
  return (
    <div className="reader-text">
      {recall.length > 0 && (
        <details className="recall-context">
          <summary>
            {t("conversation.chat.recall", { count: recall.length })}
          </summary>
          <pre>{recall.join("\n")}</pre>
        </details>
      )}
      {rest && <Markdown text={rest} />}
    </div>
  );
}
