import { useEffect, useMemo, useRef, useState } from "react";
export interface Command {
  id: string;
  title: string;
  category: string;
  detail?: string;
  keywords?: string;
  run: () => void | Promise<void>;
}
export function rankCommands(items: Command[], query: string) {
  const q = query.toLocaleLowerCase().trim(),
    terms = q.split(/\s+/).filter(Boolean);
  return items
    .map((item, order) => {
      const title = item.title.toLocaleLowerCase(),
        haystack = [title, item.category, item.detail, item.keywords]
          .join(" ")
          .toLocaleLowerCase();
      return terms.every((term) => haystack.includes(term))
        ? {
            item,
            order,
            score: !terms.length
              ? 0
              : title === q
                ? 3
                : title.startsWith(terms[0]!)
                  ? 2
                  : terms.every((term) => title.includes(term))
                    ? 1
                    : 0,
          }
        : null;
    })
    .filter((row) => row !== null)
    .sort((a, b) => b.score - a.score || a.order - b.order)
    .map((row) => row.item);
}
export function Palette({
  items,
  refresh,
  onClose,
  onError,
}: {
  items: Command[];
  refresh: () => Promise<void>;
  onClose: () => void;
  onError: (message: string) => void;
}) {
  const root = useRef<HTMLDialogElement>(null),
    input = useRef<HTMLInputElement>(null),
    close = useRef<HTMLButtonElement>(null);
  const [query, setQuery] = useState(""),
    [selected, setSelected] = useState(""),
    [loading, setLoading] = useState(true),
    [error, setError] = useState("");
  const all = useMemo(() => rankCommands(items, query), [items, query]),
    results = all.slice(0, 80),
    index = Math.max(
      0,
      results.findIndex((item) => item.id === selected),
    );
  useEffect(() => {
    const previous = document.activeElement;
    root.current?.showModal();
    input.current?.focus();
    let alive = true;
    void refresh()
      .catch(() => {
        if (alive) setError("Could not refresh. Showing available results.");
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    const fit = () =>
      root.current?.style.setProperty(
        "--command-height",
        `${visualViewport?.height || innerHeight}px`,
      );
    fit();
    visualViewport?.addEventListener("resize", fit);
    window.addEventListener("resize", fit);
    return () => {
      alive = false;
      root.current?.close();
      visualViewport?.removeEventListener("resize", fit);
      window.removeEventListener("resize", fit);
      if (previous instanceof HTMLElement) previous.focus();
    };
  }, []);
  useEffect(() => {
    document
      .getElementById(`command-option-${index}`)
      ?.scrollIntoView({ block: "nearest" });
  }, [index, query]);
  function choose(item: Command | undefined) {
    if (!item) return;
    onClose();
    Promise.resolve()
      .then(item.run)
      .catch((error) => onError(String(error)));
  }
  return (
    <dialog
      ref={root}
      className="command-palette"
      aria-label="Search Lectern"
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
      onKeyDown={(event) => {
        if (event.nativeEvent.isComposing) return;
        if (event.key === "Escape") {
          event.preventDefault();
          onClose();
        }
        if (event.key === "ArrowDown" || event.key === "ArrowUp") {
          event.preventDefault();
          const next = Math.max(
            0,
            Math.min(
              results.length - 1,
              index + (event.key === "ArrowDown" ? 1 : -1),
            ),
          );
          setSelected(results[next]?.id || "");
        }
        if (event.key === "Enter" && event.target === input.current) {
          event.preventDefault();
          choose(results[index]);
        }
        if (event.key === "Tab") {
          event.preventDefault();
          (document.activeElement === input.current
            ? close.current
            : input.current
          )?.focus();
        }
      }}
    >
      <div className="command-search">
        <label className="sr-only" htmlFor="command-query">
          Search sessions, tasks, and actions
        </label>
        <input
          ref={input}
          id="command-query"
          type="search"
          autoComplete="off"
          placeholder="Search sessions, tasks, actions…"
          role="combobox"
          aria-autocomplete="list"
          aria-expanded="true"
          aria-controls="command-results"
          aria-activedescendant={
            results.length ? `command-option-${index}` : undefined
          }
          value={query}
          onChange={(event) => {
            setQuery(event.target.value);
            setSelected("");
          }}
        />
        <button
          ref={close}
          type="button"
          aria-label="Close search"
          onClick={onClose}
        >
          <span className="command-close-text">Esc</span>
          <span className="command-close-icon">×</span>
        </button>
      </div>
      <p className="command-status" role="status" aria-live="polite">
        {error ||
          (all.length
            ? `${all.length} ${all.length === 1 ? "result" : "results"}${all.length > 80 ? " · showing the first 80; type to narrow" : ""}${loading ? " · updating…" : ""}`
            : loading
              ? "Searching…"
              : "No matches. Try a session name, project, or action.")}
      </p>
      <div id="command-results" role="listbox" aria-label="Search results">
        {results.map((item, i) => (
          <div
            id={`command-option-${i}`}
            key={item.id}
            role="option"
            aria-selected={i === index}
            className="command-result"
            data-command-id={item.id}
            onPointerDown={(event) => event.preventDefault()}
            onClick={() => choose(item)}
          >
            <strong>{item.title}</strong>
            <span>
              {[item.category, item.detail].filter(Boolean).join(" · ")}
            </span>
          </div>
        ))}
      </div>
      <p className="command-help">
        ↑ ↓ to choose · Enter to open · Esc to close
      </p>
    </dialog>
  );
}
