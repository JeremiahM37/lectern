// Saved replies for the key bar. Working an agent from a phone is mostly a
// handful of short answers — yes, option 2, continue, clear the context — typed
// on a keyboard that makes each one slow. A snippet is one tap.
export interface Snippet {
  text: string;
  // Sent with Enter, which is what a reply wants; off for a fragment to edit.
  enter: boolean;
}

const KEY = "lec-terminal-snippets";

export const defaultSnippets: Snippet[] = [
  { text: "y", enter: true },
  { text: "1", enter: true },
  { text: "2", enter: true },
  { text: "3", enter: true },
  { text: "continue", enter: true },
  { text: "/compact", enter: true },
  { text: "/clear", enter: true },
];

export function parseSnippets(raw: string | null): Snippet[] {
  if (raw === null) return defaultSnippets;
  try {
    const value: unknown = JSON.parse(raw);
    if (!Array.isArray(value)) return defaultSnippets;
    return value
      .filter(
        (row): row is Snippet =>
          !!row && typeof row === "object" && typeof row.text === "string" && row.text.length > 0,
      )
      .slice(0, 60)
      .map((row) => ({ text: row.text.slice(0, 2000), enter: row.enter !== false }));
  } catch {
    return defaultSnippets;
  }
}

export function loadSnippets(): Snippet[] {
  try {
    return parseSnippets(localStorage.getItem(KEY));
  } catch {
    return defaultSnippets;
  }
}

export function saveSnippets(snippets: Snippet[]) {
  try {
    localStorage.setItem(KEY, JSON.stringify(snippets));
  } catch {}
}

// What actually goes down the wire: Enter is a carriage return to a terminal.
export const snippetBytes = (snippet: Snippet) => snippet.text + (snippet.enter ? "\r" : "");
