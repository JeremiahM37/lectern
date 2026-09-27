import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Modal } from "../sessions/Modal";
import { DiffViewer, fileDomId, type LineNote } from "./DiffViewer";
import { CommentTray } from "./CommentTray";
import { FileTree } from "./FileTree";
import { FocusReview, jumpHunk } from "./FocusReview";
import { GitPanel } from "./GitPanel";
import { ConflictResolver } from "./ConflictResolver";
import { ReviewRounds } from "./ReviewRounds";
import { DiffModeToggle } from "./DiffModeToggle";
import {
  anchorFor,
  commentState,
  fingerprint,
  parseFile,
  placeComment,
  type DiffMode,
  type FileAttribution,
  type ParsedFile,
} from "./diffModel";
import { PREF_KEYS, useStoredFlag, useStoredPref } from "./prefs";
import type { DraftComment, DiffResponse, RepoDiff, StoredComment, ViewedMark } from "./types";
import type { JsonValue } from "../api";
import type { SessionCheck } from "../types";

// A subset of the app's `api.request`, the same seam TaskDetail uses — so
// this panel can be opened both from the top-level app (the full DeckApi)
// and from inside Conversation.tsx, which only carries this much.
export interface SessionReviewApi {
  request<T>(path: string, options?: { method?: string; body?: JsonValue }): Promise<T>;
}

type Tab = "changes" | "commit" | "conflicts" | "checks";

interface ReviewState {
  comments: StoredComment[];
  viewed: ViewedMark[];
}

interface Attribution {
  repos: { name?: string; files: Record<string, FileAttribution> }[];
  hook_evidence: boolean;
}

const repoKey = (r: RepoDiff) => r.name ?? "";

/** The review workspace for a session: the live diff (every repository in a
 * grouped workspace) with a file tree, unified or side-by-side layout,
 * authorship gutter and inline comments sent to the agent as one prompt per
 * round; the local git flow; three-way conflict resolution; and the session's
 * check history. Reachable from the session card and the Conversation view.
 * See docs/review.md. */
export function SessionReview({
  api,
  sessionId,
  name,
  onClose,
  onNotice,
}: {
  api: SessionReviewApi;
  sessionId: number;
  name?: string;
  onClose(): void;
  onNotice(text: string, error?: boolean): void;
}) {
  const [tab, setTab] = useState<Tab>("changes");
  const [diff, setDiff] = useState<DiffResponse>();
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [reload, setReload] = useState(0);
  const [state, setState] = useState<ReviewState>({ comments: [], viewed: [] });
  const [attribution, setAttribution] = useState<Attribution>();
  const [conflicts, setConflicts] = useState(0);

  const [mode, setMode] = useStoredPref<DiffMode>(PREF_KEYS.mode, "unified", ["unified", "split"]);
  const [wrap, setWrap] = useStoredFlag(PREF_KEYS.wrap, false);
  // The tree beside the diff by default on a wide screen; on a phone it
  // would push the diff below the fold, so it starts hidden there.
  const [showTree, setShowTree] = useStoredFlag(
    PREF_KEYS.tree,
    typeof window === "undefined" || !window.matchMedia?.("(max-width: 760px)").matches,
  );
  const [showAuthors, setShowAuthors] = useStoredFlag(PREF_KEYS.authorship, true);

  const [repo, setRepo] = useState("");
  const [selected, setSelected] = useState<string>();
  const [focus, setFocus] = useState<number>();
  const [summary, setSummary] = useState("");
  const [sending, setSending] = useState(false);
  const pane = useRef<HTMLDivElement>(null);

  // The dialog is what scrolls; the sheet header (and, where it is pinned,
  // the toolbar) sit over its top edge.
  function hunk(dir: 1 | -1) {
    const p = pane.current;
    const dialog = p?.closest("dialog");
    if (!p || !dialog) return;
    const dialogTop = dialog.getBoundingClientRect().top;
    let inset = 0;
    for (const el of dialog.querySelectorAll<HTMLElement>(".sheet-head, .review-toolbar-sticky")) {
      if (getComputedStyle(el).position === "sticky")
        inset = Math.max(inset, el.getBoundingClientRect().bottom - dialogTop);
    }
    jumpHunk(p, dir, dialog, inset);
  }

  const loadState = useCallback(() => {
    api
      .request<ReviewState>(`/sessions/${sessionId}/review/state`)
      .then(setState)
      .catch(() => {});
  }, [sessionId]);

  useEffect(() => {
    let live = true;
    // A refresh after a git action keeps what is on screen (and the git
    // panel's own state, such as the last commit's result) mounted.
    if (!diff) setLoading(true);
    setLoadError("");
    api
      .request<DiffResponse>(`/sessions/${sessionId}/diff`)
      .then((parsed) => {
        if (!live) return;
        setDiff(parsed);
        if (parsed.repos.length === 1) setRepo(repoKey(parsed.repos[0]!));
      })
      .catch((e) => live && setLoadError(String(e instanceof Error ? e.message : e)))
      .finally(() => live && setLoading(false));
    loadState();
    api
      .request<Attribution>(`/sessions/${sessionId}/attribution`)
      .then((a) => live && setAttribution(a))
      .catch(() => live && setAttribution(undefined));
    api
      .request<{ files: unknown[] }>(`/sessions/${sessionId}/git/conflicts${repo ? `?repo=${encodeURIComponent(repo)}` : ""}`)
      .then((c) => live && setConflicts(c.files.length))
      .catch(() => live && setConflicts(0));
    return () => {
      live = false;
    };
  }, [sessionId, reload]);

  const repos = diff?.repos ?? [];
  const multi = repos.length > 1;
  const shown = multi ? repos.filter((r) => !repo || repoKey(r) === repo) : repos;
  const gitRepo = multi ? repo : repos[0] ? repoKey(repos[0]) : "";

  // Parsed files by repo+path, for comment placement.
  const parsed = useMemo(() => {
    const m = new Map<string, ParsedFile>();
    for (const r of repos) for (const f of r.files) m.set(`${repoKey(r)}\0${f.path}`, parseFile(f.patch));
    return m;
  }, [diff]);

  // Every stored comment, placed on today's diff.
  const placed = useMemo(
    () =>
      state.comments.map((c) => {
        const placement = placeComment(parsed.get(`${c.repo}\0${c.file}`), c);
        return { c, placement, state: commentState(c.status, placement) };
      }),
    [state.comments, parsed],
  );
  const notesFor = (r: RepoDiff): LineNote[] =>
    placed
      .filter((p) => p.c.repo === repoKey(r) && p.placement && p.state !== "resolved")
      .map((p) => ({
        key: String(p.c.id),
        file: p.c.file,
        line: p.placement!.line,
        side: p.c.side,
        text: p.c.text,
        code: p.c.code,
        state: p.state,
        round: p.c.round || undefined,
      }));
  const drafts = placed.filter((p) => p.c.status === "draft");
  const viewedFor = (r: RepoDiff) =>
    Object.fromEntries(state.viewed.filter((v) => v.repo === repoKey(r)).map((v) => [v.path, v.fingerprint]));
  const attrFor = (r: RepoDiff) =>
    showAuthors ? attribution?.repos.find((a) => (a.name ?? "") === repoKey(r))?.files ?? {} : undefined;

  async function addComment(r: RepoDiff, c: Omit<DraftComment, "key">) {
    try {
      await api.request(`/sessions/${sessionId}/review/comments`, {
        method: "POST",
        body: { repo: repoKey(r), ...c } as unknown as JsonValue,
      });
      loadState();
    } catch (e) {
      onNotice(String(e instanceof Error ? e.message : e), true);
    }
  }

  async function updateComment(id: number, body: Record<string, JsonValue>, method = "PATCH") {
    try {
      await api.request(`/sessions/${sessionId}/review/comments/${id}`, { method, body });
      loadState();
    } catch (e) {
      onNotice(String(e instanceof Error ? e.message : e), true);
    }
  }

  async function toggleViewed(r: RepoDiff, path: string, fp: string, viewed: boolean) {
    setState((s) => ({
      ...s,
      viewed: viewed
        ? [...s.viewed.filter((v) => !(v.repo === repoKey(r) && v.path === path)), { repo: repoKey(r), path, fingerprint: fp }]
        : s.viewed.filter((v) => !(v.repo === repoKey(r) && v.path === path)),
    }));
    try {
      await api.request(`/sessions/${sessionId}/review/viewed`, {
        method: "PUT",
        body: { repo: repoKey(r), path, fingerprint: viewed ? fp : "" },
      });
    } catch (e) {
      onNotice(String(e instanceof Error ? e.message : e), true);
      loadState();
    }
  }

  // A comment's stored line and context, moved to where it sits in today's
  // diff — so a comment sent (or sent again) after the agent's edits names
  // the line the agent will actually find.
  function reanchor(p: (typeof placed)[number]): Record<string, JsonValue> {
    if (!p.placement || p.placement.line === p.c.line) return {};
    const file = parsed.get(`${p.c.repo}\0${p.c.file}`);
    const a = file && anchorFor(file, p.c.side, p.placement.line);
    return a ? { line: a.line, code: a.code, context_before: a.context_before, context_after: a.context_after } : {};
  }

  function reopen(id: number) {
    const p = placed.find((x) => x.c.id === id);
    void updateComment(id, { status: "draft", ...(p ? reanchor(p) : {}) });
  }

  async function sendReview() {
    setSending(true);
    try {
      await Promise.all(
        drafts
          .filter((d) => Object.keys(reanchor(d)).length > 0)
          .map((d) =>
            api.request(`/sessions/${sessionId}/review/comments/${d.c.id}`, { method: "PATCH", body: reanchor(d) }),
          ),
      );
      const result = await api.request<{ sent: boolean; comments: number; round: number }>(
        `/sessions/${sessionId}/review`,
        { method: "POST", body: { comment_ids: drafts.map((d) => d.c.id), summary } },
      );
      onNotice(
        `Sent ${String(result.comments)} comment(s) to the session as one message (round ${result.round}).`,
      );
      setSummary("");
      loadState();
    } catch (e) {
      onNotice(String(e instanceof Error ? e.message : e), true);
    } finally {
      setSending(false);
    }
  }

  const noteActions = (n: LineNote) => {
    const id = Number(n.key);
    if (n.state === "draft")
      return (
        <button type="button" className="b no dl-note-btn" onClick={() => void updateComment(id, {}, "DELETE")}>
          Delete
        </button>
      );
    return (
      <>
        <button type="button" className="b ok dl-note-btn" onClick={() => void updateComment(id, { status: "resolved" })}>
          Resolve
        </button>
        <button type="button" className="b dl-note-btn" onClick={() => reopen(id)}>
          Reopen
        </button>
      </>
    );
  };

  // Flat list of every shown file, for the tree and file-by-file review.
  const flat = shown.flatMap((r) => r.files.map((f) => ({ r, f })));
  const focusFiles = flat.map(({ r, f }) => ({
    key: `${repoKey(r)}\0${f.path}`,
    label: multi ? `${repoKey(r)} / ${f.path}` : f.path,
    viewed: viewedFor(r)[f.path] === fingerprint(f.patch),
  }));

  function viewer(r: RepoDiff, files = r.files, idPrefix = "") {
    return (
      <DiffViewer
        key={`${repoKey(r)}:${idPrefix}`}
        repoLabel={multi && !idPrefix ? repoKey(r) : undefined}
        files={files}
        stats={r.stats}
        wrap={wrap}
        mode={mode}
        truncated={r.truncated}
        commentable
        comments={notesFor(r)}
        onAddComment={(c) => void addComment(r, c)}
        attribution={attrFor(r)}
        viewed={viewedFor(r)}
        onToggleViewed={(path, fp, v) => void toggleViewed(r, path, fp, v)}
        imageSource={imageSource(r)}
        noteActions={noteActions}
        idPrefix={idPrefix}
      />
    );
  }

  const imageCache = useRef(new Map<string, Promise<string | undefined>>());
  const imageSource = (r: RepoDiff) => (path: string, side: "old" | "new") => {
    const key = `${repoKey(r)}\0${path}\0${side}\0${reload}`;
    let hit = imageCache.current.get(key);
    if (!hit) {
      const qs = new URLSearchParams({ path, side, ref: r.base_ref || "HEAD" });
      if (multi) qs.set("repo", repoKey(r));
      hit = api
        .request<{ data?: string; mime: string; missing?: boolean; too_large?: boolean }>(
          `/sessions/${sessionId}/git/blob?${qs.toString()}`,
        )
        .then((b) => (b.data ? `data:${b.mime};base64,${b.data}` : undefined));
      imageCache.current.set(key, hit);
    }
    return hit;
  };

  function selectFile(path: string) {
    setSelected(path);
    const r = shown.find((x) => x.files.some((f) => f.path === path));
    const el = r && document.getElementById(fileDomId("", path));
    if (el instanceof HTMLDetailsElement) {
      el.open = true;
      el.scrollIntoView({ block: "start" });
    }
  }

  const totalFiles = repos.reduce((n, r) => n + r.files.length, 0);
  const openCount = placed.filter((p) => p.state === "open").length;
  const addressedCount = placed.filter((p) => p.state === "addressed").length;

  const tabs: { id: Tab; label: string }[] = [
    { id: "changes", label: `Changes (${totalFiles})` },
    { id: "commit", label: "Commit" },
    ...(conflicts > 0 ? [{ id: "conflicts" as Tab, label: `Conflicts (${conflicts})` }] : []),
    { id: "checks", label: "Checks" },
  ];

  return (
    <Modal className="sheet task-detail review-panel review-workspace" id="session-review" aria-label="Review and merge">
      <div className="sheet-head">
        <h2>Review &amp; merge{name ? ` — ${name}` : ""}</h2>
        <button className="b" onClick={onClose}>
          Close
        </button>
      </div>
      <div className="review-tabs" role="tablist" aria-label="Review">
        {tabs.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={tab === t.id}
            className={"review-tab" + (t.id === "conflicts" ? " warn" : "")}
            onClick={() => setTab(t.id)}
          >
            {t.label}
          </button>
        ))}
        {multi && (
          <select aria-label="Repository" value={repo} onChange={(e) => setRepo(e.target.value)}>
            {tab === "changes" && <option value="">All repositories</option>}
            {tab !== "changes" && !repo && <option value="">choose one…</option>}
            {repos.map((r) => (
              <option key={repoKey(r)} value={repoKey(r)}>
                {repoKey(r)}
              </option>
            ))}
          </select>
        )}
      </div>
      {loading && <p className="sub">Loading the live diff…</p>}
      {!loading && loadError && <p className="sub error">{loadError}</p>}

      {!loading && !loadError && diff && tab === "changes" && (
        <>
          <header className="diffhead review-toolbar-sticky">
            <span>{totalFiles} file(s) changed</span>
            <DiffModeToggle mode={mode} onChange={setMode} />
            <button className={"wrapbtn" + (wrap ? " on" : "")} aria-pressed={wrap} onClick={() => setWrap(!wrap)}>
              ⏎ wrap: {wrap ? "on" : "off"}
            </button>
            <button className={"wrapbtn" + (showTree ? " on" : "")} aria-pressed={showTree} onClick={() => setShowTree(!showTree)}>
              🗂 files
            </button>
            <button
              className={"wrapbtn" + (showAuthors ? " on" : "")}
              aria-pressed={showAuthors}
              title="Mark lines written by the agent (◆) and by you (●)"
              onClick={() => setShowAuthors(!showAuthors)}
            >
              ◆ authors
            </button>
            <span className="hunk-nav">
              <button type="button" className="wrapbtn" aria-label="Previous hunk" onClick={() => hunk(-1)}>
                ▲
              </button>
              <button type="button" className="wrapbtn" aria-label="Next hunk" onClick={() => hunk(1)}>
                ▼
              </button>
            </span>
            <button
              type="button"
              className="b ok focus-start"
              disabled={!flat.length}
              onClick={() => {
                const first = focusFiles.findIndex((f) => !f.viewed);
                setFocus(first >= 0 ? first : 0);
              }}
            >
              Review file by file
            </button>
          </header>
          {showAuthors && attribution && (
            <p className="sub authors-legend">
              <span className="a-agent">◆ agent</span> <span className="a-human">● you</span>
              {!attribution.hook_evidence && " · uncommitted lines are only attributed once the agent reports an edit"}
            </p>
          )}
          {(openCount > 0 || addressedCount > 0) && (
            <p className="sub review-round-summary" role="status">
              Sent comments: {addressedCount} changed by the agent, {openCount} not changed yet.
            </p>
          )}
          <div className={"review-body" + (showTree ? " with-tree" : "")}>
            {showTree && (
              <FileTree
                files={flat.map(({ r, f }) => {
                  const s = r.stats.find((x) => x.path === f.path);
                  return {
                    path: multi ? `${repoKey(r)}/${f.path}` : f.path,
                    additions: s?.additions,
                    deletions: s?.deletions,
                    viewed: viewedFor(r)[f.path] === fingerprint(f.patch),
                    comments: placed.filter((p) => p.c.repo === repoKey(r) && p.c.file === f.path && p.state !== "resolved").length,
                  };
                })}
                selected={selected}
                onSelect={(p) => {
                  const hit = flat.find(({ r, f }) => (multi ? `${repoKey(r)}/${f.path}` : f.path) === p);
                  if (hit) selectFile(hit.f.path);
                }}
              />
            )}
            <div className="review-pane" ref={pane}>
              {shown.map((r) => viewer(r))}
            </div>
          </div>

          <ReviewRounds
            items={placed.filter((p) => p.c.status !== "draft")}
            onResolve={(id) => void updateComment(id, { status: "resolved" })}
            onReopen={reopen}
          />

          <CommentTray
            comments={drafts.map((d) => ({
              key: String(d.c.id),
              file: d.c.file,
              line: d.placement?.line ?? d.c.line,
              side: d.c.side,
              text: d.c.round ? `${d.c.text} (raised again)` : d.c.text,
              code: d.c.code,
            }))}
            summary={summary}
            onSummaryChange={setSummary}
            onRemove={(key) => void updateComment(Number(key), {}, "DELETE")}
            onSend={() => void sendReview()}
            busy={sending}
            sendLabel={drafts.length > 1 ? `Send ${drafts.length} comments to agent` : "Send to agent"}
          />
        </>
      )}

      {!loading && tab === "commit" && (
        multi && !repo ? (
          <p className="sub">Choose the repository to commit in.</p>
        ) : (
          <GitPanel
            api={api}
            sessionId={sessionId}
            repo={gitRepo}
            defaultMessage={name}
            onChanged={() => setReload((n) => n + 1)}
            onNotice={onNotice}
            onShowConflicts={() => setTab("conflicts")}
          />
        )
      )}

      {!loading && tab === "conflicts" && (
        multi && !repo ? (
          <p className="sub">Choose the repository with the conflict.</p>
        ) : (
          <ConflictResolver
            api={api}
            sessionId={sessionId}
            repo={gitRepo}
            onResolved={() => setReload((n) => n + 1)}
            onNotice={onNotice}
          />
        )
      )}

      {tab === "checks" && <ChecksSection api={api} sessionId={sessionId} onNotice={onNotice} />}

      {focus !== undefined && flat[focus] && (
        <FocusReview
          files={focusFiles}
          index={focus}
          onIndex={setFocus}
          onClose={() => setFocus(undefined)}
          onToggleViewed={(i, v) => {
            const { r, f } = flat[i]!;
            void toggleViewed(r, f.path, fingerprint(f.patch), v);
          }}
          renderFile={(i) => {
            const { r, f } = flat[i]!;
            return viewer(r, [f], "focus-");
          }}
          footer={
            drafts.length > 0 ? (
              <button type="button" className="b ok" disabled={sending} onClick={() => void sendReview()}>
                Send {drafts.length} 💬
              </button>
            ) : null
          }
        />
      )}
    </Modal>
  );
}

function ChecksSection({
  api,
  sessionId,
  onNotice,
}: {
  api: SessionReviewApi;
  sessionId: number;
  onNotice(text: string, error?: boolean): void;
}) {
  const [checks, setChecks] = useState<SessionCheck[]>();
  const [running, setRunning] = useState(false);
  const poll = useRef<number | undefined>(undefined);
  function load() {
    api
      .request<SessionCheck[]>(`/sessions/${sessionId}/checks?limit=10`)
      .then(setChecks)
      .catch(() => {});
  }
  useEffect(() => {
    load();
    return () => window.clearInterval(poll.current);
  }, [sessionId]);
  async function runNow() {
    setRunning(true);
    try {
      await api.request(`/sessions/${sessionId}/checks`, { method: "POST" });
      // the run happens on the target and can take a while; poll a few times
      // rather than opening a second SSE connection just for this one panel
      window.clearInterval(poll.current);
      let tries = 0;
      poll.current = window.setInterval(() => {
        load();
        if (++tries >= 15) window.clearInterval(poll.current);
      }, 2000);
    } catch (e) {
      onNotice(String(e), true);
    } finally {
      setRunning(false);
    }
  }
  return (
    <section className="review-check-history">
      <h3>
        Checks
        <button type="button" className="b" disabled={running} onClick={() => void runNow()}>
          {running ? "Starting…" : "Run check"}
        </button>
      </h3>
      {checks && checks.length === 0 && <p className="sub">No checks have run yet.</p>}
      {checks && checks.length > 0 && (
        <ul className="review-check-list">
          {checks.map((c) => (
            <li key={c.id}>
              <span className={`chip check-chip check-${c.status}`}>{c.status}</span>
              <code>{c.command}</code>
              <span className="sub">
                {c.finished_at ? new Date(c.finished_at * 1000).toLocaleString() : "running…"}
              </span>
              {c.output_tail && <pre className="review-check-output">{c.output_tail}</pre>}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
