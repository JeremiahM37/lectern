// Explorer order: folders first, then names compared as people read them —
// "file2" before "file10", case and accents ignored.
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });

export function naturalCompare(a: string, b: string): number {
  return collator.compare(a, b) || (a < b ? -1 : a > b ? 1 : 0);
}

export function sortEntries<T extends { name: string; directory: boolean }>(entries: T[]): T[] {
  return entries.slice().sort((a, b) => Number(b.directory) - Number(a.directory) || naturalCompare(a.name, b.name));
}
