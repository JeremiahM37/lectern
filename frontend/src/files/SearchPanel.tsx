import { t } from "../i18n";
import { useEffect, useMemo, useRef, useState } from "react";
import { errorMessage } from "../terminal/model";
import type { FileApi, Hit, SearchOptions } from "./api";

const OPTIONS_KEY = "lec-files-search-options";
function loadOptions(): SearchOptions {
  try {
    return { regex: false, caseSensitive: false, word: false, include: "", ignored: false, ...JSON.parse(localStorage.getItem(OPTIONS_KEY) || "{}") };
  } catch {
    return { regex: false, caseSensitive: false, word: false, include: "", ignored: false };
  }
}

/** Project-wide search, run on the target (ripgrep, else git grep). */
export function SearchPanel({
  api,
  visible,
  focusKey,
  onOpen,
}: {
  api: FileApi;
  visible: boolean;
  focusKey: number;
  onOpen: (hit: Hit) => void;
}) {
  const [query, setQuery] = useState("");
  const [options, setOptions] = useState(loadOptions);
  const [state, setState] = useState<{ results: Hit[]; truncated: boolean; source: string; query: string }>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [collapsed, setCollapsed] = useState<string[]>([]);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    try {
      localStorage.setItem(OPTIONS_KEY, JSON.stringify(options));
    } catch {}
  }, [options]);
  useEffect(() => {
    if (visible) input.current?.focus();
  }, [visible, focusKey]);
  // Search as you type, after a short pause; each new search cancels the last.
  useEffect(() => {
    if (!query.trim()) {
      setState(undefined);
      setError("");
      return;
    }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      setBusy(true);
      setError("");
      api
        .search(query, options, controller.signal)
        .then((result) => setState({ ...result, query }))
        .catch((reason) => {
          if (!controller.signal.aborted) setError(errorMessage(reason));
        })
        .finally(() => {
          if (!controller.signal.aborted) setBusy(false);
        });
    }, 300);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [query, options, api]);
  const groups = useMemo(() => {
    const map = new Map<string, Hit[]>();
    for (const hit of state?.results || []) map.set(hit.path, [...(map.get(hit.path) || []), hit]);
    return [...map.entries()];
  }, [state]);
  const toggle = (key: keyof SearchOptions) => setOptions({ ...options, [key]: !options[key] });
  return (
    <div className="wb-search" id="files-search">
      <div className="wb-search-row">
        <input
          ref={input}
          id="files-search-input"
          type="search"
          placeholder={t("files.searchPlaceholder")}
          aria-label={t("files.searchPlaceholder")}
          spellCheck={false}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <button aria-label={t("files.matchCase")} title={t("files.matchCase")} aria-pressed={options.caseSensitive} onClick={() => toggle("caseSensitive")}>
          Aa
        </button>
        <button aria-label={t("files.wholeWord")} title={t("files.wholeWord")} aria-pressed={options.word} onClick={() => toggle("word")}>
          <u>ab</u>
        </button>
        <button aria-label={t("files.regex")} title={t("files.regex")} aria-pressed={options.regex} onClick={() => toggle("regex")}>
          .*
        </button>
      </div>
      <div className="wb-search-row">
        <input
          type="text"
          placeholder={t("files.include")}
          aria-label={t("files.includeLabel")}
          spellCheck={false}
          value={options.include}
          onChange={(event) => setOptions({ ...options, include: event.target.value })}
        />
        <label className="wb-check" title={t("files.ignoredHint")}>
          <input type="checkbox" checked={options.ignored} onChange={() => toggle("ignored")} /> {t("files.ignored")}
        </label>
      </div>
      <p className="wb-search-summary" role="status">
        {error ? (
          <span className="wb-error">{error}</span>
        ) : busy ? (
          t("files.searching")
        ) : state ? (
          t("files.matches", { matches: t("files.match", { count: state.results.length }), files: t("files.file", { count: groups.length }), more: state.truncated ? t("files.firstShown") : "" })
        ) : (
          ""
        )}
      </p>
      <div className="wb-search-results">
        {groups.map(([path, hits]) => {
          const folded = collapsed.includes(path);
          return (
            <section key={path} className="wb-search-file">
              <button
                className="wb-search-path"
                aria-expanded={!folded}
                onClick={() => setCollapsed(folded ? collapsed.filter((p) => p !== path) : [...collapsed, path])}
              >
                <span>{folded ? "▸" : "▾"}</span> {path} <span className="wb-count">{hits.length}</span>
              </button>
              {!folded &&
                hits.map((hit) => {
                  // Show the match with some context, trimmed on long lines.
                  const from = Math.max(0, hit.column - 1 - 40);
                  const before = hit.text.slice(from, hit.column - 1),
                    match = hit.text.slice(hit.column - 1, Math.max(hit.column, hit.end - 1)),
                    after = hit.text.slice(Math.max(hit.column, hit.end - 1), Math.max(hit.column, hit.end - 1) + 120);
                  return (
                    <button key={hit.line + ":" + hit.column} className="wb-search-hit" title={`${path}:${hit.line}:${hit.column}`} onClick={() => onOpen(hit)}>
                      <span className="wb-hit-line">{hit.line}</span>
                      <span className="wb-hit-text">
                        {from > 0 ? "…" : ""}
                        {before.trimStart()}
                        <mark>{match}</mark>
                        {after}
                      </span>
                    </button>
                  );
                })}
            </section>
          );
        })}
      </div>
    </div>
  );
}
