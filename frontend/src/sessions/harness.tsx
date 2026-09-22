import React from "react";
import { createRoot } from "react-dom/client";
import { Sessions, type SessionsApi } from "./Sessions";
import type { Project, SessionView, Target } from "../types";
const project = {
  id: 1,
  name: "Deck",
  target_id: 1,
  target_name: "local",
  setup_cmd: "",
  repo_path: "/repo",
  default_base_branch: "main",
  workroot_override: "",
  policy_json: "{}",
  verify_cmd: "",
  keep_worktrees: 0,
  review_gate: 0,
  env_json: "{}",
  context_json: "{}",
  strict_mcp: 0,
  permissions_json: "{}",
  gate_matcher: "",
  default_agent: "claude",
  capability_profile: "full",
  default_permission_mode: "acceptEdits",
  skill_sources_json: "[]",
  created_at: 1,
} as Project;
const target = {
  id: 1,
  name: "local",
  kind: "local",
  host: "",
  port: 0,
  user: "",
  key_path: "",
  workroot: "",
  max_concurrent: 2,
  sandbox: 0,
  status: "ok",
  info_json: "{}",
  context_json: "{}",
  memory_dir: "",
  command_prefix: "",
  created_at: 1,
} as Target;
const session = {
  id: 1,
  project_id: 1,
  target_id: 1,
  name: "Main work",
  agent: "claude",
  model: "sonnet",
  workdir: "/repo",
  tmux_session: "lec",
  status: "running",
  origin: "managed",
  pane_tail: "Working output",
  context_pct: 50,
  last_activity_at: 1,
  created_at: 1,
  updated_at: 2,
  ended_at: null,
  archived_at: null,
  group_path: "Work/Core",
  can_restore: false,
  idle_seconds: 3,
  uptime_seconds: 100,
  handoff_in_flight: false,
  wraps: 0,
  project_name: "Deck",
  target_name: "local",
  target_kind: "local",
} as SessionView;
const calls: unknown[] = [];
Object.assign(window, { calls });
const api: SessionsApi = {
  sessions: async () => [session],
  request: (async (p: string, o?: unknown) => {
    calls.push([p, o]);
    if (p === "/agents") return [{ name: "claude", builtin: true }];
    if (p === "/launch-profiles") return [];
    if (p === "/models") return { claude: ["sonnet"] };
    if (p === "/sessions/discover")
      return [
        {
          target_id: 1,
          tmux_session: "outside",
          agent: "codex",
          model: "",
          workdir: "/tmp",
        },
      ];
    if (p === "/sessions") return session;
    if (p.endsWith("/reader"))
      return { session, text: "Live terminal text", ended: false };
    if (p.includes("/conversations/"))
      return {
        conversation:{agent:"claude"},
        messages: [{ role: "assistant", text: "Saved answer" }],
        before: null,
      };
    if (p.endsWith("/conversations"))
      return {
        conversations: [{ id: "abc", modified: 1, title: "Saved" }],
        current: { id: "abc", state: "identified", saved: true },
        fork_supported: true,
        resume_supported: true,
      };
    if (p === "/conversation-search")
      return {
        id: "job",
        done: true,
        complete: true,
        scopes: [],
        results: [
          {
            id: "hit",
            title: "Found",
            target: "local",
            agent: "claude",
            cwd: "/repo",
            snippet: "needle",
          },
        ],
      };
    if (p.includes("/results/hit"))
      return {
        messages: [{ role: "assistant", text: "Search answer", matched: true }],
        before: null,
        after: null,
        fork_options: [],
        index_complete:true,
      };
    if (p.endsWith("/terminal")) return { url: "/terminal/session/1" };
    return {};
  }) as SessionsApi["request"],
};
createRoot(document.getElementById("root")!).render(
  <Sessions
    api={api}
    projects={[project]}
    targets={[target]}
    onOpenTerminal={(u) => calls.push(["terminal", u])}
    onReview={() => {}}
    onNotice={() => {}}
  />,
);
