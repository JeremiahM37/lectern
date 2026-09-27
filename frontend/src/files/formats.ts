// How a workspace file is shown, decided from its name and first bytes.

export type ViewKind = "code" | "markdown" | "html" | "mermaid" | "csv" | "notebook" | "image" | "svg" | "pdf" | "binary";

const IMAGES: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  avif: "image/avif",
  bmp: "image/bmp",
  ico: "image/x-icon",
};

export function extension(path: string): string {
  const name = path.slice(path.lastIndexOf("/") + 1).toLowerCase();
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(dot + 1) : "";
}

export function mimeFor(path: string): string {
  const ext = extension(path);
  return IMAGES[ext] || (ext === "svg" ? "image/svg+xml" : ext === "pdf" ? "application/pdf" : "application/octet-stream");
}

/** The view for a file; binary sniffing needs its first bytes. */
export function viewKind(path: string, head: Uint8Array): ViewKind {
  const ext = extension(path);
  if (IMAGES[ext]) return "image";
  if (ext === "svg") return "svg";
  if (ext === "pdf") return "pdf";
  if (head.includes(0)) return "binary";
  if (["md", "markdown", "mdx", "mdown"].includes(ext)) return "markdown";
  if (["html", "htm", "xhtml"].includes(ext)) return "html";
  if (["mmd", "mermaid"].includes(ext)) return "mermaid";
  if (["csv", "tsv"].includes(ext)) return "csv";
  if (ext === "ipynb") return "notebook";
  return "code";
}

/** Whether a view has a rendered form besides its source text. */
export function rendered(kind: ViewKind): boolean {
  return ["markdown", "html", "mermaid", "csv", "notebook", "svg"].includes(kind);
}

const LANGUAGES: Record<string, string> = {
  ts: "typescript", tsx: "typescript", mts: "typescript", cts: "typescript",
  js: "javascript", jsx: "javascript", mjs: "javascript", cjs: "javascript",
  json: "json", jsonc: "json", ipynb: "json", webmanifest: "json",
  css: "css", scss: "scss", less: "less",
  html: "html", htm: "html", xhtml: "html", vue: "html", svelte: "html",
  md: "markdown", markdown: "markdown", mdx: "markdown", mdown: "markdown",
  py: "python", pyi: "python", go: "go", rs: "rust", rb: "ruby", php: "php",
  java: "java", kt: "kotlin", kts: "kotlin", scala: "scala", swift: "swift",
  c: "c", h: "c", cc: "cpp", cpp: "cpp", cxx: "cpp", hpp: "cpp", hh: "cpp",
  cs: "csharp", fs: "fsharp", sh: "shell", bash: "shell", zsh: "shell", fish: "shell",
  ps1: "powershell", psm1: "powershell", bat: "bat", cmd: "bat",
  yml: "yaml", yaml: "yaml", toml: "ini", ini: "ini", cfg: "ini", conf: "ini", env: "ini",
  xml: "xml", svg: "xml", plist: "xml", sql: "sql", graphql: "graphql", gql: "graphql",
  lua: "lua", r: "r", pl: "perl", pm: "perl", dart: "dart", ex: "elixir", exs: "elixir",
  clj: "clojure", hcl: "hcl", tf: "hcl", proto: "protobuf", sol: "sol", jl: "julia",
  dockerfile: "dockerfile", mmd: "markdown", mermaid: "markdown", csv: "plaintext", tsv: "plaintext",
};

export function languageFor(path: string): string {
  const name = path.slice(path.lastIndexOf("/") + 1).toLowerCase();
  if (name === "dockerfile" || name.startsWith("dockerfile.")) return "dockerfile";
  if (name === "makefile" || name === "gnumakefile") return "plaintext";
  if (name === ".bashrc" || name === ".zshrc" || name === ".profile") return "shell";
  return LANGUAGES[extension(path)] || "plaintext";
}
