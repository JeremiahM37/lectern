import React from "react";
import { createRoot } from "react-dom/client";
import { Settings, type SettingsApi } from "./Settings";
const calls: unknown[] = [];
Object.assign(window, { calls });
const target = {
  id: 1,
  name: "local",
  kind: "local",
  host: "",
  port: 0,
  user: "admin",
  key_path: "",
  workroot: "",
  max_concurrent: 2,
  sandbox: 0,
  status: "online",
  info_json: "{}",
  context_json: "{}",
  memory_dir: "",
  command_prefix: "",
  created_at: 1,
};
const project = {
  id: 1,
  name: "Deck",
  target_id: 1,
  target_name: "local",
  repo_path: "/repo",
  setup_cmd: "make setup",
  default_base_branch: "main",
  workroot_override: "",
  policy_json: "{}",
  verify_cmd: "go test ./...",
  keep_worktrees: 0,
  review_gate: 0,
  env_json: "{}",
  context_json: "{}",
  strict_mcp: 0,
  permissions_json: "{}",
  gate_matcher: "",
  default_agent: "claude",
  capability_profile: "parity",
  default_permission_mode: "acceptEdits",
  skill_sources_json: "[]",
  created_at: 1,
};
const api: SettingsApi = {
  request: (async (p: string, o?: unknown) => {
    calls.push([p, o]);
    if (p === "/targets") return [target];
    if (p === "/projects") return [project];
    if (p === "/settings")
      return {
        discord_webhook: "",
        ntfy_server: "https://ntfy.sh",
        ntfy_topic: "deck",
      };
    if (p === "/projects/usage")
      return [
        {
          project_id: 1,
          tasks: 3,
          open_tasks: 1,
          sessions: 2,
          last_active_at: 1,
        },
      ];
    if (p === "/projects/1/mcp")
      return {
        mcp: { server: { command: "run" } },
        revision: "r1",
        strict_mcp: false,
      };
    if (p === "/projects/1/capability")
      return {
        profile: "parity",
        allow: ["Bash"],
        mcp_servers: ["server"],
        memory_dir: "/memory",
      };
    if (p.startsWith("/skills?"))
      return {
        skills: [{ id: "skill-a", name: "Skill A", description: "Useful" }],
      };
    if (p.startsWith("/projects/1/skills?")) return { attachments: [] };
    if (p.startsWith("/projects/1/workflows?"))
      return {
        workflows: [
          {
            id: "spec-kit",
            name: "Spec Kit",
            description: "Specification-first development with constitution, planning, tasks, and implementation workflows.",
            version: "d848fb4e18f44640ad6b42e60a280551ee90cdce",
            upstream_url: "https://github.com/github/spec-kit",
            enabled: false,
            commands: ["lectern-spec-kit constitution", "lectern-spec-kit specify", "lectern-spec-kit clarify", "lectern-spec-kit plan", "lectern-spec-kit tasks", "lectern-spec-kit analyze", "lectern-spec-kit checklist", "lectern-spec-kit implement", "lectern-spec-kit converge"],
          },
          {
            id: "maestro",
            name: "Maestro",
            description: "Curated agent workflow guidance for diagnosing, fortifying, refining, reflecting, and teaching Maestro.",
            version: "00f9115d446a8ba26b8f18f6ed306bc4a21807c3",
            upstream_url: "https://github.com/sharpdeveye/maestro",
            enabled: false,
            commands: ["lectern-maestro diagnose", "lectern-maestro fortify", "lectern-maestro refine", "lectern-maestro reflect", "lectern-maestro agent-workflow", "lectern-maestro teach-maestro"],
          },
        ],
        reload_required: true,
      };
    if (p === "/agents")
      return [
        { name: "claude", command: "claude", builtin: true },
        {
          name: "runner-secret",
          command: "runner",
          env: { API_KEY: { __lectern_retained: "token" } },
        },
      ];
    if (p === "/launch-profiles")
      return [
        {
          id: 1,
          name: "Review",
          agent: "claude",
          command: "",
          model: "sonnet",
          env_json: "{}",
        },
      ];
    if (p === "/stats")
      return { total_cost_usd: 2, last_7d_usd: 1, tasks_done: 4 };
    if (p === "/delegation")
      return { settings: { enabled: false, worker_agent: "", worker_model: "", permission_mode: "acceptEdits", correction_cycles: 1 }, worker_ready: false, worker_problem: "no worker agent chosen" };
    if (p === "/health")
      return {
        version: "v1",
        build: { revision: "abcdef123456", modified: false },
      };
    if (p.startsWith("/projects/import/scan"))
      return [{ name: "Found repo", path: "/src/found", registered: false }];
    if (p === "/push/subscriptions") return pushDevices;
    if (p === "/push/subscribe" && (o as { method?: string } | undefined)?.method === "DELETE") {
      const endpoint = ((o as { body?: { endpoint?: string } }).body || {}).endpoint;
      pushDevices = pushDevices.filter((d) => d.endpoint !== endpoint);
      return { ok: true };
    }
    return {};
  }) as SettingsApi["request"],
};
// ?push=unavailable | ?push=ios | ?push=subscribed drive the three other
// scenarios the e2e suite exercises against this fixture (real
// subscribe/getSubscription is not reliably scriptable headless — see
// frontend/e2e coverage notes in e2e/test_react_settings.py). Default is
// "available, not yet subscribed".
const pushMode = new URLSearchParams(location.search).get("push");
let pushDevices: { id: number; endpoint: string; created_at: number }[] =
  pushMode === "subscribed"
    ? [{ id: 1, endpoint: "https://push.example/this-device", created_at: 1 }]
    : [{ id: 2, endpoint: "https://push.example/other-device", created_at: 1 }];
createRoot(document.getElementById("root")!).render(
  <Settings
    api={api}
    onNotice={() => {}}
    onEnablePush={() => {
      calls.push(["push"]);
      pushDevices = [...pushDevices, { id: 1, endpoint: "https://push.example/this-device", created_at: 2 }];
    }}
    pushAvailable={pushMode !== "unavailable" && pushMode !== "ios"}
    pushUnavailableReason={
      pushMode === "unavailable"
        ? "Open Lectern over https to enable alerts."
        : pushMode === "ios"
          ? "On iPhone/iPad, add Lectern to the Home Screen first (Share → Add to Home Screen), then enable alerts from there."
          : undefined
    }
    pushUnavailableReasonKind={
      pushMode === "unavailable" ? "insecure" : pushMode === "ios" ? "ios-not-installed" : undefined
    }
    pushEndpoint={pushMode === "subscribed" ? "https://push.example/this-device" : null}
    onUnsubscribePush={(endpoint) => {
      calls.push(["unsubscribe", endpoint]);
      pushDevices = pushDevices.filter((d) => d.endpoint !== endpoint);
    }}
  />,
);
