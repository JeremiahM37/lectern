# How Lectern compares

> Every cell is checked against that product's own README, docs or source, with the source listed under Evidence below.
> "?" means it couldn't be verified, not that the feature is missing. Corrections are welcome: open an issue with a link.

Legend: ✓ = yes (verified) · **partial** = partially, qualifier follows · ✗ = no (positively established) · ? = could not verify (never treated as "no")

Sources current as of 2026-09-26. Lectern verified against `/mnt/bulk/lectern-connect-ui` (README.md + docs/*.md + internal/* source). All other products verified via `gh api`/`gh search`/`gh issue list` against their own repos, or WebFetch against their own docs sites. Every non-"?" cell is cited in the Evidence list below by `[row-product]`.

| # | Row | Lectern | Happy | Claude Squad | Vibe Kanban | agent-deck | Conductor | Claude Code (web/app) | Codex (cloud/app/CLI) |
|---|---|---|---|---|---|---|---|---|---|
| 1 | Start/message session from an ordinary **claude.ai** chat (MCP remote) | ✓ | ✗ | ✗ | ✗ | ✗ | ✓ hosted MCP | ? no such feature described | ✗ n/a (OpenAI product) |
| 2 | Same, from **ChatGPT** | **partial** — connector works, untested e2e | ✗ | ✗ | ✗ | ✗ | ✓ named explicitly | ✗ no cross-vendor link | ✓ native (Codex tab/cloud) |
| 3 | Chat-uploaded file (PDF) handed over as the real file | ✓ | **partial** — images shipped, general files still roadmap | ✗ no chat surface | ? | ? no chat surface | ? | ? unconfirmed for cloud sessions | ? unconfirmed for cloud tasks |
| 4 | Terminal dashboard across machines; right-click new window; multi-select | ✓ | ? | **partial/✗** single machine, issue open | ✗ single workspace terminal | **partial** live view, no right-click/multi-select | ✗ single embedded terminal | ✗ explicitly single-machine, no multi-open | ✗ not described |
| 5 | One-command wrapper in any folder (`lectern claude`) | ✓ | ✓ `happy claude`/`happy codex` | ✓ `cs` | ✓ `npx vibe-kanban` | ✗ needs verb+flag | ✗ GUI app only | ✓ `claude` (local only) | ✓ `codex` (local only) |
| 6 | Breadth of agents (built-in + catalog + custom) | 3 built-in + 13 catalog + unlimited custom | ≥5 named + open ACP catalog | 4 named + custom cmd | 10 named | 13 named + custom | 4 named | 1 (Claude family only) | **partial** any Chat-Completions/Responses-API model, not a CLI catalog |
| 7 | Switch a live session to a different agent, keeping context | ✓ | **partial** — marketing claim, not shown working | ✗ fixed at creation | ✗ fresh execution, no inherited context | ✗ same-tool only | ? | **partial** — model switch, not cross-agent | **partial** — model switch, not cross-agent |
| 8 | Recently closed sessions restore with conversation | ✓ | ✓ | **partial** pause/resume only, kill = gone | **partial** archive yes, delete = gone | ✓ | ✓ | **partial** archived restore, deleted = gone | ✓ resume + search local chats |
| 9 | Adopts/discovers sessions started outside the tool | ✓ | **partial** hook-based scanner, gaps noted | ✗ | ? | ? (adopts external "conductor" setups only) | ? | ✗ explicit opt-in only | ✗ one-time import, not live adoption |
| 10 | Runs agents on remote machines: SSH / containers / Proxmox | ✓ SSH, Proxmox `pct`, sandbox | ✗ install-per-machine only, remote provisioning is roadmap | ✗ confirmed local-only | ✗ SSH only for editor links | **partial** SSH + Docker, no Proxmox | **partial** vendor microVM cloud only, Mac client | **partial** Anthropic/org-hosted sandbox, not your SSH box | **partial** OpenAI-hosted container or local only |
| 11 | Phone control: installable app/PWA; approve/deny + reply from push | ✓ | **partial** native apps + push, no confirmed approve/deny/reply | ✗ | ✗ native OS notifications only, no PWA (open issue) | **partial** bots + PWA, no notification actions | **partial** mobile app, no push approve/deny documented | **partial** native apps + push, no reply-from-notification confirmed | **partial** push exists per bug reports, reply unconfirmed |
| 12 | Graduated approvals (once / session / deny+feedback) | ✓ all three | **partial** modes + remembered grants, no deny-with-feedback | ✗ blanket autoyes only | **partial/✗** binary approve/request-changes only | **partial** quick-approve + YOLO, no granularity | **partial** approval gate, no granularity | **partial** 2 levels, no deny+feedback | **partial** 3 modes, no deny+feedback confirmed |
| 13 | Chat view renders tool calls as cards with diffs | ✓ | ✓ | ✗ diff tab only | ✓ | ? | ✗ separate panels | **partial** diff view, not per-call cards | **partial** diff review, not confirmed as cards |
| 14 | Voice mode (talk + spoken approvals); paid vs free | ✓ free, Web Speech API | **partial** ElevenLabs, 20min free/30d then paid | ✗ | ? | ? | ? | ✗ dictation only, excluded from cloud, no spoken approvals | ✓ ChatGPT Voice (subscription tier unconfirmed) |
| 15 | Works without VPN: relay/pairing; E2E vs just paired | ✓ pairing, explicitly not E2E | ✓ E2E-encrypted relay | n/a local binary | **partial** pairing relay, no E2E claim | **partial** bots + token, no relay/E2E | ? | n/a hosted web service | n/a hosted web service |
| 16 | Task board/kanban: isolated worktrees + diff review + PR creation | ✓ all three | **partial** worktree+diff yes, no kanban, no PR creation found | **partial** worktree+diff yes, PR creation not automatic (`gh pr create` never called) | ✓ all three (project sunsetting) | ✗ kanban only via 3rd-party tool | **partial** worktree+PR yes, no kanban board | ✓ thread board + branch + diff + PR | **partial** worktree+PR yes, no kanban UI documented |
| 17 | Best-of-N with a judge / delegated builds (worker+lead) | ✓ | ✗ roadmap only | ✗ | ? | ? | ? | **partial** reviewer-fleet/team-lead, different mechanism | ? |
| 18 | Evals on your own repo (replay from merged PRs) | ✓ | ? | ✗ | ? | ? (roadmap idea only) | ✗ (Checks ≠ eval runner) | ✗ plugin-eval only, not repo-wide | ? |
| 19 | Cross-agent awareness / claim board | ✓ | ✗ multiplayer = human collab, not agent peer-awareness | ✗ by design (isolates) | ? | ? | ✗ isolation is the point | **partial** opt-in cross-session messaging | ? internal component only, not documented user feature |
| 20 | Persistent project memory integration | ✓ Grimoire | ✗ roadmap only | ✗ | ? | **partial** cross-session recall, not project KB | ✗ explicitly not memory | ✓ CLAUDE.md/AGENTS.md/MEMORY.md | **partial** AGENTS.md, explicitly not "memory" |
| 21 | Usage/quota/budget tracking (caps, alerts) | ✓ | **partial** voice-only caps, cost tracking roadmap | ✗ (feature request closed, unimplemented) | ? | ✓ full dashboard + provider quota | ✗ ("not right now") | ✓ | **partial** dashboard only, no caps (confirmed via open issues) |
| 22 | Self-hosted & license | ✓ MIT | ✓ MIT | ✓ AGPL-3.0 | ✓ Apache-2.0 (sunsetting as a company) | ✓ MIT | ✗ proprietary, no self-host | ✗ proprietary SaaS | **partial** CLI Apache-2.0 OSS; Cloud OpenAI-hosted only |
| 23 | MCP server exposing the tool itself to other agents | ✓ | **partial** internal bridge only, not external-facing | ✗ (consumes MCP, doesn't expose) | ✓ (local-only, not internet-reachable) | ✓ (`recall mcp` + catalog) | ✓ hosted, ~20 tools | ✓ `claude mcp serve` | ✗ client-only, no server mode |

## Evidence

**Lectern** (source of truth `/mnt/bulk/lectern-connect-ui`):
1. `docs/use-from-chat.md`: "Brainstorm in claude.ai... say 'start a Lectern session that builds this'"; `docs/sessions-mcp.md` documents `start_session`/`send_to_session`/`end_session` MCP tools.
2. `docs/use-from-chat.md`: "Status: ChatGPT is supported by the connector but **not yet tested end to end**. claude.ai is."
3. `docs/use-from-chat.md`: "Files you upload to the chat, including PDFs and images, are handed over as the real file."
4. `docs/terminal-client.md`: "o / right-click | Open selected session in a new terminal window"; "b | Select multiple sessions: click/Space marks them, Enter opens all selected."
5. README: `lectern claude` / `lectern codex` "in any folder."
6. `docs/agents.md`: 3 first-class/experimental built-ins (claude/codex/gemini table) + catalog presets "Gemini CLI's ACP mode, OpenCode, Aider, Goose, Amp, Cursor Agent CLI, GitHub Copilot CLI, Qwen Code, Crush, Kimi Code CLI, Cline CLI, plus the Zed ACP adapters for Claude Code and Codex" (13) + "Custom command…" unlimited.
7. `docs/mobile-sessions.md` "Switch agent or model": "Switching saves a fresh handoff and starts the destination with that context. It keeps the original session running."
8. README: "Recently closed brings back a session you closed by mistake, with its conversation."
9. README: "Lectern finds and adopts Claude and Codex sessions you started outside it"; `docs/terminal-client.md` "Promote conversation."
10. README: "Runs anywhere: your laptop, anything reachable over SSH, a Proxmox container, or an ephemeral sandbox."
11. `docs/mobile-sessions.md`: push notifications with "Open terminal" and "Reply" actions; approvals "from the app or straight from the notification."
12. `docs/mobile-sessions.md`: "Allow once... Allow for this session... Deny with feedback opens a note that is sent back to the agent."
13. `docs/mobile-sessions.md` "Chat cards and graduated approvals": per-tool-type card rendering with real diffs for Read/Edit/Write/MultiEdit.
14. `docs/mobile-sessions.md` "Voice mode": "no vendor keys, no metered API," spoken approvals via Web Speech API.
15. `docs/remote-access.md`: device pairing over a public tunnel; explicitly "Full E2E encryption would protect against a threat (a malicious relay) that does not exist in this deployment."
16. `docs/DELEGATED_BUILDS.md` (worktree + diff); `internal/triggers/gitops.go`: `gh pr create --head ... --title ... --body ...`; `internal/api/session_review.go`: "Review and merge: live diffs, commit/push/PR."
17. `docs/evals.md`: "the same Best-of-N machinery... `POST /api/tasks/{id}/dispatch`'s `variants` array"; `docs/replay-evals.md`: "the same judge machinery Best-of-N uses (`judge_agent`/`judge_model`)"; `docs/DELEGATED_BUILDS.md`: worker+lead review workflow.
18. `docs/replay-evals.md`: suite built from a project's own merged-PR history, graded against the PR's accepted diff.
19. `docs/agent-events.md` §5 "Cross-agent awareness"; `docs/claims.md`: claim board, advisory PreToolUse warnings.
20. README: "Memory: pairs with Grimoire, so a new session starts with the project's knowledge."
21. `docs/budgets.md`: daily/weekly/per-agent/per-task USD caps, Claude quota alerts, cost-anomaly detection.
22. `LICENSE`: MIT, Jeremiah Mackey.
23. `docs/web-connector.md`: `lectern mcp --http` serves the MCP tool surface over OAuth 2.1 to claude.ai/ChatGPT.

**Happy** (slopus/happy + happy-cli/happy-desktop/happy-agent):
1–2. `packages/happy-app/.../oauth.ts`, `authenticateClaude.ts`/`authenticateCodex.ts`: OAuth runs *into* Claude/ChatGPT accounts, not a reverse connector; no MCP-remote-connector doc found.
3. CHANGELOG: image paste shipped; `docs/roadmap.md` Table Stakes: "Attachments in composer / in agent output [hard, encrypted attachments, extra storage - needs design]."
5. README: "Instead of `claude`, use: `happy claude` or `happy codex`."
6. `packages/happy-cli/README.md`: Claude Code, Codex, agy (Antigravity), Gemini (deprecated), OpenClaw, plus `happy acp <cli>` for any ACP agent.
7. happy-desktop README: "Let Claude plan, Codex build, and Grok review... the session, the permissions, and the context stay together across every handoff" (marketing copy, not shown executing).
8. CLI README: `happy resume <id>`; CHANGELOG: "Sessions can now be branched or rewound," 2-month retention.
9. `docs/session-protocol-claude.md`: hook-based `SessionScanner`; CHANGELOG bugfix "`claude --resume` not finding Happy sessions" implies known gaps.
10. `docs/roadmap.md` Integrations: "remote machine ecosystem — exe.dev, sprites" listed as unbuilt.
11. App Store/Play links in README; `happy notify`; `docs/roadmap.md` "Push Notification Routing... All registered devices get all notifications — no routing intelligence."
12. `docs/permission-resolution.md`: modes `default|acceptEdits|bypassPermissions|plan|read-only|safe-yolo|yolo`; CHANGELOG: "'Don't ask again' sticks."
13. CHANGELOG: "Tool calls show as one-line rows you can open for details"; "New diff viewer with syntax highlighting."
14. `packages/happy-server/.../voiceRoutes.ts`: `VOICE_FREE_LIMIT_SECONDS=1200`, `VOICE_HARD_LIMIT_SECONDS=18000`; `PRIVACY.md`: ElevenLabs SDK.
15. README: "End-to-end encrypted - Your code never leaves your devices unencrypted"; default relay `api.cluster-fluster.com`.
16. Worktree code (`useWorktrees.ts`) + diff viewer confirmed; no kanban/PR-creation code found; `docs/competition/comparison-matrix.md` shows worktree-per-task orchestration was only being *studied* (Superset), not shipped.
17. `docs/roadmap.md` "Viral/Cool": "Multi-agent dispatch — Fan-out N agents... Orchestration UI (progress, results, cost)" — future work.
19. happy-desktop README "Natively multiplayer" describes human-teammate collaboration, not agent-to-agent.
20. `docs/roadmap.md`: "Memory viewing / editing" under nice-to-have, unbuilt.
21. Only voice has enforced limits (#14); general cost tracking is roadmap-only.
22. README: "License — MIT License."
23. `packages/happy-cli/bin/happy-mcp.mjs`, `.mcp.json` (`127.0.0.1:29979/mcp`): internal local bridge for Codex tool-call plumbing, not a documented external-facing server.

**Claude Squad** (smtg-ai/claude-squad):
1–3. README: local terminal app (tmux+worktrees); no chat/web surface anywhere in code or docs.
4. README Menu section (single-machine keymap); open issue #204 "ssh drop in" requests exactly this, unresolved.
5. README Installation: `cs`.
6. README: "manages multiple Claude Code, Codex, Gemini CLI (and other local agents including Aider)"; `-p`/profiles accepts any shell command.
7. Issue #62 "Independent Agent per Session": confirms one agent fixed per session.
8. Issue #103 comment: pause/resume "available in v1.0.10"; no restore path found for killed (`D`) sessions.
9. README Menu: only `n`/`N` create-new; no adopt-external command.
10. README "How It Works": tmux + git worktrees, same machine; issue #204 unresolved.
11. `web/src/app/page.tsx`: static marketing site only, no PWA/push code.
12. README Usage: blanket `-y/--autoyes` flag only.
13. README Menu: "Switch between preview tab and diff tab" (plain diff, not cards).
16. `session/git/worktree_ops.go` `Setup()` (worktree); diff tab (review); `session/git/worktree_git.go` `PushChanges()`/`OpenBranchURL()` — pushes and opens a browser URL but never calls `gh pr create`.
19. README Highlights: "Each task gets its own isolated git workspace, so no conflicts" — explicit isolation, not shared awareness.
21. Issue #129 "Feature Request: Monitoring and Usage Tracking Framework" — closed, unimplemented.
22. `gh api repos/smtg-ai/claude-squad/license` → `spdx_id: "AGPL-3.0"`.
23. `session/tmux/tmux.go`: only auto-accepts Claude Code's own "trust MCP server" prompt (consumes, doesn't expose MCP).

**Vibe Kanban** (BloopAI/vibe-kanban):
Sunsetting: README h1 "Vibe Kanban is sunsetting"; blog: "the vast majority are free users and we couldn't find a business model"; local workspaces continue, becomes community-maintained OSS.
1–2. `docs/integrations/vibe-kanban-mcp-server.mdx`: "local-only... cannot be accessed via publicly accessible URLs."
3. `docs/workspaces/chat-interface.mdx`: only documents image attachment.
4. `docs/workspaces/interface.mdx`: one expandable terminal per workspace, "exclusive to the Workspaces UI," not cross-machine.
5. README: `npx vibe-kanban`.
6. README: "Switch between 10+ coding agents — Claude Code, Codex, Gemini CLI, GitHub Copilot, Amp, Cursor, OpenCode, Droid, CCR, and Qwen Code"; `docs/supported-coding-agents.mdx` confirms the exact 10.
7. `docs/workspaces/sessions.mdx`: "New sessions don't inherit conversation history."
8. `docs/workspaces/managing-workspaces.mdx`: Archive "preserves everything," Delete is permanent.
10. README "Remote Deployment": SSH only generates `vscode://vscode-remote/...` editor links.
11. Open issue #2882 "Feature Request: Web Push Notifications for Headless/Cloud Deployments"; open issue #2536 "[Feature Request] Plan for PWA support?" — both confirm absence.
12. `docs/workspaces/chat-interface.mdx` "Approval Workflow": Approve / Request Changes only, or `dangerously_skip_permissions` to bypass entirely.
13. Same doc: bash tool cards with exit code; green/red diff cards in Changes panel.
15. `docs/remote-access.mdx`: pairing-code relay via cloud.vibekanban.com, no E2E claim found.
16. `docs/workspaces/managing-workspaces.mdx`: "Each workspace creates a git worktree"; `docs/workspaces/git-operations.mdx`: "Creating Pull Requests" with AI-generated description.
22. `LICENSE`: Apache-2.0; `docs/self-hosting/deploy-docker.mdx`.
23. `docs/integrations/vibe-kanban-mcp-server.mdx`: exposes a local MCP server with org/project/issue/workspace tools.

**agent-deck** (asheshgoplani/agent-deck):
1–2. README: access surfaces are TUI/CLI/Web UI + Telegram/Slack bots; "ChatGPT" hits are only Codex model-display labels (`internal/web/static/app/pickerTools.js`).
4. `docs/COMMAND-CENTER.md`: live tmux-over-WebSocket session view; no right-click/multi-select found.
5. README Quick Start: `agent-deck add . -c claude` / `agent-deck try "task"` (verb+flag required, not a one-word wrapper).
6. README "Multi-Tool Support" table: 13 tools (Claude Code, Gemini CLI, OpenCode, Codex, Copilot, Crush, Muse Code, Cursor, Hermes Agent, pi, DeepSeek Harness, Oh My Pi) + `[tools.*]` custom config.
8. README "Archive Sessions": conversation/metadata/worktree preserved; `Shift+U`/`R` restore.
10. README "Remote Instances" (SSH) and "Docker Sandbox" both confirmed; zero hits for "Proxmox."
11. README "Conductor" (Telegram/Slack bots); `internal/web/static/sw.js` `notificationclick` handler only focuses/navigates — no actions, no approve/deny/reply.
12. Checklist item CAP-TUI-020: quick-approve (`a`) + "YOLO" bypass toggle; no once/session/deny-feedback granularity.
15. Web UI binds loopback by default with bearer-token requirement for non-loopback; no relay/pairing service or E2E claim found.
16. `hermes kanban list/create` shells out to the separate third-party Hermes Agent CLI — not agent-deck-native.
18. `docs/ROADMAP.md`: P2 idea, "a per-session event journal... to give evals something real to read" — unbuilt.
20. `skills/agent-deck/SKILL.md`: `recall` subsystem indexes past transcripts, `recall context <session> --into current` (cross-session recall, not a project KB).
21. README "Cost Tracking Dashboard" (daily/weekly/monthly/per-group/session limits, 80/100% thresholds) and "Provider Quota" (`agent-deck usage`).
22. `gh api repos/asheshgoplani/agent-deck/license` → MIT; `LICENSE`: "Copyright (c) 2025 Ashesh Goplani."
23. `skills/agent-deck/SKILL.md`: `recall mcp` built-in server, `agent-deck mcp attach <session> recall`.

**Conductor** (conductor.build docs):
1–2. `docs/api/mcp`: "Conductor's hosted Model Context Protocol (MCP) server lets ChatGPT, Claude, Codex, and other MCP clients manage your cloud workspaces."
4. `docs/reference/big-terminal-mode`: single embedded terminal per workspace.
5. `docs/installation`: macOS app download/install only, no CLI wrapper.
6. `docs/guides/providers` / `docs/reference/harnesses`: Claude Code, Codex, Cursor, OpenCode.
8. `docs/concepts/workflow`: "restore an archived workspace later... including its chat history."
10. `docs/installation`: "Conductor is available for macOS"; `docs/cloud/cloud-computer`: cloud runs in vendor "isolated microVM... Amazon Linux 2023."
11. `pricing`: "Conductor mobile app" listed as a Pro-tier feature; no push approve/deny/reply documented.
12. `docs/reference/security-and-permissions`: approval gate for shell/file/MCP/fetch tool calls, no once/session granularity documented.
13. `docs/guides/review-and-merge`: Diff Viewer and Checks tab are separate panels, not chat cards.
16. `docs/concepts/git-worktrees`: "Conductor creates a Git worktree for that workspace"; `docs/guides/review-and-merge`: "Use Create PR... draft a pull request description"; `docs/concepts/workspaces-and-branches` frames a workspace as one task, not a kanban board.
18. `docs/reference/checks`: merge-readiness aggregator (CI, PR metadata, comments), not an eval runner.
19. `docs/concepts/git-worktrees`: isolation framed as the point ("belong to one task instead of... another agent's task").
20. `docs/reference/conductor-json`: manages only "scripts"/"runScriptMode" — explicitly not a memory mechanism.
21. `pricing`: "Not right now, but we plan to introduce usage-based pricing for cloud compute in the future."
22. `pricing`: "Self-Hosting: Not currently available"; tiers Free/$50/$60/Enterprise; no public source repo found.
23. `docs/api/mcp`: hosted MCP server at `api.conductor.build/mcp`, ~20 tools.

**Claude Code (web/app/mobile)** (code.claude.com/docs):
1. No page describes ordinary claude.ai chat offering "start a coding session" (only the separate claude.ai/code surface and Projects).
2. `code.claude.com/docs/en/overview`: Anthropic-only ecosystem, no ChatGPT/OpenAI integration mentioned.
3. `code.claude.com/docs/en/mobile`: "Claude Code downloads other files to your machine and passes them to Claude as `@` file references" (Remote Control, not confirmed for cloud sessions).
4. `code.claude.com/docs/en/agent-view`: "a single-screen dashboard for managing multiple Claude Code sessions on your local machine, not across machines... Sessions can only be opened in the same terminal."
5. `code.claude.com/docs/en/overview`: `claude` in any directory (local only).
6. `code.claude.com/docs/en/permission-modes`: "Claude Opus 4.6..., Sonnet 4.6..., or a Fable model" — Claude/Anthropic family only.
7. `code.claude.com/docs/en/remote-control`: "When you pick a model from a connected device, Claude Code runs the session on that model" — model switch, not cross-agent.
8. `code.claude.com/docs/en/claude-code-on-the-web`: "Reopen the session... to provision a fresh VM with your conversation history restored"; deleted sessions unrecoverable.
9. `code.claude.com/docs/en/remote-control`: "Remote Control only activates when you explicitly run `claude remote-control`... unless auto-connect is turned on."
10. `code.claude.com/docs/en/claude-code-on-the-web`: "By default it runs on infrastructure Anthropic manages, or on your organization's self-hosted environment when routed there."
11. `code.claude.com/docs/en/mobile`: native iOS/Android app; `code.claude.com/docs/en/remote-control`: "Push when actions required" toggle. No reply-from-notification documented.
12. `code.claude.com/docs/en/claude-projects`: "Each approval covers that prompt, or the rest of that thread if you choose the broader option" — 2 levels, no deny+feedback.
13. `code.claude.com/docs/en/web-quickstart`: "A diff indicator shows lines added and removed... Select it to open the diff view" (not per-call cards).
14. `code.claude.com/docs/en/voice-dictation`: "voice dictation does not work in cloud sessions or SSH sessions"; no spoken-response feature found.
15. `code.claude.com/docs/en/remote-control`: "Your local Claude Code session makes outbound HTTPS requests only and never opens inbound ports" — plain hosted service, not pairing.
16. `code.claude.com/docs/en/claude-projects`: "Each thread... works on its own branch and opens a pull request."
17. `code.claude.com/docs/en/ultrareview`: "a fleet of reviewer agents... every reported finding is independently reproduced and verified" — verification of one build, not best-of-N judging of alternatives.
18. `code.claude.com/docs/en/plugin-evals`: only plugin/skill eval, not repo-wide merged-PR replay.
19. `code.claude.com/docs/en/cross-session-messaging`: opt-in messaging between sessions, not automatic claim detection.
20. `code.claude.com/docs/en/memory`: CLAUDE.md, AGENTS.md, auto `MEMORY.md`.
21. `code.claude.com/docs/en/costs`: `/usage`, spend limits, usage credits, org spend caps.
22. `code.claude.com/docs/en/self-hosted-environments`: "The control plane remains Anthropic-hosted" even when execution is self-hosted — proprietary SaaS.
23. `claude mcp serve` (Claude Code itself as an MCP server), per code.claude.com docs search.

**Codex (cloud/app/CLI)** (developers.openai.com/codex, learn.chatgpt.com/codex/*, github.com/openai/codex):
2. `learn.chatgpt.com/codex/cloud`: "choose your environment, and describe the result you want... watch the task logs or let the task run in the background."
3. `learn.chatgpt.com/codex/projects`: only folder-level "Add local project"; open issue openai/codex#38821 notes connector/file-tool gaps for cloud tasks.
4. `learn.chatgpt.com/codex/app`: "Keep parallel work visible and move between chats quickly" — no right-click/multi-select documented.
5. github.com/openai/codex README: "simply run `codex` to get started," runs "locally on your computer."
6. `learn.chatgpt.com/codex/models`: "You can also point Codex at any model and provider that supports either the Chat Completions or Responses APIs."
7. `learn.chatgpt.com/codex/cli`: `/model` command switches model/reasoning effort mid-session.
8. `learn.chatgpt.com/codex/cli`: `codex resume` — "reopen a recent chat... or search across local chats."
9. `learn.chatgpt.com/codex/import`: imports "chat sessions from the last 30 days... up to 50 chats" from Claude Code/Cursor — one-time migration, not live adoption.
10. `learn.chatgpt.com/codex/environments/cloud-environment`: "Codex creates a container and checks out your repo" (cloud) vs. README "locally on your computer" (CLI).
11. Open issues openai/codex#32908 ("push notifications not delivered on iOS"), #33300 (delivery reliability) confirm the feature exists but reply-from-notification is unconfirmed.
12. `learn.chatgpt.com/codex/permission-modes`: "Ask for approval," "Approve for me," "Full access," plus `config.toml` — "deny with feedback" not verbatim-confirmed.
13. `learn.chatgpt.com/codex/cloud`: "Review the summary and diff"; `learn.chatgpt.com/codex/ide`: "Inspect the two affected files" — diff review, not confirmed as discrete cards.
14. `learn.chatgpt.com/codex/use-chatgpt`: "use ChatGPT Voice to start work, check progress, or change direction."
15. `learn.chatgpt.com/codex/remote`: relays through ChatGPT to "your connected computer" — no VPN/tunnel documented.
16. `learn.chatgpt.com/codex/cloud`/`cloud-environment`: container + repo checkout, "open a pull request when the work is ready"; worktree issues (#48328, #47848) confirm per-task isolation; no kanban UI documented.
20. `learn.chatgpt.com/codex/agent-configuration/agents-md`: global + project AGENTS.md, closer files override; explicitly "does not include traditional persistent memory features."
21. `learn.chatgpt.com/codex/pricing`: usage dashboard exists; open issues #45536 and #42057 confirm no enforceable spend caps/auto-stop.
22. github.com/openai/codex `LICENSE` + README: "licensed under the Apache-2.0 License" (CLI); no self-host option documented for Cloud.
23. `codex-rs/codex-mcp/src/lib.rs`: only client types (`McpResourceClient`, `McpRuntime`); `learn.chatgpt.com/codex/extend/mcp` covers only `codex mcp add/list/login` (client-side config), no server mode.
