import { useEffect, useMemo, useRef, useState } from "react";
import { highlights, parseQuery, rank } from "./fuzzy";
import type { Index } from "./api";

function Highlighted({ text, marks, offset }: { text: string; marks: Set<number>; offset: number }) {
  return (
    <>
      {Array.from(text, (char, index) =>
        marks.has(index + offset) ? <mark key={index}>{char}</mark> : char,
      )}
    </>
  );
}

/**
 * Go to file: fuzzy-matches every path the workspace index returned, tracked
 * and untracked first and gitignored files as a second section. Matching runs
 * in the browser against a cached index, so typing never waits on the target.
 */
export function QuickOpen({
  index,
  loading,
  error,
  recent,
  onOpen,
  onClose,
}: {
  index?: Index;
  loading: boolean;
  error: string;
  recent: string[];
  onOpen: (path: string, line?: number, column?: number) => void;
  onClose: () => void;
}) {
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const parsed = parseQuery(query);
  const results = useMemo(() => {
    if (!index) return [];
    if (!parsed.text) {
      const known = new Set(index.files);
      const recents = recent.filter((path) => known.has(path)).map((path) => ({ path, score: 0, ignored: false }));
      const seen = new Set(recents.map((r) => r.path));
      return recents.concat(rank("", index.files, [], 50).filter((r) => !seen.has(r.path))).slice(0, 50);
    }
    return rank(parsed.text, index.files, index.ignored, 100);
  }, [index, parsed.text, recent]);
  useEffect(() => setSelected(0), [query]);
  useEffect(() => {
    input.current?.focus();
  }, []);
  useEffect(() => {
    list.current?.querySelector(`[data-index="${selected}"]`)?.scrollIntoView({ block: "nearest" });
  }, [selected]);
  const choose = (path: string) => onOpen(path, parsed.line, parsed.column);
  const firstIgnored = results.findIndex((r) => r.ignored);
  return (
    <div
      className="wb-quick-backdrop"
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div className="wb-quick" role="dialog" aria-label="Go to file" id="quick-open">
        <div className="wb-quick-row">
        <input
          ref={input}
          id="quick-open-input"
          role="combobox"
          aria-expanded="true"
          aria-controls="quick-open-results"
          aria-activedescendant={results.length ? "quick-open-" + selected : undefined}
          placeholder="Go to file — name, path, or name:line"
          autoComplete="off"
          spellCheck={false}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "ArrowDown") {
              event.preventDefault();
              setSelected(Math.min(results.length - 1, selected + 1));
            } else if (event.key === "ArrowUp") {
              event.preventDefault();
              setSelected(Math.max(0, selected - 1));
            } else if (event.key === "Enter") {
              event.preventDefault();
              const choice = results[selected];
              if (choice) choose(choice.path);
            } else if (event.key === "Escape") {
              event.preventDefault();
              event.stopPropagation();
              onClose();
            }
          }}
        />
        <button className="wb-quick-close" aria-label="Close Go to file" onClick={onClose}>
          Esc
        </button>
        </div>
        <div ref={list} id="quick-open-results" role="listbox" aria-label="Files">
          {error && <p className="wb-error" role="alert">{error}</p>}
          {!index && loading && <p className="wb-muted">Listing files…</p>}
          {index && !results.length && <p className="wb-muted">No matching files.</p>}
          {!parsed.text && index && recent.length > 0 && <p className="wb-section">Recently opened and top-level files</p>}
          {results.map((result, position) => {
            const slash = result.path.lastIndexOf("/");
            const name = result.path.slice(slash + 1),
              folder = slash > 0 ? result.path.slice(0, slash) : "";
            const marks = parsed.text ? highlights(parsed.text, result.path) : new Set<number>();
            return (
              <div key={(result.ignored ? "i:" : "") + result.path}>
                {position === firstIgnored && <p className="wb-section">Ignored by .gitignore</p>}
                <div
                  id={"quick-open-" + position}
                  data-index={position}
                  role="option"
                  aria-selected={position === selected}
                  className={"wb-quick-item" + (result.ignored ? " ignored" : "")}
                  onPointerMove={() => setSelected(position)}
                  onClick={() => choose(result.path)}
                >
                  <span className="wb-quick-name">
                    <Highlighted text={name} marks={marks} offset={slash + 1} />
                  </span>
                  <span className="wb-quick-folder">
                    <Highlighted text={folder} marks={marks} offset={0} />
                  </span>
                </div>
              </div>
            );
          })}
        </div>
        <p className="wb-quick-foot">
          {index
            ? `${index.files.length.toLocaleString()} files${index.ignored.length ? ` · ${index.ignored.length.toLocaleString()} ignored` : ""}${index.truncated ? " · list truncated" : ""}${loading ? " · refreshing…" : ""}`
            : " "}
          <span> ↑↓ to choose · Enter to open · Esc to close</span>
        </p>
      </div>
    </div>
  );
}
