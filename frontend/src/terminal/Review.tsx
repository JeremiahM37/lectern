import { errorMessage } from "./model";
import { useContext, useEffect, useRef, useState } from "react";
import { PaneEmbedContext } from "../workspace/registry";
import { json } from "./model";
import { t, useLocale } from "../i18n";
import "./review.css";
interface ChangedFile {
  path: string;
  status: string;
  working: boolean;
  staged: boolean;
}
interface Changes {
  path: string;
  patch: string;
  files: ChangedFile[];
  repositories?: { id: number; name: string }[];
  selected_repository?: number;
  branch: string;
  truncated: boolean;
}
export function Review({
  kind,
  id,
  name,
  onClose,
}: {
  kind: string;
  id: string;
  name?: string;
  onClose: () => void;
}) {
  useLocale();
  const root = useRef<HTMLDialogElement>(null),
    patch = useRef<HTMLDivElement>(null);
  const [scope, setScope] = useState<"working" | "staged">("working");
  const [repository, setRepository] = useState("0");
  const [selected, setSelected] = useState("");
  const [query, setQuery] = useState("");
  const [data, setData] = useState<Changes>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [wrap, setWrap] = useState(
    () => localStorage.getItem("lec-review-wrap") !== "0",
  );
  const embedded = useContext(PaneEmbedContext);
  useEffect(() => {
    const previous = document.activeElement;
    if (embedded) root.current?.show();
    else root.current?.showModal();
    return () => {
      root.current?.close();
      if (!embedded && previous instanceof HTMLElement) previous.focus();
    };
  }, []);
  useEffect(() => {
    const abort = new AbortController();
    setBusy(true);
    setError("");
    void json<Changes>(
      `/api/term/${encodeURIComponent(kind)}/${encodeURIComponent(id)}/changes?scope=${scope}&path=${encodeURIComponent(selected)}&repository=${encodeURIComponent(repository)}`,
      { signal: abort.signal },
    )
      .then((next) => {
        if (abort.signal.aborted) return;
        setData(next);
        if (patch.current) {
          patch.current.scrollTop = 0;
          patch.current.scrollLeft = 0;
        }
      })
      .catch((error) => {
        if (!abort.signal.aborted) {
          setData(undefined);
          setError(errorMessage(error));
        }
      })
      .finally(() => {
        if (!abort.signal.aborted) setBusy(false);
      });
    return () => abort.abort();
  }, [kind, id, scope, selected, repository, refresh]);
  let oldLine = 0,
    newLine = 0;
  const lines = (
    data?.patch ||
    t("terminalPage.review.noTextual")
  )
    .split("\n")
    .map((line, index) => {
      const match = line.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
      let before: number | string = "",
        after: number | string = "",
        tone = "";
      if (match) {
        oldLine = Number(match[1]);
        newLine = Number(match[2]);
        tone = "review-hunk";
      } else if (oldLine || newLine) {
        if (line.startsWith("+")) {
          after = newLine++;
          tone = "review-add";
        } else if (line.startsWith("-")) {
          before = oldLine++;
          tone = "review-del";
        } else if (line.startsWith(" ")) {
          before = oldLine++;
          after = newLine++;
        }
      }
      return (
        <div key={index} className={`review-line ${tone}`}>
          <span className="review-number" aria-hidden="true">
            {before}
          </span>
          <span className="review-number" aria-hidden="true">
            {after}
          </span>
          <code>{line || " "}</code>
        </div>
      );
    });
  const files = (data?.files || []).filter(
    (file) =>
      file[scope] &&
      file.path.toLocaleLowerCase().includes(query.toLocaleLowerCase()),
  );
  return (
    <dialog
      ref={root}
      className={`code-review ${wrap ? "review-wrapped" : ""}`}
      aria-label={t("terminalPage.review.title")}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onKeyDown={(event) => event.stopPropagation()}
    >
      <header>
        <div>
          <h2>{t("terminalPage.review.title")}</h2>
          <p className="review-name">{name || `${kind} ${id}`}</p>
        </div>
        <button
          className="review-close"
          aria-label={t("terminalPage.review.close")}
          onClick={onClose}
        >
          ×
        </button>
      </header>
      <div className="review-toolbar">
        <label
          className="review-repository-label"
          hidden={(data?.repositories?.length || 0) < 2}
        >
          {t("terminalPage.review.repository")}
          <select
            className="review-repository"
            aria-label={t("terminalPage.review.repository")}
            value={repository}
            onChange={(event) => {
              setRepository(event.target.value);
              setSelected("");
              setQuery("");
            }}
          >
            {data?.repositories?.map((repo) => (
              <option key={repo.id} value={repo.id}>
                {repo.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t("terminalPage.review.changes")}
          <select
            className="review-scope"
            value={scope}
            onChange={(event) => {
              setScope(event.target.value as "working" | "staged");
              setSelected("");
            }}
          >
            <option value="working">
              {t("terminalPage.review.working", { files: data?.files.filter((file) => file.working).length || 0 })}
            </option>
            <option value="staged">
              {t("terminalPage.review.staged", { files: data?.files.filter((file) => file.staged).length || 0 })}
            </option>
          </select>
        </label>
        <span className="review-branch">{data?.branch}</span>
        <button
          className="review-refresh"
          onClick={() => setRefresh((old) => old + 1)}
        >
          {t("terminalPage.common.refresh")}
        </button>
        <button
          className="review-wrap"
          aria-pressed={wrap}
          onClick={() => {
            setWrap(!wrap);
            localStorage.setItem("lec-review-wrap", wrap ? "0" : "1");
          }}
        >
          {t("terminalPage.review.wrap")}
        </button>
      </div>
      <p className="review-status" role="status">
        {busy
          ? t("terminalPage.review.loading")
          : error ||
            (!data
              ? ""
              : data.truncated
                ? t("terminalPage.review.truncated")
                : t("terminalPage.review.summary", { files: data.files.filter((file) => file[scope]).length, time: new Date().toLocaleTimeString() }))}
      </p>
      <div className="review-layout">
        <aside>
          <input
            className="review-filter"
            type="search"
            placeholder={t("terminalPage.review.filter")}
            aria-label={t("terminalPage.review.filter")}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
          <div className="review-files" aria-label={t("terminalPage.review.changedFiles")}>
            {files.map((file) => (
              <button
                key={file.path}
                className="review-file"
                title={file.path}
                aria-pressed={file.path === data?.path}
                onClick={() => setSelected(file.path)}
              >
                <span className="review-file-status">{file.status}</span>
                <span>{file.path}</span>
              </button>
            ))}
            {!files.length &&
              (query ? t("terminalPage.review.noMatching") : t("terminalPage.review.noChanges"))}
          </div>
        </aside>
        <section className="review-detail">
          <div className="review-path">{data?.path || t("terminalPage.review.noFile")}</div>
          <div
            className="review-patch"
            ref={patch}
            tabIndex={0}
            aria-label={t("terminalPage.review.diff")}
            aria-busy={busy}
          >
            {error
              ? t("terminalPage.review.loadFailed")
              : data?.path
                ? lines
                : t("terminalPage.review.nothing")}
          </div>
        </section>
      </div>
      <footer>
        {t("terminalPage.review.footer")}
      </footer>
    </dialog>
  );
}
