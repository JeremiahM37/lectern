// Quick Open's ranking. The scoring follows fzy's published algorithm
// (github.com/jhawthorn/fzy, MIT): characters must appear in order, and a
// match earns more at the start of a word, after a path separator, on a
// capital, and next to the previous matched character. Gaps cost a little.
// Two additions for file paths: matches in the file name beat the same match
// in a folder name, and an exact name or name-prefix match wins outright.

const GAP_LEADING = -0.005,
  GAP_TRAILING = -0.005,
  GAP_INNER = -0.01,
  CONSECUTIVE = 1.0,
  AFTER_SLASH = 0.9,
  AFTER_WORD = 0.8,
  CAPITAL = 0.7,
  AFTER_DOT = 0.6;

function bonuses(text: string): Float64Array {
  const out = new Float64Array(text.length);
  let previous = "/";
  for (let i = 0; i < text.length; i++) {
    const c = text[i]!;
    if (previous === "/") out[i] = AFTER_SLASH;
    else if (previous === "-" || previous === "_" || previous === " ") out[i] = AFTER_WORD;
    else if (previous === ".") out[i] = AFTER_DOT;
    else if (c >= "A" && c <= "Z" && previous >= "a" && previous <= "z") out[i] = CAPITAL;
    previous = c;
  }
  return out;
}

/** In-order subsequence test, case-insensitive. Cheap filter before scoring. */
export function matches(needle: string, haystack: string): boolean {
  let at = 0;
  for (let i = 0; i < needle.length; i++) {
    at = haystack.indexOf(needle[i]!, at);
    if (at < 0) return false;
    at++;
  }
  return true;
}

/** fzy's score for needle (lowercase) in text; -Infinity when it cannot match. */
export function score(needle: string, text: string, positions?: number[]): number {
  const n = needle.length,
    m = text.length;
  const lower = text.toLowerCase();
  if (!n) return 0;
  if (n > m || !matches(needle, lower)) return -Infinity;
  if (n === m) {
    positions?.push(...Array.from({ length: n }, (_, i) => i));
    return Infinity;
  }
  const bonus = bonuses(text);
  // D: best score ending in a match at j; M: best score up to j.
  const D: Float64Array[] = [],
    M: Float64Array[] = [];
  for (let i = 0; i < n; i++) {
    const d = new Float64Array(m),
      s = new Float64Array(m);
    let best = -Infinity;
    const gap = i === n - 1 ? GAP_TRAILING : GAP_INNER;
    for (let j = 0; j < m; j++) {
      if (lower[j] === needle[i]) {
        let value = -Infinity;
        if (!i) value = j * GAP_LEADING + bonus[j]!;
        else if (j) value = Math.max(M[i - 1]![j - 1]! + bonus[j]!, D[i - 1]![j - 1]! + CONSECUTIVE);
        d[j] = value;
        s[j] = best = Math.max(value, best + gap);
      } else {
        d[j] = -Infinity;
        s[j] = best = best + gap;
      }
    }
    D.push(d);
    M.push(s);
  }
  if (positions) {
    // Walk back through the matrices to find which characters matched.
    let forceMatch = false;
    let j = m - 1;
    const found: number[] = [];
    for (let i = n - 1; i >= 0; i--) {
      for (; j >= 0; j--) {
        if (D[i]![j]! !== -Infinity && (forceMatch || D[i]![j]! === M[i]![j]!)) {
          forceMatch = i > 0 && j > 0 && M[i]![j]! === D[i - 1]![j - 1]! + CONSECUTIVE;
          found.push(j);
          j--;
          break;
        }
      }
    }
    positions.push(...found.reverse());
  }
  return M[n - 1]![m - 1]!;
}

export interface Ranked {
  path: string;
  score: number;
  ignored: boolean;
}

/** A query may end in :line or :line:col, as copied from a compiler error. */
export function parseQuery(raw: string): { text: string; line?: number; column?: number } {
  const trimmed = raw.trim();
  const match = /^(.*?)(?::(\d+))(?::(\d+))?$/.exec(trimmed);
  if (match && match[1]) return { text: match[1], line: Number(match[2]), column: match[3] ? Number(match[3]) : undefined };
  return { text: trimmed };
}

function basename(path: string) {
  return path.slice(path.lastIndexOf("/") + 1);
}

/** Score one path: best of the name alone (weighted) and the whole path. */
export function rankPath(needle: string, path: string): number {
  if (!needle) return 0;
  const name = basename(path),
    lowerName = name.toLowerCase();
  let best = score(needle, path);
  if (best === -Infinity) return best;
  if (lowerName === needle) return 1e6 - path.length;
  if (lowerName.startsWith(needle)) best = Math.max(best, 1e4 - path.length);
  const inName = score(needle, name);
  if (inName !== -Infinity) best = Math.max(best, (inName === Infinity ? 1e3 : inName) + 2);
  // Shorter paths win ties: a file near the root is usually the one meant.
  return best - path.length * 0.001;
}

/**
 * Rank every path for a query. Space-separated terms must all match (each
 * against the full path); results are ordered by score, then shorter path.
 */
export function rank(query: string, files: string[], ignored: string[] = [], limit = 200): Ranked[] {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  const out: Ranked[] = [];
  const consider = (path: string, isIgnored: boolean) => {
    const lower = path.toLowerCase();
    let total = 0;
    for (const term of terms) {
      if (!matches(term, lower)) return;
      const value = rankPath(term, path);
      if (value === -Infinity) return;
      total += value;
    }
    out.push({ path, score: total, ignored: isIgnored });
  };
  if (!terms.length) {
    // Nothing typed: the shortest paths first, which are usually the
    // project's own top-level files.
    return files
      .slice()
      .sort((a, b) => a.split("/").length - b.split("/").length || a.localeCompare(b))
      .slice(0, limit)
      .map((path) => ({ path, score: 0, ignored: false }));
  }
  for (const path of files) consider(path, false);
  for (const path of ignored) consider(path, true);
  out.sort((a, b) => Number(a.ignored) - Number(b.ignored) || b.score - a.score || a.path.length - b.path.length || a.path.localeCompare(b.path));
  const tracked = out.filter((item) => !item.ignored).slice(0, limit),
    rest = out.filter((item) => item.ignored).slice(0, Math.max(20, limit - tracked.length));
  return tracked.concat(rest);
}

/** Character positions in path that matched, for highlighting a result. */
export function highlights(query: string, path: string): Set<number> {
  const out = new Set<number>();
  for (const term of query.toLowerCase().split(/\s+/).filter(Boolean)) {
    const name = basename(path),
      offset = path.length - name.length,
      positions: number[] = [];
    if (score(term, name, positions) !== -Infinity) positions.forEach((p) => out.add(p + offset));
    else {
      score(term, path, positions);
      positions.forEach((p) => out.add(p));
    }
  }
  return out;
}
