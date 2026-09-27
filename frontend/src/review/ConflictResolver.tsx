import { useEffect, useMemo, useState } from "react";
import { conflictCount, hasMarkers, parseConflicts, resolveConflicts, type Choice } from "./conflicts";
import { ConfirmButton, type ReviewApi } from "./GitPanel";

interface ConflictList {
  operation: string;
  branch: string;
  files: { path: string; base: boolean; ours: boolean; theirs: boolean }[];
  file?: {
    path: string;
    binary: boolean;
    base: boolean;
    ours: boolean;
    theirs: boolean;
    base_text?: string;
    ours_text?: string;
    theirs_text?: string;
    working_text?: string;
    merged_text?: string;
  };
}

const CHOICES: { choice: Choice; label: string }[] = [
  { choice: "ours", label: "Accept ours" },
  { choice: "theirs", label: "Accept theirs" },
  { choice: "both", label: "Accept both" },
];

function choiceName(c: Choice | undefined): string {
  if (c === undefined) return "unresolved";
  if (typeof c === "object") return "edited";
  return { ours: "ours", theirs: "theirs", both: "both", "both-theirs-first": "both", base: "base" }[c];
}

/**
 * Three-way merge-conflict resolution for one repository. Each conflict shows
 * ours, the common ancestor and theirs side by side with Accept ours /
 * theirs / both; the result below is the whole file, rebuilt from those
 * choices and freely editable. Saving writes the file on the target and
 * stages it; leftover conflict markers are refused.
 */
export function ConflictResolver({
  api,
  sessionId,
  repo,
  onResolved,
  onNotice,
}: {
  api: ReviewApi;
  sessionId: number;
  repo: string;
  onResolved(): void;
  onNotice(text: string, error?: boolean): void;
}) {
  const base = `/sessions/${sessionId}/git`;
  const [list, setList] = useState<ConflictList>();
  const [path, setPath] = useState("");
  const [detail, setDetail] = useState<ConflictList["file"]>();
  const [choices, setChoices] = useState<(Choice | undefined)[]>([]);
  const [result, setResult] = useState("");
  const [edited, setEdited] = useState(false);
  const [busy, setBusy] = useState(false);
  const [reload, setReload] = useState(0);
  const repoQ = repo ? `repo=${encodeURIComponent(repo)}` : "";

  useEffect(() => {
    api
      .request<ConflictList>(`${base}/conflicts?${repoQ}`)
      .then((l) => {
        setList(l);
        if (!l.files.some((f) => f.path === path)) setPath(l.files[0]?.path ?? "");
      })
      .catch((e) => onNotice(String(e instanceof Error ? e.message : e), true));
  }, [sessionId, repo, reload]);

  useEffect(() => {
    setDetail(undefined);
    if (!path) return;
    api
      .request<ConflictList>(`${base}/conflicts?${repoQ}&path=${encodeURIComponent(path)}`)
      .then((l) => setDetail(l.file))
      .catch((e) => onNotice(String(e instanceof Error ? e.message : e), true));
  }, [path, reload]);

  // The pristine three-way conflict when git still has all three sides; the
  // working file (as the checkout wrote it) otherwise.
  const source = detail?.merged_text || detail?.working_text || "";
  const segments = useMemo(() => parseConflicts(source), [source]);
  const regions = segments.filter((s) => s.kind === "conflict");
  useEffect(() => {
    setChoices(regions.map(() => undefined));
    setResult(source);
    setEdited(false);
  }, [source]);

  function choose(i: number, c: Choice) {
    const next = [...choices];
    next[i] = c;
    setChoices(next);
    setResult(resolveConflicts(segments, next));
    setEdited(false);
  }

  async function save(body: { content?: string; take?: string; allow_markers?: boolean }) {
    setBusy(true);
    try {
      await api.request(`${base}/resolve`, { method: "POST", body: { repo, path, ...body } });
      onNotice(`Resolved ${path}.`);
      setReload((n) => n + 1);
      onResolved();
    } catch (e) {
      onNotice(e instanceof Error ? e.message : String(e), true);
    } finally {
      setBusy(false);
    }
  }

  if (!list) return <p className="sub">Loading conflicts…</p>;
  if (!list.files.length)
    return (
      <p className="sub conflicts-none">
        {list.operation
          ? `No conflicts left. Commit to finish the ${list.operation}.`
          : "No merge conflicts in this repository."}
      </p>
    );

  const unresolved = hasMarkers(result);
  return (
    <section className="conflicts" aria-label="Merge conflicts">
      <p className="sub">
        {list.operation ? `A ${list.operation} into ${list.branch} stopped on ` : ""}
        {list.files.length} conflicted file{list.files.length === 1 ? "" : "s"}.
      </p>
      <div className="conflict-files" role="tablist" aria-label="Conflicted files">
        {list.files.map((f) => (
          <button
            key={f.path}
            type="button"
            role="tab"
            aria-selected={f.path === path}
            className="b conflict-file"
            onClick={() => setPath(f.path)}
          >
            {f.path}
          </button>
        ))}
      </div>
      {!detail && <p className="sub">Loading {path}…</p>}
      {detail && (
        <>
          <div className="conflict-whole btnrow">
            <button type="button" className="b" disabled={busy || !detail.ours} onClick={() => void save({ take: "ours" })}>
              Take ours for the whole file
            </button>
            <button
              type="button"
              className="b"
              disabled={busy || !detail.theirs}
              onClick={() => void save({ take: "theirs" })}
            >
              Take theirs for the whole file
            </button>
          </div>
          {detail.binary ? (
            <p className="sub">This file is binary: take one side as a whole.</p>
          ) : (
            <>
              {!detail.ours || !detail.theirs ? (
                <p className="sub">
                  One side {detail.ours ? "deleted" : "never had"} this file. Take a side, or edit the result.
                </p>
              ) : null}
              <ol className="conflict-regions">
                {regions.map((r, i) =>
                  r.kind === "conflict" ? (
                    <li key={i} className="conflict-region" data-choice={choiceName(choices[i])}>
                      <div className="conflict-region-head">
                        <b>Conflict {i + 1}</b>
                        <span className="sub">{choiceName(choices[i])}</span>
                        <span className="btnrow">
                          {CHOICES.map(({ choice, label }) => (
                            <button
                              key={label}
                              type="button"
                              className="b"
                              aria-pressed={choices[i] === choice}
                              onClick={() => choose(i, choice)}
                            >
                              {label}
                            </button>
                          ))}
                        </span>
                      </div>
                      <div className={`conflict-sides${r.base ? " three" : ""}`}>
                        <figure className="conflict-ours">
                          <figcaption>Ours · {r.oursLabel || "HEAD"}</figcaption>
                          <pre>{r.ours.join("\n") || " "}</pre>
                        </figure>
                        {r.base && (
                          <figure className="conflict-base">
                            <figcaption>Base</figcaption>
                            <pre>{r.base.join("\n") || " "}</pre>
                          </figure>
                        )}
                        <figure className="conflict-theirs">
                          <figcaption>Theirs · {r.theirsLabel || "incoming"}</figcaption>
                          <pre>{r.theirs.join("\n") || " "}</pre>
                        </figure>
                      </div>
                    </li>
                  ) : null,
                )}
              </ol>
              <label className="f" htmlFor={`conflict-result-${sessionId}`}>
                Result {edited ? "(edited by hand)" : ""}
              </label>
              <textarea
                id={`conflict-result-${sessionId}`}
                className="f conflict-result"
                spellCheck={false}
                rows={Math.min(24, Math.max(8, result.split("\n").length + 1))}
                value={result}
                onChange={(e) => {
                  setResult(e.target.value);
                  setEdited(true);
                }}
              />
              <p className="sub">
                {unresolved
                  ? `${conflictCount(parseConflicts(result))} conflict(s) still unresolved.`
                  : "No conflict markers left."}
                {detail.working_text !== undefined &&
                  detail.merged_text &&
                  conflictCount(parseConflicts(detail.working_text)) !== regions.length && (
                  <>
                    {" "}
                    <button
                      type="button"
                      className="linkish"
                      onClick={() => {
                        setResult(detail.working_text ?? "");
                        setEdited(true);
                      }}
                    >
                      Start from the file as it is now
                    </button>
                  </>
                )}
              </p>
              <div className="btnrow">
                {unresolved ? (
                  <ConfirmButton
                    label="Save with markers…"
                    confirm="Save anyway"
                    className="b warn"
                    prompt="The result still has conflict markers."
                    onConfirm={() => void save({ content: result, allow_markers: true })}
                  />
                ) : null}
                <button
                  type="button"
                  className="b ok"
                  disabled={busy || unresolved}
                  onClick={() => void save({ content: result })}
                >
                  {busy ? "Saving…" : "Mark resolved"}
                </button>
              </div>
            </>
          )}
        </>
      )}
    </section>
  );
}
