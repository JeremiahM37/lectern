import React from "react";
import { createRoot } from "react-dom/client";
import { Board, type BoardApi } from "./Board";
import type { Project, TaskView } from "../types";
const project = {
  id: 1,
  name: "Lectern",
  target_id: 1,
  target_name: "local",
  target_kind: "local",
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
  default_isolation_json: "{}",
  skill_sources_json: "[]",
  created_at: 1,
} satisfies Project;
function task(id: number, status: string, title: string): TaskView {
  return {
    id,
    project_id: 1,
    title,
    prompt: `Prompt for ${title}`,
    status,
    priority: 2,
    labels_json: "[]",
    labels: [],
    agent: "claude",
    model: "sonnet",
    permission_mode: "acceptEdits",
    base_branch: "main",
    parent_task_id: null,
    created_by: "user",
    created_by_attempt: null,
    created_at: 1,
    updated_at: 1,
    project_name: "Lectern",
    target_name: "local",
    target_host: "localhost",
    target_user: "admin",
    target_kind: "local",
    attempt:
      status === "review"
        ? {
            id: 9,
            n: 2,
            status: "done",
            branch: "lec/2",
            worktree_path: "/tmp/w",
            tmux_session: "",
            started_at: 1,
            finished_at: 2,
            exit_code: 0,
            result: {},
            diff_stat: [],
            verify: {},
            driver: "claude-exec",
          }
        : undefined,
    attempts:
      status === "review"
        ? [
            {
              id: 8,
              n: 1,
              status: "failed",
              model: "haiku",
              exit_code: 1,
              cost_usd: 0.01,
              agent: "claude",
              permission_mode: "acceptEdits",
              driver: "claude-exec",
              started_at: 1,
              finished_at: 3,
              diff_stat: [],
              verify: {},
              input_tokens: 500,
              output_tokens: 200,
            },
            {
              id: 9,
              n: 2,
              status: "done",
              model: "sonnet",
              exit_code: 0,
              cost_usd: 0.02,
              agent: "claude",
              permission_mode: "acceptEdits",
              driver: "claude-exec",
              started_at: 1,
              finished_at: 4,
              diff_stat: [{ path: "app.py", additions: 4, deletions: 1 }],
              verify: { rc: 0 },
              input_tokens: 800,
              output_tokens: 300,
            },
          ]
        : [],
  };
}
let tasks = [
  task(1, "backlog", "Backlog job"),
  task(2, "review", "Review job"),
  ...Array.from({ length: 17 }, (_, i) => task(10 + i, "done", `Done ${i}`)),
];
const calls: unknown[] = [];
(window as unknown as { calls: unknown[] }).calls = calls;
const api: BoardApi = {
  tasks: async () => tasks,
  task: async (id) => tasks.find((t) => t.id === id)!,
  projects: async () => [project],
  createTask: async (body) => {
    // The server labels an orchestrated task; the harness mirrors that contract.
    const t = { ...task(99, "backlog", body.title), ...body, labels: body.orchestrate ? ["orchestrated"] : [] };
    tasks = [t, ...tasks];
    calls.push(["create", body]);
    return t;
  },
  taskAction: async (id, action, body) => {
    calls.push([action, id, body]);
    const t = tasks.find((x) => x.id === id);
    if (t) t.status = action === "complete" ? "done" : "queued";
    return {};
  },
  request: (async (path, opts) => {
    calls.push([path, opts]);
    if (path === "/agents") return [{ name: "claude", builtin: true }, { name: "codex", builtin: true }, { name: "flash-builder", task: {} }];
    if (path === "/delegation")
      return { orchestrate_ready: true, worker_ready: true, settings: { enabled: true, lead_agent: "codex", worker_agent: "flash-builder" } };
    if (path === "/templates")
      return [{ name: "Health", title: "Health check", prompt: "Add health" }];
    if (path.includes("capability"))
      return {
        profile: "full",
        mcp_servers: ["grimoire"],
        memory_dir: "/memory",
      };
    if (path === "/routines")
      return [
        {
          id: 1,
          name: "PR sweep",
          prompt: "Review PRs",
          project_ids: [1],
          schedule: "daily",
          permission_mode: "acceptEdits",
          agent: "claude",
          model: "sonnet",
          enabled: true,
        },
      ];
    if (path.includes("/events"))
      return [
        {
          id: 1,
          attempt_id: 9,
          seq: 1,
          ts: 1,
          type: "text",
          payload: { text: "Implemented safely" },
          attempt_n: 2,
        },
      ];
    if (path.includes("/diff"))
      return {
        attempt_n: 2,
        stats: [{ path: "a.ts", additions: 1, deletions: 1 }],
        files: [{ path: "a.ts", patch: "@@ -1 +1 @@\n-old\n+new" }],
      };
    if (path.endsWith("/run")) return { tasks: [{}], failed: [] };
    return {};
  }) as BoardApi["request"],
};
createRoot(document.getElementById("root")!).render(
  <Board
    api={api}
    onOpenTask={() => {}}
    onChat={(t) => calls.push(["chat", t.id])}
    onNotice={(m, e) => calls.push(["notice", m, e])}
    onOpenSession={(id) => calls.push(["session", id])}
    onOpenTerminal={(u, t) => calls.push(["terminal", u, t])}
  />,
);
