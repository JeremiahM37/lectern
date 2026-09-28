// English strings for this area of the app. Keys are "area.name"; values may
// hold {placeholders}. Every other language has a file of the same name.
const catalog: Record<string, string> = {
  // AgentEditor
  // {label} is the name of an arguments field, e.g. "Fixed arguments".
  "agentSettings.editor.argsNotArray": "{label} must be a JSON array of strings.",
  "agentSettings.editor.envLineFormat": "Environment lines must use KEY=value.",
  // {name} is an environment variable name.
  "agentSettings.editor.envInvalidName": "Invalid environment name {name}.",
  "agentSettings.editor.nameCommandRequired": "Name and command are required.",
  "agentSettings.editor.permissionsNotJson": "Permission arguments must be JSON.",
  "agentSettings.editor.permissionsNotObject": "Permission arguments must be a JSON object.",
  "agentSettings.editor.promptArgsName": "Opening prompt arguments",
  "agentSettings.editor.resumeArgsName": "Resume arguments",
  "agentSettings.editor.resumeIdArgsName": "Resume-by-ID arguments",
  "agentSettings.editor.forkArgsName": "Fork arguments",
  "agentSettings.editor.acpCommandRequired": "ACP command is required.",
  "agentSettings.editor.acpArgsName": "ACP arguments",
  "agentSettings.editor.taskArgsName": "Task arguments",
  "agentSettings.editor.saving": "Saving runner…",
  // {name} is the agent's name.
  "agentSettings.editor.editAgentNamed": "Edit agent {name}",
  "agentSettings.editor.editAgent": "Edit agent",
  "agentSettings.editor.addAgent": "Add agent",
  "agentSettings.editor.close": "Close",
  "agentSettings.editor.cancel": "Cancel",
  "agentSettings.editor.catalog": "Agent catalog",
  "agentSettings.editor.starterTemplate": "Starter template (catalog — one click, then edit anything below)",
  "agentSettings.editor.searchCatalog": "Search agent catalog",
  // {n} is the number of agents in the catalog, or empty while it loads.
  "agentSettings.editor.searchPlaceholder": "Search {n} agents by name, command or vendor",
  "agentSettings.editor.customRunner": "Custom runner",
  "agentSettings.editor.customRunnerHint": "Any other CLI — fill in the fields below",
  // A catalog group heading for agents that fit no other group.
  "agentSettings.editor.groupOther": "Other",
  "agentSettings.editor.alreadyAdded": "already added",
  // {binary} is a program name such as npx.
  "agentSettings.editor.binaryNotDetected": "{binary} not detected on any machine",
  "agentSettings.editor.installedHere": "installed here",
  // A capability chip for something the agent cannot do; {label} is e.g. "Resume".
  "agentSettings.editor.capabilityMissing": "no {label}",
  // {query} is the text the person searched for.
  "agentSettings.editor.noMatch": "No catalog agent matches “{query}”. Custom runner works for any other CLI.",
  "agentSettings.editor.unverifiedFields": "Unverified fields (best guess, check before relying on them):",
  "agentSettings.editor.sessionIds": "Session ids:",
  // {date} is when the preset was checked, {by} what it was checked against, {source} a link or document name.
  "agentSettings.editor.checkedAgainst": "Checked {date} against {by} ({source}).",
  "agentSettings.editor.vendorDocs": "the vendor's docs",
  "agentSettings.editor.itsDocs": "its docs",
  // Followed by the install command.
  "agentSettings.editor.install": "Install:",
  "agentSettings.editor.name": "Name",
  "agentSettings.editor.command": "Command",
  "agentSettings.editor.fixedArgs": "Fixed arguments",
  "agentSettings.editor.modelFlag": "Model flag",
  "agentSettings.editor.providerUrl": "Provider endpoint URL (optional)",
  "agentSettings.editor.promptPositional": "Opening prompt is a positional argument",
  // {prompt}, {id}, {dir} and {bin} in the next labels are literal syntax the person types; keep them unchanged.
  "agentSettings.editor.promptArgs": "Opening prompt arguments (JSON array; {prompt} is the message — for a CLI that takes it through a flag)",
  "agentSettings.editor.resumeArgs": "Resume arguments (resume the CLI's own last conversation)",
  "agentSettings.editor.resumeIdArgs": "Resume-by-ID arguments (JSON array; {id}/{dir} substituted — blank if unsupported)",
  "agentSettings.editor.forkArgs": "Fork arguments (JSON array; {id}/{dir} substituted — blank if unsupported)",
  "agentSettings.editor.modelsCommand": "Model catalog command (optional; {bin} is the resolved binary)",
  "agentSettings.editor.trustCommand": "Trust command (optional; {dir} is the working directory)",
  "agentSettings.editor.yoloArgs": "Yolo arguments",
  "agentSettings.editor.yoloEnv": "Yolo environment (KEY=value lines, set only for yolo launches)",
  "agentSettings.editor.environment": "Environment (KEY=value lines; existing values are masked and retained)",
  "agentSettings.editor.enableTasks": "Enable background tasks for this agent",
  "agentSettings.editor.tasksDisabledByAcp": "Disabled while ACP is enabled below — a background task is either a plain command or an ACP agent, never both.",
  "agentSettings.editor.taskCommand": "Task command",
  "agentSettings.editor.taskArgs": "Task arguments (one per line; JSON array accepted)",
  "agentSettings.editor.promptTemplate": "Prompt template",
  "agentSettings.editor.taskOutput": "Task output",
  "agentSettings.editor.outputPlain": "Plain text",
  "agentSettings.editor.outputJsonl": "JSONL events",
  "agentSettings.editor.permissionArgs": "Permission arguments",
  "agentSettings.editor.useAcp": "Use the Agent Client Protocol (ACP) instead of a task command",
  "agentSettings.editor.acpDisabledByTasks": "Disabled while background tasks are enabled above.",
  // Followed by a link to agentclientprotocol.com.
  "agentSettings.editor.acpPermissions": "Every permission mode is supported automatically — ACP carries its own gated approvals. See",
  "agentSettings.editor.acpCommand": "ACP command",
  "agentSettings.editor.acpArgs": "ACP arguments (one per line; JSON array accepted)",
  "agentSettings.editor.acpEnvironment": "ACP environment (KEY=value lines; existing values are masked and retained)",
  "agentSettings.editor.saveRunner": "Save runner",

  // agentCatalog (capability chips on catalog rows)
  // "Resume" here is a noun: resuming a previous conversation.
  "agentSettings.catalog.chip.resume": "Resume",
  // "Exact resume" is a noun: reopening the very same saved conversation by its id.
  "agentSettings.catalog.chip.exact": "Exact resume",
  // "Fork" here is a noun: branching a conversation.
  "agentSettings.catalog.chip.fork": "Fork",
  "agentSettings.catalog.chip.model": "Model",
  "agentSettings.catalog.chip.yolo": "Auto-approve",
  "agentSettings.catalog.chip.task": "Tasks",
  "agentSettings.catalog.chip.skills": "Skills",
  // {label} is a capability chip name.
  "agentSettings.catalog.supported": "{label} is supported",
  "agentSettings.catalog.unavailable": "{label} isn't available",

  // LaunchProfiles and launchProfileForm
  "agentSettings.profiles.instructionsHelp": "Sent to the agent when a session starts. Resumes and forks keep the original briefing. Team workflows use the chosen agent's available delegation tools.",
  "agentSettings.profiles.envInvalidJson": "Environment must be valid JSON.",
  "agentSettings.profiles.envNotObject": "Environment must be a JSON object with string values.",
  // {name} is the starter profile's name.
  "agentSettings.profiles.starterLoaded": "“{name}” starter loaded as an unsaved draft.",
  "agentSettings.profiles.nameRequired": "Name is required.",
  "agentSettings.profiles.saving": "Saving profile…",
  "agentSettings.profiles.saved": "Profile saved",
  // {name} is the profile's name.
  "agentSettings.profiles.confirmDelete": "Delete {name}?",
  "agentSettings.profiles.deleted": "Profile deleted",
  "agentSettings.profiles.title": "Launch profiles",
  "agentSettings.profiles.starters": "Starter profiles",
  "agentSettings.profiles.startersHint": "Pick a starter to open an editable draft with a ready purpose and briefing. Nothing is saved until you choose Save profile.",
  "agentSettings.profiles.useStarter": "Use starter",
  "agentSettings.profiles.savedProfile": "Saved profile",
  "agentSettings.profiles.newProfile": "New profile",
  // A button that starts a new, empty profile.
  "agentSettings.profiles.new": "New",
  "agentSettings.profiles.draftNote": "Editing an unsaved draft — nothing is saved until you choose Save profile.",
  "agentSettings.profiles.description": "Description",
  "agentSettings.profiles.instructions": "Workflow instructions",
  "agentSettings.profiles.agent": "Agent",
  "agentSettings.profiles.advanced": "Advanced · command, model and environment",
  "agentSettings.profiles.commandOverride": "Command override",
  "agentSettings.profiles.defaultModel": "Default model",
  "agentSettings.profiles.environment": "Environment (JSON)",
  "agentSettings.profiles.save": "Save profile",
  "agentSettings.profiles.delete": "Delete profile",

  // AgentCommands
  "agentSettings.commands.checking": "Checking commands on this machine…",
  "agentSettings.commands.complete": "Command lookup complete. No agents were started.",
  "agentSettings.commands.title": "Agent commands",
  "agentSettings.commands.intro": "Checks commands on this machine’s default PATH. This does not check login or model access.",
  "agentSettings.commands.found": "Found",
  "agentSettings.commands.notFound": "Not found",
  "agentSettings.commands.notChecked": "Not checked",
  "agentSettings.commands.checkAgain": "Check again",

  // UsagePanel
  "agentSettings.usage.loading": "Loading usage…",
  // The budget that covers every agent together.
  "agentSettings.usage.overall": "Overall",
  "agentSettings.usage.budgets": "Budgets",
  // {label} is "Overall" or an agent name; {period} is "daily" or "weekly".
  "agentSettings.usage.budgetRowLabel": "{label} ({period})",
  "agentSettings.usage.daily": "daily",
  "agentSettings.usage.weekly": "weekly",
  "agentSettings.usage.accountQuota": "Account quota",
  // A quota window length.
  "agentSettings.usage.fiveHour": "5 hour",
  "agentSettings.usage.sevenDay": "7 day",
  // {age} is how long ago, e.g. "5m ago".
  "agentSettings.usage.quotaStale": "Last updated {age} — a session's statusline hasn't reported since.",
  // Shown under an amount spent today.
  "agentSettings.usage.today": "today",
  "agentSettings.usage.thisWeek": "this week",
  // A time window to report over (noun).
  "agentSettings.usage.window": "Window",
  "agentSettings.usage.days": "{n} days",
  "agentSettings.usage.costByDay": "Cost by day",
  "agentSettings.usage.noUsage": "No usage recorded in this window.",
  "agentSettings.usage.byAgentModelLabel": "By agent and model",
  "agentSettings.usage.byAgentModel": "By agent / model",
  "agentSettings.usage.estimatedTitle": "Includes spend estimated from the model price table",
  // {cost} is a dollar amount; "est." abbreviates "estimated".
  "agentSettings.usage.estimated": "(~{cost} est.)",
  // {input} and {output} are token counts.
  "agentSettings.usage.tokensInOut": "{input} in / {output} out",
  "agentSettings.usage.nothingYet": "Nothing yet.",
  "agentSettings.usage.byProject": "By project",
  "agentSettings.usage.unassigned": "Unassigned",
  "agentSettings.usage.topSessionsLabel": "Top sessions by cost",
  "agentSettings.usage.topSessions": "Top sessions",
  "agentSettings.usage.noneYet": "None yet.",
  "agentSettings.usage.topTasksLabel": "Top tasks by cost",
  "agentSettings.usage.topTasks": "Top tasks",
  // A budget whose stop-mode limit has been reached.
  "agentSettings.usage.blocked": "blocked",

  // OutcomesPanel
  "agentSettings.outcomes.group.agent": "Agent",
  "agentSettings.outcomes.group.model": "Model",
  "agentSettings.outcomes.group.project": "Project",
  "agentSettings.outcomes.col.totalCost": "Total cost",
  "agentSettings.outcomes.col.perPass": "$/pass",
  "agentSettings.outcomes.col.perAccepted": "$/accepted",
  "agentSettings.outcomes.col.per100Lines": "$/100 lines",
  "agentSettings.outcomes.col.passesPer10": "Passes/$10",
  "agentSettings.outcomes.col.medianTime": "Median time to pass",
  // Short duration units.
  "agentSettings.outcomes.seconds": "{n}s",
  "agentSettings.outcomes.minutes": "{n}m",
  "agentSettings.outcomes.loading": "Loading outcomes…",
  "agentSettings.outcomes.groupBy": "Group by",
  "agentSettings.outcomes.spendComparison": "Spend comparison",
  "agentSettings.outcomes.spendByAgent": "Spend by agent",
  "agentSettings.outcomes.spendByModel": "Spend by model",
  "agentSettings.outcomes.spendByProject": "Spend by project",
  "agentSettings.outcomes.empty": "Nothing yet — run a task or a check to see outcomes here.",
  "agentSettings.outcomes.table": "Outcomes table",
  "agentSettings.outcomes.costPerOutcome": "Cost per outcome",
  "agentSettings.outcomes.passed": "Passed",
  "agentSettings.outcomes.accepted": "Accepted",
  "agentSettings.outcomes.shippedTitle": "Pull requests / commits reported by Claude Code's OpenTelemetry counters",
  "agentSettings.outcomes.shipped": "PRs / commits",
  "agentSettings.outcomes.partialTitle": "Some of this row's cost has no measured or estimated source",
  // A row flag: only part of the cost is known.
  "agentSettings.outcomes.partial": "partial",
  "agentSettings.outcomes.estimatedTitle": "Some of this row's cost is a token-based estimate, not a measured figure",
  "agentSettings.outcomes.estimated": "estimated",
  "agentSettings.outcomes.footnote": "Costs prefer exact OpenTelemetry figures, then the agent's own reported cost, then a configured per-model token estimate — rows marked \"partial\" or \"estimated\" are not fully measured. PRs / commits come from Claude Code's OpenTelemetry counters; \"—\" means nothing in the row reported them. See docs/outcomes.md.",

  // BudgetsPanel
  "agentSettings.budgets.saved": "Budgets saved",
  "agentSettings.budgets.title": "Budgets",
  "agentSettings.budgets.loading": "Loading budgets…",
  "agentSettings.budgets.intro": "Daily and weekly USD spend caps, overall and per agent, plus a per-task budget (set on the task itself). \"Warn\" only alerts; \"Stop\" refuses new dispatches and session launches once a limit reaches 100%, and cancels a task over its own budget — an interactive session is never killed, only told.",
  "agentSettings.budgets.dailyCap": "Daily cap (USD)",
  "agentSettings.budgets.weeklyCap": "Weekly cap (USD)",
  "agentSettings.budgets.noCap": "no cap",
  "agentSettings.budgets.mode": "Mode",
  "agentSettings.budgets.warnOnly": "Warn only",
  "agentSettings.budgets.stopAt100": "Stop at 100%",
  "agentSettings.budgets.overallExhausted": "The overall budget is currently exhausted in stop mode.",
  "agentSettings.budgets.perAgent": "Per agent",
  "agentSettings.budgets.agentNamePlaceholder": "agent name (e.g. claude, codex)",
  "agentSettings.budgets.dailyPlaceholder": "daily $",
  "agentSettings.budgets.weeklyPlaceholder": "weekly $",
  // Budget modes (verbs): only warn, or stop new work.
  "agentSettings.budgets.warn": "Warn",
  "agentSettings.budgets.stop": "Stop",
  "agentSettings.budgets.addAgentLimit": "+ Add agent limit",
  "agentSettings.budgets.alerts": "Alerts",
  "agentSettings.budgets.spendThresholds": "Spend thresholds (%)",
  "agentSettings.budgets.quotaThresholds": "Claude quota thresholds (%)",
  "agentSettings.budgets.anomalyDetection": "Cost anomaly detection",
  "agentSettings.budgets.anomalyMultiplier": "Anomaly multiplier (x trailing 7-day median $/hour)",
  "agentSettings.budgets.save": "Save budgets",

  // ModelPrices and modelPrices
  "agentSettings.prices.needsName": "Every price needs a model or agent name.",
  // {name} is a model or agent name.
  "agentSettings.prices.listedTwice": "{name} is listed twice.",
  "agentSettings.prices.needsRates": "{name}: enter input and output rates.",
  "agentSettings.prices.ratesInvalid": "{name}: rates must be numbers of 0 or more.",
  // Stands in for an agent name when spend is known only in tokens.
  "agentSettings.prices.tokenOnly": "Token-only",
  // {label} is an agent name such as "Codex"; {models} a comma-separated list of model names.
  "agentSettings.prices.unpricedNotice": "{label} spend isn't shown until you set a price for {models}.",
  "agentSettings.prices.title": "Model prices",
  "agentSettings.prices.loading": "Loading prices…",
  "agentSettings.prices.saved": "Model prices saved",
  // Followed by the example "codex", then introAfter.
  "agentSettings.prices.introBefore": "USD per 1M tokens, used to estimate spend for agents that report tokens but no cost (Codex). A row named after an agent (e.g.",
  "agentSettings.prices.introAfter": ") covers its models without their own row. Cached input is optional; left blank it is billed at the input rate.",
  "agentSettings.prices.modelOrAgent": "Model or agent",
  "agentSettings.prices.modelOrAgentPlaceholder": "model or agent",
  "agentSettings.prices.input": "Input",
  "agentSettings.prices.cachedInput": "Cached input",
  "agentSettings.prices.output": "Output",
  // Lower-case rate names used inside rateLabel and ratePlaceholder.
  "agentSettings.prices.field.input": "input",
  "agentSettings.prices.field.cached": "cached",
  "agentSettings.prices.field.output": "output",
  // Stands in for the name of a row not yet named.
  "agentSettings.prices.newRow": "new",
  // {name} is a model or agent; {field} is input, cached or output.
  "agentSettings.prices.rateLabel": "{name} {field} per 1M",
  "agentSettings.prices.ratePlaceholder": "{field} $",
  "agentSettings.prices.remove": "Remove {name}",
  // {agent} is an agent name; {tokens} a formatted token count.
  "agentSettings.prices.unpricedRow": "({agent}, no price · {tokens} tokens / 30 days)",
  "agentSettings.prices.setPrice": "Set price",
  "agentSettings.prices.addPrice": "+ Add price",
  "agentSettings.prices.save": "Save prices",

  // LimitPolicy
  "agentSettings.limits.mode.notify": "Notify me with one-tap choices",
  "agentSettings.limits.mode.wait": "Wait, then resume the same agent at the reset",
  "agentSettings.limits.mode.handoff": "Hand off now to another agent",
  // Short policy names shown inside "Use the default (…)".
  "agentSettings.limits.modeName.notify": "notify",
  "agentSettings.limits.modeName.wait": "wait",
  "agentSettings.limits.modeName.handoff": "handoff",
  "agentSettings.limits.modeName.swap": "swap",
  // {then} is a short policy name such as "wait".
  "agentSettings.limits.swapThen": "swap, then {then}",
  "agentSettings.limits.savedStatus": "Saved usage-limit policy",
  "agentSettings.limits.saved": "Usage-limit policy saved",
  "agentSettings.limits.title": "When an agent hits its usage limit",
  "agentSettings.limits.swapAccounts": "Swap accounts",
  "agentSettings.limits.swapHint": "First swap to another signed-in account of the same agent and continue the conversation (Settings → Accounts)",
  "agentSettings.limits.whenAllLimited": "When every account is limited",
  "agentSettings.limits.policy": "Policy",
  "agentSettings.limits.policyLabel": "Usage-limit policy",
  "agentSettings.limits.useDefault": "Use the default",
  // {policy} is the inherited policy's short name.
  "agentSettings.limits.useDefaultInherited": "Use the default ({policy})",
  "agentSettings.limits.fallbackAgent": "Fallback agent",
  "agentSettings.limits.fallbackAgentForHandoff": "Fallback agent (for “Hand off”)",
  "agentSettings.limits.none": "none",
  "agentSettings.limits.fallbackModel": "Fallback model",
  "agentSettings.limits.agentDefault": "agent default",
  "agentSettings.limits.save": "Save usage-limit policy",

  // Triggers
  "agentSettings.triggers.linearApiKey": "Linear API key",
  "agentSettings.triggers.jiraToken": "Jira API token or personal access token",
  // When a trigger last polled: never.
  "agentSettings.triggers.never": "never",
  "agentSettings.triggers.justNow": "just now",
  "agentSettings.triggers.minutesAgo": "{n}m ago",
  "agentSettings.triggers.hoursAgo": "{n}h ago",
  "agentSettings.triggers.daysAgo": "{n}d ago",
  // {message} is the server's error.
  "agentSettings.triggers.loadFailed": "Triggers: {message}",
  // {kind} is GitHub, Slack, Linear or Jira.
  "agentSettings.triggers.added": "{kind} trigger added — configure it below",
  "agentSettings.triggers.configInvalid": "Config must be valid JSON",
  "agentSettings.triggers.secretsInvalid": "Secrets must be valid JSON",
  // {name} is the trigger's name.
  "agentSettings.triggers.saved": "{name} saved",
  "agentSettings.triggers.confirmDelete": "Delete the {kind} trigger \"{name}\"?",
  "agentSettings.triggers.testing": "Testing…",
  "agentSettings.triggers.title": "Triggers",
  "agentSettings.triggers.intro": "Let this project pick up work on its own: a labelled GitHub issue or an @mention, a Slack message or /lectern command, or a labelled Linear or Jira issue. Every source needs an author allowlist before it can act — see docs/triggers.md.",
  "agentSettings.triggers.kind": "Trigger kind",
  "agentSettings.triggers.add": "Add trigger",
  "agentSettings.triggers.none": "No triggers configured for this project yet.",
  "agentSettings.triggers.unnamed": "(unnamed)",
  // {time} is e.g. "5m ago" or "never".
  "agentSettings.triggers.lastPoll": "last poll: {time}",
  "agentSettings.triggers.enabled": "Enabled",
  "agentSettings.triggers.hideSettings": "Hide settings",
  // A button (verb) that opens the trigger's settings.
  "agentSettings.triggers.edit": "Edit",
  "agentSettings.triggers.testConnection": "Test connection",
  "agentSettings.triggers.delete": "Delete",
  "agentSettings.triggers.pollInterval": "Poll interval (seconds, GitHub/Linear/Jira only)",
  "agentSettings.triggers.config": "Config (JSON)",
  // {set} secrets have a value out of {total}.
  "agentSettings.triggers.secrets": "Secrets (JSON — {set} of {total} set; leave blank to keep them unchanged)",
  "agentSettings.triggers.save": "Save",
  "agentSettings.triggers.recentEvents": "Recent events",
  "agentSettings.triggers.noEvents": "No events yet.",
  // {id} is a task number; {status} its status.
  "agentSettings.triggers.taskCreated": "→ task #{id} ({status})",

  // Skills
  "agentSettings.skills.loading": "Loading available skills…",
  "agentSettings.skills.summary": "{available} available · {attached} attached",
  "agentSettings.skills.attachedStatus": "Skill attached.",
  // {message} is the server's error.
  "agentSettings.skills.destinationOccupied": "Destination occupied: {message}",
  "agentSettings.skills.detachedStatus": "Skill detached.",
  "agentSettings.skills.title": "Project skills",
  "agentSettings.skills.intro": "New launches use saved attachments; running processes are not restarted.",
  // The agent CLI whose skills are shown.
  "agentSettings.skills.provider": "Provider",
  "agentSettings.skills.providerLabel": "Skills provider",
  // A button (verb).
  "agentSettings.skills.reload": "Reload",
  "agentSettings.skills.searchCatalog": "Search catalog",
  "agentSettings.skills.searchPlaceholder": "name, source, description",
  // A heading: skills already attached.
  "agentSettings.skills.attachedHeading": "Attached",
  "agentSettings.skills.noneAttached": "No skills attached for this provider.",
  "agentSettings.skills.detach": "Detach",
  "agentSettings.skills.available": "Available on this machine",
  // A disabled button's label: this skill is already attached.
  "agentSettings.skills.attachedButton": "Attached",
  "agentSettings.skills.attach": "Attach",
  "agentSettings.skills.sourceDirs": "Skill source directories",
  "agentSettings.skills.unsavedDirs": "Unsaved directory changes",
  "agentSettings.skills.clear": "Clear",
  "agentSettings.skills.dirsSaved": "Directories saved. Reload the provider to discover them.",
  "agentSettings.skills.saveDirs": "Save directories",

  // Workflows
  "agentSettings.workflows.loading": "Loading available workflows…",
  // {provider} is an agent product name such as Claude Code.
  "agentSettings.workflows.available.one": "{count} workflow available for {provider}",
  "agentSettings.workflows.available.other": "{count} workflows available for {provider}",
  "agentSettings.workflows.enabledStatus": "{name} enabled. Start a new {provider} session to use the change.",
  "agentSettings.workflows.disabledStatus": "{name} disabled. Start a new {provider} session to use the change.",
  "agentSettings.workflows.title": "Project workflows",
  "agentSettings.workflows.intro": "Choose a workflow, then use its commands in a new agent session. Disabling keeps your specs, plans, and other project documents.",
  "agentSettings.workflows.providerLabel": "Workflows provider",
  "agentSettings.workflows.loadingShort": "Loading…",
  "agentSettings.workflows.retry": "Retry",
  "agentSettings.workflows.none": "No optional workflows are available for this provider.",
  "agentSettings.workflows.pinnedVersion": "Pinned version:",
  "agentSettings.workflows.upstream": "Upstream project",
  // {name} is the workflow's name.
  "agentSettings.workflows.disableNamed": "Disable {name}",
  "agentSettings.workflows.enableNamed": "Enable {name}",
  // State label (adjective): the workflow is on.
  "agentSettings.workflows.enabled": "Enabled",
  // A button (verb) that turns the workflow on.
  "agentSettings.workflows.enable": "Enable",
  "agentSettings.workflows.commands": "Commands",
  "agentSettings.workflows.reloadNote": "Start a new {provider} session after enabling or disabling this workflow.",
};

export default catalog;
