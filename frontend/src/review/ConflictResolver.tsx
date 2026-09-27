import { useEffect, useMemo, useState } from "react";
import { conflictCount, hasMarkers, parseConflicts, resolveConflicts, type Choice } from "./conflicts";
import { ConfirmButton, type ReviewApi } from "./GitPanel";
import { t, useLocale } from "../i18n";

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

const CHOICES = (): { choice: Choice; label: string }[] => [
  { choice: "ours", label: t("review.conflicts.acceptOurs") },
  { choice: "theirs", label: t("review.conflicts.acceptTheirs") },
  { choice: "both", label: t("review.conflicts.acceptBoth") },
];

function choiceName(c: Choice | undefined): string {
  if (c === undefined) return "unresolved";
  if (typeof c === "object") return "edited";
  return { ours: "ours", theirs: "theirs", both: "both", "both-theirs-first": "both", base: "base" }[c];
}

// choiceName, as shown to the person (choiceName itself is also a data- value).
function choiceLabel(c: Choice | undefined): string {
  const labels: Record<string, string> = {
    unresolved: t("review.conflicts.choice.unresolved"),
    edited: t("review.conflicts.choice.edited"),
    ours: t("review.conflicts.choice.ours"),
    theirs: t("review.conflicts.choice.theirs"),
    both: t("review.conflicts.choice.both"),
    base: t("review.conflicts.choice.base"),
  };
  const name = choiceName(c);
  return labels[name] ?? name;
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
  useLocale();
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
      onNotice(t("review.conflicts.resolved", { path }));
      setReload((n) => n + 1);
      onResolved();
    } catch (e) {
      onNotice(e instanceof Error ? e.message : String(e), true);
    } finally {
      setBusy(false);
    }
  }

  if (!list) return <p className="sub">{t("review.conflicts.loading")}</p>;
  if (!list.files.length)
    return (
      <p className="sub conflicts-none">
        {list.operation
          ? t("review.conflicts.noneLeft", { operation: list.operation })
          : t("review.conflicts.none")}
      </p>
    );

  const unresolved = hasMarkers(result);
  return (
    <section className="conflicts" aria-label={t("review.conflicts.label")}>
      <p className="sub">
        {list.operation
          ? t("review.conflicts.stoppedOn", { operation: list.operation, branch: list.branch, count: list.files.length })
          : t("review.conflicts.files", { count: list.files.length })}
      </p>
      <div className="conflict-files" role="tablist" aria-label={t("review.conflicts.filesLabel")}>
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
      {!detail && <p className="sub">{t("review.conflicts.loadingPath", { path })}</p>}
      {detail && (
        <>
          <div className="conflict-whole btnrow">
            <button type="button" className="b" disabled={busy || !detail.ours} onClick={() => void save({ take: "ours" })}>
              {t("review.conflicts.takeOurs")}
            </button>
            <button
              type="button"
              className="b"
              disabled={busy || !detail.theirs}
              onClick={() => void save({ take: "theirs" })}
            >
              {t("review.conflicts.takeTheirs")}
            </button>
          </div>
          {detail.binary ? (
            <p className="sub">{t("review.conflicts.binary")}</p>
          ) : (
            <>
              {!detail.ours || !detail.theirs ? (
                <p className="sub">
                  {detail.ours ? t("review.conflicts.oneSideDeleted") : t("review.conflicts.oneSideNever")}
                </p>
              ) : null}
              <ol className="conflict-regions">
                {regions.map((r, i) =>
                  r.kind === "conflict" ? (
                    <li key={i} className="conflict-region" data-choice={choiceName(choices[i])}>
                      <div className="conflict-region-head">
                        <b>{t("review.conflicts.conflictN", { n: i + 1 })}</b>
                        <span className="sub">{choiceLabel(choices[i])}</span>
                        <span className="btnrow">
                          {CHOICES().map(({ choice, label }) => (
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
                          <figcaption>{t("review.conflicts.ours", { label: r.oursLabel || "HEAD" })}</figcaption>
                          <pre>{r.ours.join("\n") || " "}</pre>
                        </figure>
                        {r.base && (
                          <figure className="conflict-base">
                            <figcaption>{t("review.conflicts.base")}</figcaption>
                            <pre>{r.base.join("\n") || " "}</pre>
                          </figure>
                        )}
                        <figure className="conflict-theirs">
                          <figcaption>{t("review.conflicts.theirs", { label: r.theirsLabel || t("review.conflicts.incoming") })}</figcaption>
                          <pre>{r.theirs.join("\n") || " "}</pre>
                        </figure>
                      </div>
                    </li>
                  ) : null,
                )}
              </ol>
              <label className="f" htmlFor={`conflict-result-${sessionId}`}>
                {t("review.conflicts.result", { edited: edited ? t("review.conflicts.editedByHand") : "" })}
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
                  ? t("review.conflicts.stillUnresolved", { n: conflictCount(parseConflicts(result)) })
                  : t("review.conflicts.noMarkers")}
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
                      {t("review.conflicts.startFromFile")}
                    </button>
                  </>
                )}
              </p>
              <div className="btnrow">
                {unresolved ? (
                  <ConfirmButton
                    label={t("review.conflicts.saveWithMarkers")}
                    confirm={t("review.conflicts.saveAnyway")}
                    className="b warn"
                    prompt={t("review.conflicts.stillHasMarkers")}
                    onConfirm={() => void save({ content: result, allow_markers: true })}
                  />
                ) : null}
                <button
                  type="button"
                  className="b ok"
                  disabled={busy || unresolved}
                  onClick={() => void save({ content: result })}
                >
                  {busy ? t("review.conflicts.saving") : t("review.conflicts.markResolved")}
                </button>
              </div>
            </>
          )}
        </>
      )}
    </section>
  );
}
