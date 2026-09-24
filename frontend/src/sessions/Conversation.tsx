import "./conversation-react.css";
import "./session-home.css";
import { SessionLineage } from "../continuity/SessionLineage";
import { Modal } from "./Modal";
import {
  useEffect,
  useLayoutEffect,
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
interface Recognition {
  lang: string;
  interimResults: boolean;
  onresult:
    | ((event: {
        results: {
          [index: number]: { [index: number]: { transcript: string } };
        };
      }) => void)
    | null;
  onend: (() => void) | null;
  onerror: (() => void) | null;
  start(): void;
  stop(): void;
}
interface Changes {
  branch: string;
  files: { path: string; status: string; working: boolean; staged: boolean }[];
  repositories?: { id: number; name: string }[];
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
}) {
  const key = `lec-draft-${kind}-${id}`;
  const [draft, setDraft] = useState(() => readDraft(key)),
    [rows, setRows] = useState<Row[]>([]),
    [sessionText, setSessionText] = useState(""),
    [sessionAgent, setSessionAgent] = useState(""),
    [status, setStatus] = useState("Connecting…"),
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
    [dictating, setDictating] = useState(false),
    [showMergeReview, setShowMergeReview] = useState(false),
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
    [currentName, setCurrentName] = useState(name);
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
    recognition = useRef<Recognition | undefined>(undefined);
  current.current = draft;
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
      recognition.current?.stop();
      document.body.style.overflow = overflow;
      visualViewport?.removeEventListener("resize", fit);
      visualViewport?.removeEventListener("scroll", fit);
      window.removeEventListener("resize", fit);
    };
  }, []);
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
          `${data.session.agent} · ${data.ended ? "Ended" : data.session.status} · live reader`,
        );
        setSessionAgent(data.session.agent);
        setUnavailable(data.ended || data.session.status === "dead");
        setSessionText(data.text || "Waiting for agent output…");
        if (takeoverTasks.current.length) {
          const pending = await api
            .request<Approval[]>("/approvals?status=pending", {
              signal: abort.current.signal,
            })
            .catch(() => []);
          if (closed.current) return;
          setApprovals(
            (Array.isArray(pending) ? pending : []).filter(
              (row) => !!row.task_id && takeoverTasks.current.includes(row.task_id),
            ),
          );
        }
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
          `${next.agent} · ${next.status}${next.attempt ? ` · turn ${next.attempt.n}` : ""}`,
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
              ? `Not delivered: ${latest.error}`
              : latest.attempt_id
                ? "Message delivered to the agent."
                : "Instructions added to the task.",
          );
        const nextRows: Row[] = [
          {
            id: "prompt",
            time: next.created_at || 0,
            role: "operator",
            text: next.prompt || next.title,
            label: "Task",
          },
          ...messages.map((message) => ({
            id: `message-${message.id}`,
            time: message.created_at,
            role: "operator" as const,
            text: message.text,
            label:
              message.status === "pending"
                ? "You · queued for next turn"
                : message.status === "failed"
                  ? `Not delivered · ${message.error}`
                  : message.attempt_id
                    ? "You · delivered to agent"
                    : "You · added to task",
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
              label: `${event.type === "text" ? "Agent" : "Result"} · turn ${event.attempt_n}`,
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
                  ? display(p.name || "Tool")
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
          `Could not refresh: ${String(error)}. Displayed output may be stale.`,
        );
      }
    } finally {
      busy.current = false;
    }
  }
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
  }, [rows, sessionText]);
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
          throw new Error("Attach up to 10 files per message.");
        if (file.size > 25 * 1024 * 1024)
          throw new Error(`${file.name} exceeds 25 MiB.`);
        setUploadStatus(`Uploading ${file.name}…`);
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
      setUploadStatus("Files ready. Add a message, then Send.");
    } catch (error) {
      if (!closed.current)
        setUploadStatus(
          `Upload failed: ${String(error)} Previously uploaded files are kept.`,
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
        "You're offline. Nothing was sent; your draft is kept on this device.",
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
        "Message including attachments exceeds 32000 bytes. Shorten the message.",
      );
      return;
    }
    sendingRef.current = true;
    setSending(true);
    setReceipt("Sending…");
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
          ? "Sent to the session."
          : result.status === "delivered"
            ? "Already delivered."
            : submitted.interrupt
              ? "Saved. Interrupting the current run before continuing."
              : "Saved. Waiting for delivery to the agent.",
      );
      void refresh();
    } catch (error) {
      if (!closed.current)
        setReceipt(
          `Could not confirm delivery: ${String(error)}. Your draft is kept.`,
        );
    } finally {
      sendingRef.current = false;
      if (!closed.current) setSending(false);
    }
  }
  async function decide(approval: Approval, decision: "approved" | "denied") {
    try {
      await api.request(`/approvals/${approval.id}/decision`, {
        method: "POST",
        body: { decision },
      });
      void refresh();
    } catch (error) {
      setError(String(error));
    }
  }
  const Speech =
    (
      window as unknown as {
        SpeechRecognition?: new () => Recognition;
        webkitSpeechRecognition?: new () => Recognition;
      }
    ).SpeechRecognition ||
    (window as unknown as { webkitSpeechRecognition?: new () => Recognition })
      .webkitSpeechRecognition;
  function dictate() {
    if (!Speech) return;
    if (dictating) {
      recognition.current?.stop();
      return;
    }
    const listener = new Speech();
    recognition.current = listener;
    listener.lang = navigator.language;
    listener.interimResults = false;
    listener.onresult = (event) => {
      const text = event.results[0]?.[0]?.transcript;
      if (text)
        change((old) => ({
          ...old,
          text: old.text + (old.text ? " " : "") + text,
          request_id: uid(),
        }));
      input.current?.focus();
    };
    listener.onend = () => setDictating(false);
    listener.onerror = () => {
      setDictating(false);
      onNotice("Dictation could not start. Check microphone permission.", true);
    };
    try {
      listener.start();
      setDictating(true);
    } catch (error) {
      onNotice(String(error), true);
    }
  }
  const hint =
    kind === "session"
      ? "Sends to the same running session. Enter adds a new line."
      : task?.takeover
        ? "This run is continuing as an interactive session. Close Chat and choose Open session."
        : task?.target_kind === "sandbox" && task.status !== "backlog"
          ? "Send queues a new sandbox run with your instructions and the previous result."
          : task?.status === "backlog"
            ? "Adds instructions to this task without dispatching it."
            : task?.status === "running"
              ? "Send queues a follow-up. Interrupt and send stops this run first, then continues."
              : task?.status === "queued"
                ? "Your message is added before the queued run starts."
                : "Send continues the task in its existing worktree.";
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
  return (
    <Modal
      id="conversation"
      className="conversation"
      aria-label={`Conversation with ${name}`}
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
                  aria-label="Rename session"
                  onClick={() => {
                    setRenamingValue(currentName);
                    setIsRenaming(true);
                  }}
                  title="Rename"
                >
                  ✎
                </button>
              </h2>
            </>
          )}
          <p id="conversation-status" role="status" data-connection={connection}>
            {connection === "offline"
              ? "Offline — showing the last output; sends are paused"
              : connection === "stale"
                ? `${status} · reconnecting…`
                : status}
          </p>
        </div>
        {onSwitch && <button className="b" onClick={onSwitch}>⇄ Switch</button>}
        <button
          className="b"
          id="conversation-close"
          aria-label="Close conversation"
          onClick={onClose}
        >
          ✕
        </button>
      </header>
      {session && onOpenSession && <SessionLineage api={api} session={session} onOpen={onOpenSession} className="conversation-lineage" />}
      <div className="reader-controls">
        <span>
          {kind === "session"
            ? "Live output · last 500 lines"
            : "Task conversation"}
        </span>
        <button
          className="b"
          id="reader-smaller"
          aria-label="Smaller text"
          onClick={() => setFont((old) => Math.max(16, old - 1))}
        >
          A−
        </button>
        <button
          className="b"
          id="reader-larger"
          aria-label="Larger text"
          onClick={() => setFont((old) => Math.min(24, old + 1))}
        >
          A+
        </button>
        <button
          className="b"
          id="reader-latest"
          onClick={() => {
            follow.current = true;
            if (log.current) log.current.scrollTop = log.current.scrollHeight;
          }}
        >
          ↓ Latest
        </button>
      </div>
      <div id="conversation-error" role="status" hidden={!error}>
        {error}
      </div>
      {kind === "session" && onAttach && !unavailable && (
        <div
          className="conversation-session-actions"
          id="conversation-session-actions"
        >
          <button
            type="button"
            className="b"
            id="conversation-terminal"
            onClick={openTerminal}
          >
            ⌨ Open terminal
          </button>
          <button
            type="button"
            className="b"
            id="conversation-merge-review"
            onClick={() => setShowMergeReview(true)}
          >
            ± Review &amp; merge
          </button>
        </div>
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
            Changed files{changedFiles.length ? ` · ${changedFiles.length}` : ""}
          </summary>
          <div className="changes-body">
            {changesBusy && <p className="sub">Reading working changes…</p>}
            {!changesBusy && changesError && (
              <p className="sub">
                Working changes are unavailable for this session.
              </p>
            )}
            {!changesBusy && changes && (
              <>
                <p className="sub">
                  {changes.branch || "workspace"} · working
                  {changes.truncated ? " · truncated" : ""}
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
                  <p className="sub">No changed files right now.</p>
                )}
              </>
            )}
            <button
              type="button"
              className="b"
              disabled={changesBusy}
              onClick={() => void loadChanges()}
            >
              Refresh
            </button>
          </div>
        </details>
      )}
      <div
        id="conversation-log"
        ref={log}
        tabIndex={0}
        aria-label="Agent output"
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
            <p>
              This tracked session is a shell, not an agent conversation. Type
              to the pane from here, or use the Terminal action above for the
              full keyboard.
            </p>
          </div>
        ) : kind === "session" && error && !sessionText ? (
          <div id="conversation-output-error">
            <p>
              Live output could not be read from this session
              {unavailable ? " because it has ended" : ""}.
            </p>
            <p>
              {unavailable
                ? "Restore tracking to continue the conversation."
                : "Messages still go to the session; the Terminal action above reads the pane directly."}
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
                <div className="reader-speaker">You · not sent yet</div>
                <div className="reader-text">{draft.text}</div>
              </article>
            )}
            <pre className="session-reader">{sessionText || "Loading…"}</pre>
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
          <div key={approval.id} className="reader-approval">
            <strong>Approval needed: {approval.tool_name}</strong>
            <pre>{JSON.stringify(approval.input)}</pre>
            <button
              className="b"
              onClick={() => void decide(approval, "approved")}
            >
              Approve
            </button>
            <button
              className="b"
              onClick={() => void decide(approval, "denied")}
            >
              Deny
            </button>
            {!!approval.task_id && (
              <a
                className="b link-button"
                href={`#task/${approval.task_id}`}
                onClick={onClose}
              >
                Open task
              </a>
            )}
          </div>
        ))}
      </div>
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
          Message {kind === "session" ? "this agent" : "this task"}
        </label>
        <textarea
          id="conversation-input"
          ref={input}
          rows={3}
          maxLength={32000}
          placeholder="Write a message or use your keyboard’s microphone…"
          value={draft.text}
          onChange={(event) => {
            const text = event.target.value;
            change((old) => ({ ...old, text, request_id: uid() }));
          }}
          onKeyDown={(event) => {
            if (
              event.key === "Enter" &&
              (event.ctrlKey || event.metaKey) &&
              !event.nativeEvent.isComposing
            ) {
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
        <div id="conversation-attachments" aria-label="Attached files">
          {draft.attachments.map((file) => (
            <div key={file.path} className="context-attachment">
              <span title={file.path}>
                {file.name} · {Math.max(1, Math.ceil(file.size / 1024))} KB
              </span>
              <button
                type="button"
                className="b"
                aria-label={`Remove ${file.name}`}
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
            Draft saved on this device · not sent yet
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
                ? "Attachments unavailable for this session or sandbox"
                : "PDFs, images, documents and other files · 25 MiB each"
            }
            disabled={sending || uploading || unavailable}
            onClick={() => files.current?.click()}
          >
            📎 Attach
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
              Interrupt and send
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
                      "Interrupt sent. Check the agent output before sending new instructions.",
                    );
                    void refresh();
                  })
                  .catch((error) => setError(String(error)));
              }}
            >
              Interrupt
            </button>
          )}
          <button
            type="button"
            className="b"
            id="conversation-mic"
            aria-label="Dictate message"
            hidden={!Speech}
            aria-pressed={dictating}
            onClick={dictate}
          >
            🎙
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
            Send
          </button>
        </div>
        {connection === "offline" && (
          <p id="conversation-offline" className="conversation-notice" role="status">
            Offline — nothing is sent while you are disconnected. Your draft is
            saved on this device and stays here when you reconnect.
          </p>
        )}
        <p id="conversation-receipt" role="status">
          {receipt}
        </p>
      </form>
    </Modal>
  );
}
