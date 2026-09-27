// RFC 4180 CSV (and TSV) parsing for the table viewer: quoted fields may hold
// the delimiter, doubled quotes and line breaks.

export function detectDelimiter(path: string, text: string): string {
  if (/\.tsv$/i.test(path)) return "\t";
  const sample = text.slice(0, 4096).split("\n")[0] || "";
  const counts: [string, number][] = [",", "\t", ";", "|"].map((d) => [d, sample.split(d).length - 1]);
  counts.sort((a, b) => b[1] - a[1]);
  return counts[0]![1] > 0 ? counts[0]![0] : ",";
}

export function parseDelimited(text: string, delimiter = ",", maxRows = Infinity): { rows: string[][]; truncated: boolean } {
  const rows: string[][] = [];
  let row: string[] = [],
    field = "",
    quoted = false,
    i = 0;
  if (text.charCodeAt(0) === 0xfeff) i = 1;
  const endRow = () => {
    row.push(field);
    field = "";
    // A final newline does not make an empty last row.
    if (!(row.length === 1 && row[0] === "")) rows.push(row);
    row = [];
  };
  for (; i < text.length; i++) {
    const c = text[i]!;
    if (quoted) {
      if (c === '"') {
        if (text[i + 1] === '"') {
          field += '"';
          i++;
        } else quoted = false;
      } else field += c;
    } else if (c === '"' && field === "") quoted = true;
    else if (c === delimiter) {
      row.push(field);
      field = "";
    } else if (c === "\n" || c === "\r") {
      if (c === "\r" && text[i + 1] === "\n") i++;
      endRow();
      if (rows.length >= maxRows) return { rows, truncated: i < text.length - 1 };
    } else field += c;
  }
  if (field !== "" || row.length) endRow();
  return { rows, truncated: false };
}
