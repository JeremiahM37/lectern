// English strings for this area of the app. Keys are "area.name"; values may
// hold {placeholders}. Every other language has a file of the same name.
const catalog: Record<string, string> = {
  // Sessions (the session list screen)
  "sessions.list.title": "Sessions",
  // {active} is the number of active sessions.
  "sessions.list.activeSummary": "{active} active · Pick up where you left off.",
  "sessions.list.setupFailed": "Workspace setup failed. Inspect retained files before launching again.",
  "sessions.list.settingUp": "Workspace is setting up. Attach becomes available when setup finishes.",
  // {error} is an error message from the server.
  "sessions.list.attachManually": "{error} — attach manually",
  // Prompt label; the box below it holds a tmux command to copy.
  "sessions.list.attachWith": "Attach with:",
  "sessions.list.undo": "Undo",
  "sessions.list.revived": "Revived “{name}”.",
  "sessions.list.restoredOf.one": "Restored {restored} of {count} interrupted session.",
  "sessions.list.restoredOf.other": "Restored {restored} of {count} interrupted sessions.",
  "sessions.list.searchSavedLabel": "Search saved conversations",
  // Button text; on wide screens followed by sessions.list.searchSavedWide (" conversations").
  "sessions.list.searchSaved": "Search saved",
  "sessions.list.searchSavedWide": " conversations",
  "sessions.list.findAgentsLabel": "Find running agents",
  // Button text in three parts: "⌕ Find" + " running" (wide screens only) + " agents".
  "sessions.list.findAgentsFind": "⌕ Find",
  "sessions.list.findAgentsWide": " running",
  "sessions.list.findAgentsEnd": " agents",
  "sessions.list.restoreClosedLabel": "Restore closed sessions",
  // Button (verb): opens the list of closed sessions to restore.
  "sessions.list.restoreButton": "↺ Restore",
  "sessions.list.newSession": "Start an agent",
  // Followed by a list of the relaunched sessions' names.
  "sessions.list.relaunched.one": "Relaunched {count} session after a restart:",
  "sessions.list.relaunched.other": "Relaunched {count} sessions after a restart:",
  "sessions.list.dismiss": "Dismiss",
  "sessions.list.interrupted.one": "1 session was interrupted by a restart and could not be reopened automatically.",
  "sessions.list.interrupted.other": "{count} sessions were interrupted by a restart and could not be reopened automatically.",
  "sessions.list.restoring": "Restoring…",
  "sessions.list.restoreIt": "Restore it",
  "sessions.list.restoreCount": "Restore {count}",
  "sessions.list.searchPlaceholder": "Search sessions, groups, branches or folders",
  "sessions.list.searchLabel": "Find a session or project",
  "sessions.list.groupBy": "Group by",
  "sessions.list.groupByLabel": "Group sessions by",
  "sessions.list.groupNone": "None",
  "sessions.list.groupNamed": "Named group",
  "sessions.list.groupProject": "Project",
  // A target is the machine a session runs on.
  "sessions.list.groupTarget": "Machine",
  // Label before a filter: which sessions to show.
  "sessions.list.show": "Show",
  "sessions.list.scopeActive": "Active sessions",
  "sessions.list.scopeAll": "Include ended and untracked",
  "sessions.list.scopeArchived": "Archived sessions",
  "sessions.list.restoreFailed": "Restore failed; the closed session is unchanged.",
  "sessions.list.sectionTitle": "Sessions and projects",
  "sessions.list.noMatch": "No sessions match your search.",
  "sessions.list.noArchived": "No archived sessions. Use “Stop and archive” in a session’s actions to keep it here for later.",
  "sessions.list.empty": "No sessions here.",
  "sessions.list.archivedOutput": "Archived terminal output",
  "sessions.list.closeArchivedOutput": "Close archived output",
  "sessions.list.close": "Close",
  "sessions.list.noOutput": "No terminal output was available.",
  "sessions.list.forkSetupStarted": "Fork workspace setup started. Follow progress in Sessions.",
  "sessions.list.forkStarted": "Fork started. The original session keeps running.",

  // GroupEditor (Move to group sheet)
  "sessions.groupEditor.title": "Move to group",
  "sessions.groupEditor.close": "Close group editor",
  "sessions.groupEditor.path": "Group path",
  // Example group path; keep the slash.
  "sessions.groupEditor.pathPlaceholder": "Work/Client",
  "sessions.groupEditor.hint": "Use / for nested groups. Leave blank to ungroup.",
  "sessions.groupEditor.save": "Save group",

  // SessionDialogs (shared)
  "sessions.dialogs.moreAgents": "More agents…",
  // Label: the AI model the agent runs on.
  "sessions.dialogs.model": "Model",

  // SessionDialogs: New session
  "sessions.dialogs.newSession.loadFailed": "Could not load launch options: {error}",
  "sessions.dialogs.newSession.checkingMemory": "Checking project memory…",
  "sessions.dialogs.newSession.memoryUnavailable": "Memory status unavailable",
  "sessions.dialogs.newSession.memoryPreviewFailed": "Could not preview project context. You can still start the session.",
  // {agent} is an agent program name, e.g. claude.
  "sessions.dialogs.newSession.noteNoYolo": "{agent} has no way to skip its prompts — it will ask",
  "sessions.dialogs.newSession.yolo": "Runs without asking",
  "sessions.dialogs.newSession.noteAsks": "Asks before risky actions",
  // Part of a " · "-joined launch summary. {name} is a launch profile name, {agent} an agent name.
  "sessions.dialogs.newSession.noteProfile": "launch profile “{name}” ({agent})",
  "sessions.dialogs.newSession.noteWorktree": "isolated Git worktree",
  "sessions.dialogs.newSession.noteBrief": "primed with project memory",
  "sessions.dialogs.newSession.noteResume": "resumes the agent’s last conversation",
  "sessions.dialogs.newSession.setupStarted": "Workspace setup started. You can keep using Lectern.",
  "sessions.dialogs.newSession.started": "Session started",
  "sessions.dialogs.newSession.title": "Start an agent",
  "sessions.dialogs.newSession.close": "Close",
  "sessions.dialogs.newSession.intro": "Pick a folder and an agent. Lectern runs it and tells you when it needs you.",
  "sessions.dialogs.newSession.project": "Project",
  "sessions.dialogs.newSession.blankRoom": "A new empty folder",
  "sessions.dialogs.newSession.recentProjects": "Recent",
  "sessions.dialogs.newSession.otherProjects": "Other projects",
  "sessions.dialogs.newSession.blankRoomHint": "Lectern makes a new folder for this session. You can turn it into a project later.",
  "sessions.dialogs.newSession.setupCommandHint": "This project’s setup command runs in the new checkout before the agent starts.",
  "sessions.dialogs.newSession.agent": "Agent",
  // {agent} is an agent name, {name} a launch profile name.
  "sessions.dialogs.newSession.lockedByProfile": "Locked to {agent} by the “{name}” launch profile — pick another profile under More options.",
  "sessions.dialogs.newSession.hintNoModel": "no model switch — the Model field is ignored",
  "sessions.dialogs.newSession.hintNoResume": "cannot resume its own history",
  "sessions.dialogs.newSession.advanced": "More options",
  "sessions.dialogs.newSession.name": "Name",
  // Label: a folder-like group the session is filed under.
  "sessions.dialogs.newSession.group": "Group",
  "sessions.dialogs.newSession.launchProfile": "Launch profile",
  "sessions.dialogs.newSession.noProfile": "Agent and project defaults",
  "sessions.dialogs.newSession.manageProfiles": "Manage launch profiles",
  // {name} launch profile, {agent} agent name.
  "sessions.dialogs.newSession.profileSummary": "{name} · {agent}. Settings are captured when the session starts.",
  "sessions.dialogs.newSession.profileSummaryModel": "{name} · {agent} · default model: {model}. Settings are captured when the session starts.",
  "sessions.dialogs.newSession.modelOverride": "{model} — or override",
  "sessions.dialogs.newSession.modelNone": "this agent has no model switch",
  "sessions.dialogs.newSession.modelDefault": "default — or type any model name",
  "sessions.dialogs.newSession.modelType": "type the model name",
  "sessions.dialogs.newSession.worktree": "Isolate in a new Git worktree",
  "sessions.dialogs.newSession.worktreeHint": "A fresh session with separate files on a new branch. Starts from a committed revision; uncommitted edits stay in the original directory.",
  "sessions.dialogs.newSession.worktreeBase": "Base branch, tag or commit",
  "sessions.dialogs.newSession.worktreeBranch": "New branch name",
  "sessions.dialogs.newSession.startFrom": "Start from",
  "sessions.dialogs.newSession.startFresh": "Fresh context",
  "sessions.dialogs.newSession.startBrief": "Fresh, primed with what this project knows",
  "sessions.dialogs.newSession.startResume": "Resume the agent's own last conversation",
  "sessions.dialogs.newSession.briefHint": "Pulls the project’s durable memory and its last handoff into the first message.",
  "sessions.dialogs.newSession.resumeHint": "Reopens the agent’s own previous conversation in this directory.",
  "sessions.dialogs.newSession.yoloUnsupported": "{agent} always asks before risky actions.",
  "sessions.dialogs.newSession.yoloHint": "The agent runs commands and edits files without asking first.",
  "sessions.dialogs.newSession.askHintPhone": "The agent asks you before it runs commands or edits files. Answer here, in Approvals, or from your phone.",
  "sessions.dialogs.newSession.askHint": "The agent asks you before it runs commands or edits files.",
  // Label: process sandboxing for the agent.
  "sessions.dialogs.newSession.isolation": "Sandbox",
  "sessions.dialogs.newSession.isolationDefault": "Project default",
  "sessions.dialogs.newSession.isolationNone": "No sandbox",
  // bwrap is a program name; keep it.
  "sessions.dialogs.newSession.isolationBwrap": "Sandbox with bubblewrap (fast)",
  "sessions.dialogs.newSession.isolationDocker": "Sandbox in a Docker container",
  "sessions.dialogs.newSession.network": "Network",
  "sessions.dialogs.newSession.networkAllow": "Allow all network access",
  "sessions.dialogs.newSession.networkDeny": "Only allow-listed sites",
  "sessions.dialogs.newSession.bwrapHint": "Runs the agent inside bubblewrap: its own filesystem view, this project's directory and its own auth read-write, the rest of $HOME hidden.",
  "sessions.dialogs.newSession.dockerHint": "Runs the agent inside a disposable Docker container with the same mounts.",
  "sessions.dialogs.newSession.firstMessage": "First message (optional)",
  "sessions.dialogs.newSession.overlapWarning": "⚠ This looks like it might already be claimed:",
  "sessions.dialogs.newSession.overlapAdvice": "Check their work or ask the operator before starting.",
  "sessions.dialogs.newSession.start": "Start",

  // SessionDialogs: Discover (running agents)
  "sessions.dialogs.discover.title": "Running agents",
  "sessions.dialogs.discover.intro": "Adopting does not restart or disturb it.",
  "sessions.dialogs.discover.empty": "No agents found running on any machine.",
  // {name} is a tmux session name.
  "sessions.dialogs.discover.projectFor": "Project for {name}",
  "sessions.dialogs.discover.unassigned": "No project",
  // Verb: take over an already-running agent.
  "sessions.dialogs.discover.adopt": "Adopt",

  // SessionDialogs: Handoff
  "sessions.dialogs.handoff.requested": "Asked for a handoff — it lands when the agent finishes its turn",
  // Verb: hand the work off to another session.
  "sessions.dialogs.handoff.title": "Hand off",
  // Follows the session's name in bold.
  "sessions.dialogs.handoff.intro": "writes down where it got to — what it did, what it learned, what it was about to do — and the next session starts primed with it.",
  // Label: what to do next after the handoff is written.
  "sessions.dialogs.handoff.then": "Then",
  "sessions.dialogs.handoff.modeSuccessor": "Start a new session with it",
  "sessions.dialogs.handoff.modeNote": "Just write it down, keep this session running",
  "sessions.dialogs.handoff.handTo": "Hand it to",
  // Appended to the current agent's name in the list.
  "sessions.dialogs.handoff.sameAgentSuffix": " — same agent, clean context",
  "sessions.dialogs.handoff.otherAgentHint": "The work moves to {agent}. It starts fresh, knowing only what the handoff says.",
  "sessions.dialogs.handoff.sameAgentHint": "Same agent, clean context — for when the window is full.",
  // Placeholder: the default model is used.
  "sessions.dialogs.handoff.modelDefault": "default",
  "sessions.dialogs.handoff.modelNone": "{agent} has no model switch",
  // Followed by the session name, then retireAfter.
  "sessions.dialogs.handoff.retire": "Retire",
  "sessions.dialogs.handoff.retireAfter": "once the handoff is written",
  "sessions.dialogs.handoff.killHint": "Its tmux session ends. The handoff and its history stay.",
  "sessions.dialogs.handoff.keepHint": "Both sessions keep running — useful if you want to compare them.",
  "sessions.dialogs.handoff.goSuccessor": "⇥ Write it and hand over",
  "sessions.dialogs.handoff.goNote": "⇥ Write the handoff",

  // ActionMenu
  // {name} is the session's name.
  "sessions.actionMenu.label": "More actions for {name}",
  "sessions.actionMenu.more": "More ···",

  // SessionGroups
  "sessions.groups.ungrouped": "Ungrouped",
  // Heading for sessions with no project or no machine.
  "sessions.groups.unassigned": "No project",
  // Follows the group's session count, e.g. "5 · 2 waiting".
  "sessions.groups.waiting": " · {n} need you",

  // SessionCard
  // Compact durations: seconds, minutes, hours and minutes, days and hours.
  "sessions.card.duration.seconds": "{s}s",
  "sessions.card.duration.minutes": "{m}m",
  "sessions.card.duration.hoursMinutes": "{h}h {m}m",
  "sessions.card.duration.daysHours": "{d}d {h}h",
  "sessions.card.cancelRequested": "Cancellation requested. Files already created will be retained.",
  // {path} is the session's folder.
  "sessions.card.makeProjectPrompt": "Make this a project.\n\n{path}\n\nIt stays exactly where it is. Name it:",
  "sessions.card.projectCreated": "Project created. The session keeps running.",
  // Used in place of a name when the session has none.
  "sessions.card.sessionFallback": "session",
  "sessions.card.stoppedTracking": "Stopped tracking “{name}”. It keeps running.",
  "sessions.card.ended": "Ended “{name}”.",
  "sessions.card.renameScratchPrompt": "Rename this scratch terminal. Its folder is untouched.",
  "sessions.card.renameSessionPrompt": "Rename this session. Its terminal keeps running.",
  "sessions.card.renamed": "Renamed.",
  "sessions.card.pathCopied": "Scratch folder path copied.",
  "sessions.card.copyFailed": "Copy failed. Select the path and copy it manually.",
  // Session status labels.
  // Lectern no longer tracks this session.
  "sessions.card.previewCancelling": "Cancellation requested. Waiting for checkout to stop; files will be retained.",
  "sessions.card.previewSettingUp": "Setting up workspace… Attach becomes available when setup finishes.",
  "sessions.card.progressUnavailable": "Progress unavailable: {error}",
  "sessions.card.setupFailedPreview": "Setup failed: {error}",
  "sessions.card.unassigned": "No project",
  // {duration} is a compact time such as "5m": how long the session has been quiet.
  "sessions.card.quiet": "quiet {duration}",
  "sessions.card.copyPath": "Copy path",
  "sessions.card.accountTitle": "The login this agent runs under (Settings → Accounts)",
  // {duration} is a compact time such as "2h 5m".
  "sessions.card.setupFor": "setup {duration}",
  "sessions.card.upFor": "up {duration}",
  "sessions.card.mediaTitle": "Recordings, files and links this session posted",
  "sessions.card.mediaCount": "▶ {count} media",
  "sessions.card.launchProfileTitle": "Captured launch profile",
  // {mode} is the sandbox kind, e.g. "bubblewrap".
  "sessions.card.isolationDenied": "Running inside {mode} with network denied (allowlist proxy only) — see docs/isolation.md",
  "sessions.card.isolationAllowed": "Running inside {mode} with network allowed — see docs/isolation.md",
  "sessions.card.adoptedTitle": "started outside lectern and adopted",
  "sessions.card.adopted": "adopted",
  "sessions.card.writingHandoff": "writing handoff…",
  // {machine} is a machine name; {error} is the reason it did not answer.
  "sessions.card.unreachableTitle": "{machine} is not answering ({error}). The status shown is the last one seen.",
  "sessions.card.thisMachine": "This machine",
  "sessions.card.noReply": "no reply",
  "sessions.card.unreachable": "⚠ {machine} unreachable",
  "sessions.card.machine": "machine",
  "sessions.card.workspaceSummary": "Workspace · {branch} · {state}",
  "sessions.card.worktreeSummary": "Worktree · {branch} · {state}",
  "sessions.card.repositoryCount": "{n} repositories",
  // {base} is a Git branch, {commit} a commit id.
  "sessions.card.base": "Base: {base} · {commit}",
  "sessions.card.notCreated": "not created",
  "sessions.card.setupError": "Setup error: {error}",
  "sessions.card.repository": "Repository",
  // {name} is a repository; {state} its setup state.
  "sessions.card.repoSetup": "{name} setup: {state}",
  "sessions.card.notCompleted": "not completed",
  "sessions.card.recordedState": "Recorded workspace state: {state}",
  "sessions.card.refreshProgress": "Refresh setup progress",
  "sessions.card.settingUpButton": "Setting up",
  "sessions.card.retryCancel": "Retry cancellation",
  "sessions.card.cancelSetup": "Cancel setup",
  "sessions.card.revive": "↻ Revive",
  "sessions.card.restore": "↺ Restore",
  "sessions.card.attach": "⌨ Terminal",
  "sessions.card.chat": "Chat",
  "sessions.card.makeProject": "⇑ Make a project",
  "sessions.card.switching": "Switching…",
  // Verb: move the session to another agent.
  "sessions.card.switch": "⇄ Switch",
  "sessions.card.rename": "✎ Rename",
  "sessions.card.trackingRestored": "Tracking restored. Your session keeps running.",
  "sessions.card.trackAgain": "Track again",
  "sessions.card.reviewChanges": "Review changes",
  "sessions.card.reviewMerge": "Review & merge",
  "sessions.card.openInTerminal": "Open in terminal",
  "sessions.card.interrupt": "⎋ Interrupt",
  // Verb: hand the work over to a fresh session.
  "sessions.card.handoff": "⇥ Handoff",
  "sessions.card.cancelCheckout": "Cancel remaining checkout",
  "sessions.card.moveToGroup": "Move to group",
  "sessions.card.savedConversations": "Saved conversations",
  "sessions.card.workspaceRepositories": "Workspace repositories",
  "sessions.card.allocationValidated": "Allocation validated; files retained. Setup did not restart.",
  "sessions.card.recoverAllocation": "Recover allocation",
  "sessions.card.removeWorktreeConfirm": "Remove {path}? End its sessions first. Changed, untracked or ignored files prevent removal. The Git branch is kept.",
  "sessions.card.worktreeRemoved": "Worktree removed; branch kept.",
  "sessions.card.removeWorktree": "Remove worktree",
  "sessions.card.archivedOutput": "Archived terminal output",
  "sessions.card.unarchived": "Record unarchived. Its terminal stays stopped; find it under Include ended and untracked.",
  "sessions.card.unarchive": "Unarchive record",
  "sessions.card.archiveStopped": "Archive stopped record",
  "sessions.card.findRunning": "Find running sessions",
  "sessions.card.dismiss": "Dismiss",
  "sessions.card.stopTracking": "Stop tracking",
  "sessions.card.killConfirm": "Kill \"{name}\"?\n\nThis ends the tmux session and the conversation. Stop tracking leaves it running.",
  "sessions.card.kill": "Kill",
  // Verb: end the session.
  "sessions.card.end": "End",
  "sessions.card.stoppedArchived": "Stopped and archived “{name}”.",
  "sessions.card.stopArchiveConfirm": "Stop \"{name}\" and move its record to Archive? This ends its terminal process. Captured output, saved conversations and worktree files are retained.",
  "sessions.card.stopArchive": "Stop and archive",
  "sessions.card.project": "Project",
  "sessions.card.unassignedOption": "— no project —",

  // NeedsYou
  "sessions.needsYou.reason.approval": "Approval needed",
  "sessions.needsYou.reason.waiting": "Wants you",
  "sessions.needsYou.reason.failedSession": "Setup failed",
  "sessions.needsYou.reason.stoppedSession": "Session ended",
  "sessions.needsYou.reason.failedTask": "Task failed",
  "sessions.needsYou.reason.reviewTask": "Ready to review",
  "sessions.needsYou.reason.duplicateWork": "Possible duplicate work",
  // Section heading: things that need the person's attention.
  "sessions.needsYou.title": "Needs you",
  "sessions.needsYou.pushPrompt": "Get a phone alert when a session needs you.",
  "sessions.needsYou.pushEnable": "Enable phone alerts",
  "sessions.needsYou.pushDismissLabel": "Dismiss phone alerts prompt",
  "sessions.needsYou.pushDismiss": "Not now",
  "sessions.needsYou.stale": "Couldn’t refresh — showing the last known state",
  // {id} is a task attempt's number.
  // {a} and {b} are two session names.
  "sessions.needsYou.pairNames": "{a} & {b}",
  "sessions.needsYou.showFewer": "Show fewer",
  "sessions.needsYou.showAll": "Show all {n}",
  "sessions.needsYou.reviewLink.one": "{count} task ready to review",
  "sessions.needsYou.reviewLink.other": "{count} tasks ready to review",
  // Link destination: the Board screen.
  "sessions.needsYou.toBoard": "→ Tasks",
  "sessions.needsYou.setupUnfinished": "workspace setup did not finish",
  "sessions.needsYou.terminalGone": "its terminal is gone",
  // Shown in place of a project name for a session with no project.
  "sessions.needsYou.unassigned": "no project",
  "sessions.needsYou.overlap": "Their last prompts overlap {percent}% — check they aren't building the same thing",
  "sessions.needsYou.openSession": "Open session",
  // {name} is a session name; "Open" is a verb.
  "sessions.needsYou.openNamed": "Open {name}",
  "sessions.needsYou.openTask": "Open task",
  "sessions.needsYou.stopDictating": "Stop dictating",
  "sessions.needsYou.dictate": "Dictate reason",
  "sessions.needsYou.showSession": "Show session",
  "sessions.needsYou.terminal": "⌨ Terminal",
  // Verb: review the session's or task's work.
  "sessions.needsYou.review": "Review",
  // Verb: open the chat view.
  "sessions.needsYou.chat": "Chat",

  // VoiceMode / voice-mode.ts (spoken aloud as well as shown)
  "sessions.voice.codeOmitted": "Code omitted.",
  "sessions.voice.cancelled": "Cancelled — nothing was sent.",
  "sessions.voice.sending": "Sending… tap to cancel",
  "sessions.voice.sent": "Sent to the session.",
  "sessions.voice.sendFailed": "Could not send: {error}",
  "sessions.voice.exiting": "Exiting voice mode.",
  "sessions.voice.nothingToRead": "Nothing to read back yet.",
  "sessions.voice.interruptSent": "Interrupt sent.",
  "sessions.voice.approved": "Approved.",
  "sessions.voice.denied": "Denied.",
  "sessions.voice.micDenied": "Microphone permission was denied.",
  // {code} is the browser's speech-recognition error code, e.g. "network".
  "sessions.voice.recognitionError": "Voice mode: {code}.",
  // Spoken aloud. {action} is what the agent wants to do. The words "approve" and "deny" are the spoken commands, which are only recognised in English.
  "sessions.voice.approvalPrompt": "Claude wants to run {action}. Say approve or deny.",
  "sessions.voice.unsupported": "Voice mode needs a browser with the Web Speech API (Chrome or Edge on desktop and Android). It is hidden here because this browser does not support it.",
  "sessions.voice.toggleOn": "🔊 Voice mode on",
  "sessions.voice.toggle": "🎧 Voice mode",
  "sessions.voice.speaking": "Speaking…",
  "sessions.voice.listening": "Listening…",
  "sessions.voice.holdMic": "Hold the mic to talk",
  "sessions.voice.idle": "Idle",
  "sessions.voice.pushToTalkHelp": "This browser cannot listen continuously, so voice mode falls back to push-to-talk: hold the button below, speak, then release.",
  "sessions.voice.holdToTalk": "🎙 Hold to talk",
  // {text} is what the person said, about to be sent.
  "sessions.voice.pendingSend": "Sending: “{text}”",
  "sessions.voice.cancel": "Cancel",
  // The quoted phrases are spoken commands, which are only recognised in English: keep them in English.
  "sessions.voice.hints": "Say “send it” or pause to send · “approve”/“deny” when asked · “interrupt” to stop the agent · “read that again” · “exit voice mode”",
  "sessions.voice.settings": "⚙ Voice settings",
  // Noun: the synthetic voice that reads replies aloud.
  "sessions.voice.voice": "Voice",
  "sessions.voice.browserDefault": "Browser default",
  "sessions.voice.rate": "Speaking rate",
  "sessions.voice.language": "Language",
  "sessions.voice.autoRead": "Read replies aloud automatically",

  // WorkspaceExtension
  "sessions.workspaceExtension.loading": "Loading workspace…",
  // {id} is the operation number; {state} is its state as the server reports it.
  "sessions.workspaceExtension.operation": "Addition {id}: {state}",
  "sessions.workspaceExtension.cancelRequested": "cancellation requested",
  "sessions.workspaceExtension.ready": "Ready to add a repository.",
  "sessions.workspaceExtension.noOthers": "No other projects on this machine.",
  "sessions.workspaceExtension.needsRecovery": "Workspace needs recovery before another repository can be added. Allocated files are retained.",
  "sessions.workspaceExtension.title": "Workspace repositories",
  "sessions.workspaceExtension.close": "Close",
  "sessions.workspaceExtension.intro": "Add a registered project on the same machine. Its checkout uses this workspace’s branch and runs its project setup command. Your existing terminal stays available.",
  "sessions.workspaceExtension.project": "Project",
  "sessions.workspaceExtension.base": "Base (optional)",
  "sessions.workspaceExtension.basePlaceholder": "Default branch",
  "sessions.workspaceExtension.noSetup": "No project setup command.",
  "sessions.workspaceExtension.add": "Add repository",
  "sessions.workspaceExtension.refresh": "Refresh progress",
  "sessions.workspaceExtension.cancel": "Cancel addition",
  "sessions.workspaceExtension.recover": "Check interrupted addition",

  // scratch.ts — title for a blank shell with no folder name; {id} is the session number
  "sessions.scratch.fallbackTitle": "Shell #{id}",

  // ScratchTerminals
  "sessions.scratch.title": "Scratch terminals",
  "sessions.scratch.sub": "Blank shells with no project. Attach to use one, or make it a project to keep its files and terminal with your work.",
  "sessions.scratch.noMatch": "No scratch terminals match your search.",
  "sessions.scratch.empty": "No scratch terminals. Open a blank shell from the Terminals tab and it waits here, out of the way of your sessions.",

  // restore.ts — how long ago a session closed
  "sessions.restore.ageRecently": "recently",
  "sessions.restore.ageJustNow": "just now",
  "sessions.restore.ageMinutes": "{minutes}m ago",
  "sessions.restore.ageHours": "{hours}h {minutes}m ago",
  "sessions.restore.ageDays": "{days}d {hours}h ago",

  // RestorePanel
  "sessions.restorePanel.noProject": "No project",
  "sessions.restorePanel.label": "Restore sessions",
  // Heading (noun): the place to bring closed sessions back
  "sessions.restorePanel.title": "Restore",
  "sessions.restorePanel.sub": "Closed, archived and interrupted sessions, newest first.",
  "sessions.restorePanel.loading": "Loading…",
  "sessions.restorePanel.searchPlaceholder": "Search names, projects, folders or the last message",
  "sessions.restorePanel.searchLabel": "Search restorable sessions",
  "sessions.restorePanel.loadingRows": "Loading restorable sessions…",
  "sessions.restorePanel.empty": "Nothing to restore.",
  "sessions.restorePanel.noMatch": "No closed session matches your search.",
  "sessions.restorePanel.unnamed": "Unnamed session",
  "sessions.restorePanel.likelyTitle": "Matched by folder, agent and time",
  "sessions.restorePanel.likely": "likely match",
  "sessions.restorePanel.unknownAgent": "unknown agent",
  "sessions.restorePanel.restoring": "Restoring…",
  "sessions.restorePanel.otherAgent": "Other agent…",

  // NowStrip
  "sessions.now.label": "Now",
  "sessions.now.toApprove": "{count} to approve",
  // {name} is the session name, {state} one of the state labels above
  "sessions.now.chipLabel": "{name} — {state}",

  // QuickSwitch
  "sessions.quickSwitch.providerUnavailable": "Provider unavailable",
  "sessions.quickSwitch.defaultModel": "Default model",
  "sessions.quickSwitch.providerGone": "That provider is no longer available. Your original session is unchanged.",
  "sessions.quickSwitch.favorite": "Add {label} to favorites",
  "sessions.quickSwitch.unfavorite": "Remove {label} from favorites",
  "sessions.quickSwitch.restoreTitle": "Restore in another agent",
  "sessions.quickSwitch.switchTitle": "Switch agent",
  "sessions.quickSwitch.close": "Close switcher",
  // Followed by the model name in bold: "Was: <model>"
  "sessions.quickSwitch.wasPrefix": "Was: ",
  // Followed by the model name in bold: "Current: <model>"
  "sessions.quickSwitch.currentPrefix": "Current: ",
  "sessions.quickSwitch.pickNext": ". Pick the next agent or model.",
  "sessions.quickSwitch.restoreHelp": "It starts in the same folder with the closed session's last handoff, or the end of its conversation, as context. Native conversations do not carry across agents.",
  "sessions.quickSwitch.switchHelp": "It gets a handoff in the same workspace and opens here when ready. Your original session stays available in Sessions.",
  "sessions.quickSwitch.retry": "Retry",
  "sessions.quickSwitch.loading": "Loading agents…",
  "sessions.quickSwitch.favorites": "Favorites",
  "sessions.quickSwitch.unavailableHere": "Unavailable on this device",
  "sessions.quickSwitch.savedProviders": "Saved providers",
  // Marks the model the session uses now (adjective)
  "sessions.quickSwitch.currentMarker": "Current",
  "sessions.quickSwitch.moreAgents": "More agents…",
  "sessions.quickSwitch.anotherModel": "Another model…",
  "sessions.quickSwitch.agent": "Agent",
  "sessions.quickSwitch.modelId": "Model ID",
  "sessions.quickSwitch.modelIdPlaceholder": "Exact model ID supported by this agent",
  "sessions.quickSwitch.switchModel": "Switch model",
  "sessions.quickSwitch.manageProviders": "Add or manage a provider…",

  // AwarenessOverlapChip — {id} is another session's number, {name} its name
  "sessions.overlap.title": "Shares recently-edited files with session #{id} ({name})",
  "sessions.overlap.chip": "⚠ overlaps #{id}",
  // Followed by the other session's name in bold
  "sessions.overlap.detailBefore": "Also edited by ",
  "sessions.overlap.detailAfter": " (session #{id}) in the last 30 minutes:",

  // RepositoryPicker
  "sessions.repoPicker.summary": "Additional repositories",
  "sessions.repoPicker.summaryCount": "Additional repositories ({n})",
  "sessions.repoPicker.help": "Add up to seven other projects on the same machine. Each gets separate files on the new branch.",
  "sessions.repoPicker.pickerLabel": "Additional repository",
  "sessions.repoPicker.noOthers": "No other projects on this machine",
  "sessions.repoPicker.add": "Add repository",
  "sessions.repoPicker.selected": "Selected repositories",
  // {name} is a project; the field holds the git revision to start from
  "sessions.repoPicker.baseFor": "Base for {name}",
  "sessions.repoPicker.basePlaceholder": "HEAD — current committed revision",
  "sessions.repoPicker.removeNamed": "Remove {name}",
  "sessions.repoPicker.remove": "Remove",

  // CheckBadge
  "sessions.check.started": "Check started",
  "sessions.check.running": "checking…",
  "sessions.check.passed": "check passed",
  "sessions.check.failed": "check failed",
  "sessions.check.error": "check error",
  "sessions.check.skipped": "check skipped",
  "sessions.check.starting": "Starting…",
  "sessions.check.run": "Run check",
  "sessions.check.noOutput": "(no output captured)",

  // QuotaChip — {spent}/{cap} are US dollar amounts
  "sessions.quota.budgetTitle": "Overall budget: ${spent} of ${cap}",
  "sessions.quota.budget": "${spent}/{cap}",
  "sessions.quota.blocked": " · blocked",
  "sessions.quota.lastUpdated": "Last updated {age}",
  // 5-hour usage window
  "sessions.quota.fiveHour": "5h {percent}%",
  // 7-day usage window
  "sessions.quota.sevenDay": "7d {percent}%",

  // usageFormat.ts / UsageBadges
  "sessions.usage.resetsNow": "resets now",
  "sessions.usage.resetsInHours": "resets in {hours}h {minutes}m",
  "sessions.usage.resetsInMinutes": "resets in {minutes}m",
  // {cost} is a dollar amount estimated from a price table
  "sessions.usage.estimatedCost": "~{cost} est.",
  "sessions.usage.ageSeconds": "{seconds}s ago",
  "sessions.usage.ageMinutes": "{minutes}m ago",
  "sessions.usage.ageHours": "{hours}h ago",
  "sessions.usage.ageDays": "{days}d ago",
  "sessions.usage.contextTitle": "{used} / {size} tokens used",
  "sessions.usage.contextLeftTitle": "Percent of context left until auto-compact",
  "sessions.usage.compactedTitle": "A context compaction just ran",
  "sessions.usage.nearlyFullTitle": "Context window is nearly full",
  "sessions.usage.compacting": "⚠ compacting",
  "sessions.usage.contextPercent": "⚠ context {percent}%",

  // SessionMemory
  "sessions.memory.basis.managed": "its own provisioned note — an exact link",
  "sessions.memory.basis.configured": "paths the operator named",
  "sessions.memory.basis.guessed": "guessed from the project's name",
  "sessions.memory.basis.all": "the whole store",
  "sessions.memory.basis.manual": "nothing automatically; agents look things up themselves",
  "sessions.memory.basis.off": "nothing; project memory is off",
  "sessions.memory.unreachable": "Lectern could not be reached.",
  // Section heading (noun)
  "sessions.memory.summary": "Memory",
  // {status} is the link state as the server reports it (linked, unlinked, unavailable)
  "sessions.memory.projectStatus": "Project memory {status}.",
  // {basis} is one of the sessions.memory.basis.* phrases
  "sessions.memory.reads": "This project reads {basis}.",
  "sessions.memory.unlinkedNote": "None of those notes exists, so sessions here start with no project memory and nothing says so.",
  "sessions.memory.notes": "{n} notes",
  "sessions.memory.writtenHeading": "Written by this session",
  "sessions.memory.asking": "Asking the memory store…",
  "sessions.memory.noProvider": "No memory provider is configured.",
  "sessions.memory.unavailable": "The memory store could not answer, so this is not “nothing written”.",
  // Followed by the session's memory key in code
  "sessions.memory.emptyBefore": "Nothing is recorded under",
  "sessions.memory.emptyAfter": ". An agent started before sessions carried this key, or one whose memory server was not given it, writes under no session at all.",
  // {kind} is the server's change kind: learned, changed, retracted or expired
  "sessions.memory.kindCount": "{n} {kind}",
  // Followed by the session's memory key in code
  "sessions.memory.under": "under",
  // {text} is what the memory said before it changed
  "sessions.memory.was": "was: {text}",
  // Button (verb)
  "sessions.memory.refresh": "Refresh",

  // memoryDelivery.ts — what the agent was allowed to read
  "sessions.memoryDelivery.mode.scoped": "this project's notes",
  "sessions.memoryDelivery.mode.managed": "the project's own note",
  "sessions.memoryDelivery.mode.all": "the whole store",
  "sessions.memoryDelivery.mode.manual": "looked up by the agent itself",
  "sessions.memoryDelivery.mode.off": "nothing",
  "sessions.memoryDelivery.untitled": "untitled memory",
  "sessions.memoryDelivery.bytes": "{size} B",
  "sessions.memoryDelivery.kilobytes": "{size} KB",
  "sessions.memoryDelivery.megabytes": "{size} MB",
  "sessions.memoryDelivery.noItems": "no items",
  "sessions.memoryDelivery.items.one": "{count} item",
  "sessions.memoryDelivery.items.other": "{count} items",

  // MemoryDeliveries
  "sessions.memoryDeliveries.thanksHelpful": "Thank you — that tunes ranking.",
  "sessions.memoryDeliveries.notedIrrelevant": "Noted as not relevant.",
  "sessions.memoryDeliveries.challengePrompt": "Why is this memory wrong or out of date?",
  "sessions.memoryDeliveries.challengeNeedsReason": "A challenge needs a reason — otherwise nobody can review it.",
  "sessions.memoryDeliveries.challengeSent": "Reported as wrong — the memory store will keep the claim and your objection side by side.",
  // Section heading (noun)
  "sessions.memoryDeliveries.summary": "Memory",
  "sessions.memoryDeliveries.reading": "Reading what was delivered…",
  "sessions.memoryDeliveries.unavailable": "What was delivered is unavailable, so this is not “nothing was”.",
  "sessions.memoryDeliveries.empty": "Nothing has been injected here yet. Project memory is added at launch and when a message retrieves something new.",
  "sessions.memoryDeliveries.justNow": "just now",
  "sessions.memoryDeliveries.unnamedRecords": "The store sent context without naming the records it came from.",
  "sessions.memoryDeliveries.whatItSaid": "What it said",
  "sessions.memoryDeliveries.cannotReview": "This store did not name the record, so it cannot be reviewed from here.",
  "sessions.memoryDeliveries.markedHelpful": "Marked helpful ✓",
  "sessions.memoryDeliveries.markedIrrelevant": "Marked not relevant",
  "sessions.memoryDeliveries.markedWrong": "Reported as wrong — sent for review",
  "sessions.memoryDeliveries.helpful": "Helpful",
  "sessions.memoryDeliveries.notRelevant": "Not relevant",
  "sessions.memoryDeliveries.thisIsWrong": "This is wrong",
  // Timeline entry kind label (lowercase)
  "sessions.memoryDeliveries.timelineKind": "memory",
  // Attempt number, e.g. "A2"
  "sessions.memoryDeliveries.attempt": "A{n}",

  // ScratchReview
  "sessions.scratchReview.kib": "{size} KiB",
  "sessions.scratchReview.mib": "{size} MiB",
  "sessions.scratchReview.today": "today",
  "sessions.scratchReview.idleDays": "{days}d idle",
  // {reasons} is a list of reasons, one per line
  "sessions.scratchReview.confirmDiscard": "Discard {name}?\n\n{reasons}\n\nIt moves to the scratch trash and can be recovered from there until it is purged.",
  "sessions.scratchReview.summary": "Scratch directory cleanup",
  "sessions.scratchReview.inspecting": "Inspecting scratch directories…",
  // {n} scratch directories
  "sessions.scratchReview.holdWork": "{n} hold work that no project claims.",
  "sessions.scratchReview.nothingUnclaimed": "Nothing unclaimed is waiting on you.",
  "sessions.scratchReview.emptyIdle": "{n} are empty and idle over {days} days; the server removes those on its own.",
  "sessions.scratchReview.noneDue": "No empty ones are due for removal.",
  "sessions.scratchReview.uninspectable": "{target} could not be inspected: {error}",
  "sessions.scratchReview.keepTitle": "Never remove this directory automatically",
  "sessions.scratchReview.keep": "Keep",
  "sessions.scratchReview.discardTitle": "Move to the scratch trash",
  "sessions.scratchReview.discard": "Discard",
  "sessions.scratchReview.removed": "Removed {n} empty scratch workspaces.",
  "sessions.scratchReview.removeEmpty": "Remove the {n} empty ones now",

};

export default catalog;
