# Workspace layout

Desktop uses a fixed sidebar; phones use a bottom bar. Both show the same four
pages — **Sessions** (home), **Approvals** (with the count waiting), **Tasks**
and **Settings** — plus **Terminals** while a terminal is open, and **More**:
Overview, Issues & PRs, Terminals, Media, Agent tests, Machines and Plugins.
Every page stays deep-linkable, and older links (`#board`, `#deck`, `#targets`,
`#tasks/<project>/…`) are rewritten to the page's new name
(docs/design/simple-ui.md).

Sessions use a searchable grid. Terminal and Chat are primary; More contains
native attachment, handoff, interruption, project assignment and removal.
Opening More or editing a field prevents periodic refresh from replacing the
focused control. Stop tracking releases an adopted process; Find running agents
can discover and track it again.

Settings opens on **Basics** (agents found, ask before risky actions, phone,
alerts, theme, language, Enter in chat); everything else sits in the Advanced
sections beside it — Machines, Projects, Agents, Notifications, Phone & devices,
Appearance, Workspace & terminal, Shortcuts, AI tool connections, Plugins,
Accounts, Budgets, Usage & about — and in settings search. Switching sections
preserves drafts. Arrow keys, Home and End navigate the section tabs. Task
creation stays on Tasks; session creation (Start an agent) stays in Sessions.
Terminal opens an internal terminal tab, and Pop out remains available.

Open in terminal uses the device's default terminal. Linux and Windows setup
instructions live under Tools → Terminal connection setup. The attached terminal and a native terminal can remain open
simultaneously.

Verified layouts: 390px phone and 1440px desktop, live session data, project
forms, secondary menus, terminal controls, keyboard navigation and draft
retention. Regression flows live in e2e/test_workspace_ui.py, alongside the
existing task, routine, approval, terminal-tab and scrolling suites.
