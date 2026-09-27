import { useCallback, useState } from "react";

// Per-device review preferences, kept in localStorage like the terminal's
// split ratio: the diff mode (unified or side by side), word wrap, the file
// tree and the authorship gutter. Every key is read and written defensively;
// a browser that refuses storage just gets the defaults.

function read(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function write(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* storage unavailable: the choice lasts for this page only */
  }
}

export function useStoredPref<T extends string>(
  key: string,
  fallback: T,
  allowed: readonly T[],
): [T, (v: T) => void] {
  const [value, setValue] = useState<T>(() => {
    const stored = read(key) as T | null;
    return stored && allowed.includes(stored) ? stored : fallback;
  });
  const set = useCallback(
    (v: T) => {
      setValue(v);
      write(key, v);
    },
    [key],
  );
  return [value, set];
}

export function useStoredFlag(key: string, fallback: boolean): [boolean, (v: boolean) => void] {
  const [v, set] = useStoredPref(key, fallback ? "1" : "0", ["1", "0", ""] as const);
  // "" is the value older builds wrote for "off" (lec-diffwrap).
  return [v === "1", (on: boolean) => set(on ? "1" : "0")];
}

export const PREF_KEYS = {
  mode: "lec-diffmode",
  wrap: "lec-diffwrap",
  tree: "lec-difftree",
  authorship: "lec-diffauthors",
} as const;
