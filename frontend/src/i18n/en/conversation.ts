// English strings for this area of the app. Keys are "area.name"; values may
// hold {placeholders}. Every other language has a file of the same name.
const catalog: Record<string, string> = {
  // Conversation — header, status and reader controls
  // {name} is the session or task name.
  "conversation.chat.dialogLabel": "Conversation with {name}",
  "conversation.chat.connecting": "Connecting…",
  // {agent} is the agent name; {state} is "Ended" or a status word from the server.
  "conversation.chat.sessionStatus": "{agent} · {state} · live reader",
  // A session that has finished.
  "conversation.chat.ended": "Ended",
  // {agent} is the agent name, {status} a status word from the server.
  "conversation.chat.taskStatus": "{agent} · {status}",
  // {agent} is the agent name, {status} a status word from the server, {n} the turn number.
  "conversation.chat.taskStatusTurn": "{agent} · {status} · turn {n}",
  "conversation.chat.waitingForOutput": "Waiting for agent output…",
  "conversation.chat.renameSession": "Rename session",
  "conversation.chat.rename": "Rename",
  "conversation.chat.offlineStatus": "Offline — showing the last output; sends are paused",
  // {status} is the last known status line.
  "conversation.chat.reconnectingStatus": "{status} · reconnecting…",
  "conversation.chat.switch": "⇄ Switch",
  "conversation.chat.close": "Close conversation",
  // Heading of the structured chat view (noun).
  "conversation.chat.viewChat": "Chat",
  "conversation.chat.viewLiveOutput": "Live output · last 500 lines",
  "conversation.chat.viewTask": "Task conversation",
  "conversation.chat.showTerminalText": "Terminal text",
  "conversation.chat.showChatCards": "Chat cards",
  "conversation.chat.smallerText": "Smaller text",
  "conversation.chat.largerText": "Larger text",
  "conversation.chat.latest": "↓ Latest",
  "conversation.chat.openTerminal": "⌨ Open terminal",
  "conversation.chat.reviewMerge": "± Review & merge",
  "conversation.chat.browser": "◎ Browser",

  // Conversation — task rows
  // Label of the task's original prompt (noun).
  "conversation.chat.rowTask": "Task",
  "conversation.chat.rowQueued": "You · queued for next turn",
  // {error} is the delivery error from the server.
  "conversation.chat.rowNotDelivered": "Not delivered · {error}",
  "conversation.chat.rowDelivered": "You · delivered to agent",
  "conversation.chat.rowAdded": "You · added to task",
  // {n} is the turn number.
  "conversation.chat.rowAgent": "Agent · turn {n}",
  // {n} is the turn number.
  "conversation.chat.rowResult": "Result · turn {n}",
  // Fallback name for a tool call with no name.
  "conversation.chat.rowTool": "Tool",
  "conversation.chat.rowToolResult": "tool result",
  "conversation.chat.rowVerify": "verify",

  // Conversation — receipts, errors and upload status
  // {error} is the delivery error from the server.
  "conversation.chat.receiptNotDelivered": "Not delivered: {error}",
  "conversation.chat.receiptDelivered": "Message delivered to the agent.",
  "conversation.chat.receiptAdded": "Instructions added to the task.",
  // {error} is an error message.
  "conversation.chat.refreshFailed": "Could not refresh: {error}. Displayed output may be stale.",
  "conversation.chat.tooManyFiles": "Attach up to 10 files per message.",
  // {name} is a file name.
  "conversation.chat.fileTooLarge": "{name} exceeds 25 MiB.",
  // {name} is a file name.
  "conversation.chat.uploading": "Uploading {name}…",
  "conversation.chat.filesReady": "Files ready. Add a message, then Send.",
  // {error} is an error message.
  "conversation.chat.uploadFailed": "Upload failed: {error} Previously uploaded files are kept.",
  "conversation.chat.offlineReceipt": "You're offline. Nothing was sent; your draft is kept on this device.",
  "conversation.chat.tooLong": "Message including attachments exceeds 32000 bytes. Shorten the message.",
  "conversation.chat.sending": "Sending…",
  "conversation.chat.sentToSession": "Sent to the session.",
  "conversation.chat.alreadyDelivered": "Already delivered.",
  "conversation.chat.savedInterrupting": "Saved. Interrupting the current run before continuing.",
  "conversation.chat.savedWaiting": "Saved. Waiting for delivery to the agent.",
  // {error} is an error message.
  "conversation.chat.deliveryUnconfirmed": "Could not confirm delivery: {error}. Your draft is kept.",
  "conversation.chat.interruptSent": "Interrupt sent. Check the agent output before sending new instructions.",

  // Conversation — composer hints
  "conversation.chat.hintSession": "Sends to the same running session. Enter adds a new line; use Send to send.",
  "conversation.chat.hintTakeover": "This run is continuing as an interactive session. Close Chat and choose Open session.",
  "conversation.chat.hintSandbox": "Send queues a new sandbox run with your instructions and the previous result.",
  "conversation.chat.hintBacklog": "Adds instructions to this task without dispatching it.",
  "conversation.chat.hintRunning": "Send queues a follow-up. Interrupt and send stops this run first, then continues.",
  "conversation.chat.hintQueued": "Your message is added before the queued run starts.",
  "conversation.chat.hintDefault": "Send continues the task in its existing worktree.",

  // Conversation — changed files
  "conversation.chat.changedFiles": "Changed files",
  // {count} is the number of changed files.
  "conversation.chat.changedFilesCount": "Changed files · {count}",
  "conversation.chat.readingChanges": "Reading working changes…",
  "conversation.chat.changesUnavailable": "Working changes are unavailable for this session.",
  // Shown when the branch name is unknown.
  "conversation.chat.workspace": "workspace",
  // {branch} is a Git branch name.
  "conversation.chat.branchWorking": "{branch} · working",
  "conversation.chat.truncated": " · truncated",
  "conversation.chat.noChanges": "No changed files right now.",
  // Verb: reload the list.
  "conversation.chat.refresh": "Refresh",

  // Conversation — output log
  "conversation.chat.agentOutput": "Agent output",
  "conversation.chat.shellSession": "This tracked session is a shell, not an agent conversation. Type to the pane from here, or use the Terminal action above for the full keyboard.",
  "conversation.chat.outputUnreadable": "Live output could not be read from this session.",
  "conversation.chat.outputUnreadableEnded": "Live output could not be read from this session because it has ended.",
  "conversation.chat.restoreTracking": "Restore tracking to continue the conversation.",
  "conversation.chat.messagesStillGo": "Messages still go to the session; the Terminal action above reads the pane directly.",
  "conversation.chat.draftRow": "You · not sent yet",
  "conversation.chat.waitingToStart": "Waiting for the conversation to start…",
  // Label of the model's hidden reasoning.
  "conversation.chat.thinking": "Thinking",
  // Speaker label for the person.
  "conversation.chat.you": "You",
  // Speaker label for the AI agent.
  "conversation.chat.agent": "Agent",
  "conversation.chat.loading": "Loading…",
  // {count} is the number of facts recalled from Grimoire (a notes app).
  "conversation.chat.recall.one": "Context from Grimoire · {count} fact",
  "conversation.chat.recall.other": "Context from Grimoire · {count} facts",

  // Conversation — composer
  "conversation.chat.messageAgent": "Message this agent",
  "conversation.chat.messageTask": "Message this task",
  "conversation.chat.placeholder": "Write a message or use your keyboard’s microphone…",
  "conversation.chat.attachedFiles": "Attached files",
  // {name} is a file name, {size} a whole number of kilobytes.
  "conversation.chat.attachmentSize": "{name} · {size} KB",
  // {name} is a file name.
  "conversation.chat.removeFile": "Remove {name}",
  "conversation.chat.draftSaved": "Draft saved on this device · not sent yet",
  "conversation.chat.attachUnavailable": "Attachments unavailable for this session or sandbox",
  "conversation.chat.attachHint": "PDFs, images, documents and other files · 25 MiB each",
  "conversation.chat.attach": "📎 Add file",
  "conversation.chat.interruptAndSend": "Interrupt and send",
  // Verb: stop the running agent.
  "conversation.chat.interrupt": "Interrupt",
  "conversation.chat.stopDictating": "Stop dictating",
  "conversation.chat.dictate": "Dictate message",
  "conversation.chat.listening": "🔴 Listening…",
  "conversation.chat.transcribing": "⏳ Transcribing…",
  "conversation.chat.recording": "🔴 Recording · tap to stop",
  // Screen-reader label of the mic button while speech is being turned into text.
  "conversation.chat.transcribingLabel": "Transcribing",
  "conversation.chat.onLectern": "Dictation is transcribed on your Lectern",
  "conversation.chat.send": "Send",
  "conversation.chat.offlineNotice": "Offline — nothing is sent while you are disconnected. Your draft is saved on this device and stays here when you reconnect.",

  // ApprovalCard
  // {tool} is a tool name such as Edit.
  // {command} is the first word of a shell command, e.g. git.
  // {tool} is a tool name such as Bash.
  "conversation.approval.allowing": "Allowing…",
  "conversation.approval.allowOnce": "Allow once",
  "conversation.approval.deny": "Deny…",
  "conversation.approval.why": "Tell the agent why (optional)",
  "conversation.approval.notePlaceholder": "e.g. not touching prod from a phone",
  "conversation.approval.cancel": "Cancel",
  "conversation.approval.denying": "Denying…",
  "conversation.approval.openTask": "Open task",

  // Tool cards (ToolCard)
  "conversation.toolCard.noItems": "No items.",
  "conversation.toolCard.arguments": "Arguments",
  "conversation.toolCard.running": "Running…",
  // Heading over a tool's output when the call failed.
  "conversation.toolCard.failed": "Failed",
  // Heading over a tool's output (noun).
  "conversation.toolCard.output": "Output",
  "conversation.toolCard.noOutput": "(no output)",
  "conversation.toolCard.outputShortened": "Output was shortened.",
  "conversation.toolCard.runningLabel": "running",
  "conversation.toolCard.failedLabel": "failed",

  // Tool card titles (describe)
  "conversation.describe.readFile": "Read file",
  "conversation.describe.writeFile": "Write file",
  "conversation.describe.editFile": "Edit file",
  // {count} is the number of edits (always more than one).
  "conversation.describe.edits": "{count} edits",
  "conversation.describe.editNotebook": "Edit notebook",
  "conversation.describe.terminal": "Terminal",
  // Title of a text search in files (noun).
  "conversation.describe.search": "Search",
  // {pattern} is the search pattern.
  "conversation.describe.pattern": "pattern: {pattern}",
  "conversation.describe.findFiles": "Find files",
  "conversation.describe.listFiles": "List files",
  "conversation.describe.fetchUrl": "Fetch URL",
  "conversation.describe.webSearch": "Web search",
  // A to-do plan (noun).
  "conversation.describe.plan": "Plan",
  // {count} is the number of to-do items.
  "conversation.describe.items.one": "{count} item",
  "conversation.describe.items.other": "{count} items",
  "conversation.describe.subagentTask": "Subagent task",
  "conversation.describe.planProposal": "Plan proposal",
  "conversation.describe.question": "Question",
  "conversation.describe.applyChanges": "Apply changes",

  // Chat cards
  // Fallback name for a tool call with no name.
  "conversation.chatCards.tool": "Tool",
  "conversation.chatCards.toolResult": "Tool result",

  // NativeSearch — saved conversation search
  "conversation.search.defaultForkName": "Conversation fork",
  // {error} is an error message.
  "conversation.search.updateFailed": "Could not update search: {error}. Available results are retained.",
  "conversation.search.starting": "Starting search…",
  // {error} is an error message.
  "conversation.search.startFailed": "Could not start search: {error}",
  // {error} is an error message.
  "conversation.search.stopFailed": "Could not stop search: {error}",
  "conversation.search.loadingMatch": "Loading matching message…",
  // {mode} is one of the three page descriptions below; {changed} and {incomplete} are the optional suffixes below (or empty).
  "conversation.search.readStatus": "{mode}{changed}{incomplete}.",
  "conversation.search.modeLatest": "Latest indexed messages",
  "conversation.search.modeSaved": "Saved messages",
  "conversation.search.modeMatch": "Matching message with nearby context",
  // {count} is a number of messages.
  "conversation.search.changedOmitted": " · {count} changed messages omitted",
  "conversation.search.indexIncomplete": " · indexing is incomplete",
  // {error} is an error message.
  "conversation.search.readFailed": "{error}. Return to results and search again.",
  // {found}, {progress}, {more} and {hint} are the pieces below (or empty).
  "conversation.search.summary": "{found}{progress}{more}{hint}",
  // {count} is the number of conversations found.
  "conversation.search.found.one": "{count} conversation found",
  "conversation.search.found.other": "{count} conversations found",
  // {count} is the number of agent profiles still being searched.
  "conversation.search.searching.one": " · searching {count} profile…",
  "conversation.search.searching.other": " · searching {count} profiles…",
  "conversation.search.incomplete": " · some profiles could not be fully searched",
  "conversation.search.more": " · more matches available; narrow your search",
  "conversation.search.tryDifferent": ". Try different words or filters.",
  "conversation.search.intro": "Search saved messages across workspaces, including conversations you no longer track.",
  "conversation.search.title": "Search saved conversations",
  "conversation.search.closeLabel": "Close saved conversation search",
  "conversation.search.close": "Close",
  "conversation.search.queryLabel": "Conversation text",
  "conversation.search.queryPlaceholder": "Find something discussed…",
  // A machine the agent runs on.
  "conversation.search.target": "Machine",
  "conversation.search.allTargets": "All machines",
  "conversation.search.agent": "Agent",
  "conversation.search.claudeAndCodex": "Claude and Codex",
  // Button: run the search (verb).
  "conversation.search.search": "Search",
  "conversation.search.stop": "Stop search",
  "conversation.search.retry": "Retry connection",
  // {count} is the number of agent profiles.
  "conversation.search.progress.one": "Machine progress · {count} profile",
  "conversation.search.progress.other": "Machine progress · {count} profiles",
  // {count} is the number of profiles with problems.
  "conversation.search.withIssues": " · {count} with issues",
  // {target}, {agent} and {state} come from the server; {documents} is a number.
  "conversation.search.scopeLine": "{target} · {agent}: {state} · {documents} conversations",
  // {count} is a number of files.
  "conversation.search.pending": " · {count} pending",
  // {count} is a number of entries.
  "conversation.search.oversized": " · {count} oversized entries skipped",
  "conversation.search.resultsLabel": "Saved conversation results",
  "conversation.search.options": "Search options",
  "conversation.search.rebuildHelp": "If a transcript was rewritten, rebuild its search index. Saved conversations remain unchanged.",
  "conversation.search.rebuild": "Rebuild and search",
  "conversation.search.back": "Back to results",
  "conversation.search.earlier": "Earlier messages",
  "conversation.search.later": "Later messages",
  "conversation.search.latestIndexed": "Latest indexed",
  "conversation.search.backToMatch": "Back to match",
  "conversation.search.fork": "Fork conversation",
  "conversation.search.toolActivity": "Tool activity",
  "conversation.search.matching": " · Matching message",
  "conversation.search.you": "You",
  "conversation.search.assistant": "Assistant",
  "conversation.search.longShortened": "Long message shortened in this view.",
  "conversation.search.forkTitle": "Fork saved conversation",
  "conversation.search.forkWarning": "Fork the whole saved conversation, including messages after the match.",
  "conversation.search.forkIsolated": "Start in a new Git worktree from the selected committed base; uncommitted changes stay in the original workspace.",
  "conversation.search.forkShared": "Both conversations will use the same workspace files.",
  "conversation.search.forkIntact": "The original conversation and terminal stay intact.",
  "conversation.search.launchSettings": "Launch settings",
  "conversation.search.chooseLaunch": "Choose launch settings",
  "conversation.search.sessionName": "Session name",
  "conversation.search.workspace": "Workspace",
  "conversation.search.forkWorkspace": "Fork workspace",
  "conversation.search.sameWorkspace": "Use the same workspace files",
  "conversation.search.isolatedWorktree": "New isolated Git worktree",
  "conversation.search.branch": "Branch (blank = automatic)",
  // HEAD is Git's name for the current commit; keep it as is.
  "conversation.search.base": "Base commit or branch (blank = HEAD)",
  "conversation.search.startingFork": "Starting fork…",
  "conversation.search.createFork": "Create fork",
  "conversation.search.cancelFork": "Cancel fork",

  // SavedConversations (NativeHistory)
  // {name} is the original session name.
  "conversation.saved.forkName": "{name} · fork",
  // {name} is the original session name.
  "conversation.saved.resumedName": "{name} · resumed",
  "conversation.saved.finding": "Finding saved conversations…",
  "conversation.saved.loading": "Loading saved messages…",
  "conversation.saved.choose": "Choose a saved conversation to see available actions.",
  "conversation.saved.none": "No saved conversations found in this workspace.",
  "conversation.saved.currentMarked": "The current terminal’s conversation is marked in the list.",
  "conversation.saved.currentUnsaved": "The current terminal has not saved readable messages yet.",
  "conversation.saved.ambiguous": "Several conversations are active in this terminal. Choose one explicitly.",
  "conversation.saved.unidentified": "The current conversation could not be identified. Choose one explicitly.",
  // {agent} is the agent name (e.g. Claude), {id} the conversation id.
  "conversation.saved.unreadable": "{agent} · {id} — Lectern can resume and fork this conversation but does not read its messages.",
  "conversation.saved.title": "Saved conversations",
  "conversation.saved.close": "Close",
  "conversation.saved.scanLimited": " Showing up to 500 discovered transcript files, prioritizing the current terminal.",
  "conversation.saved.conversation": "Conversation",
  "conversation.saved.chooseOption": "Choose a saved conversation",
  "conversation.saved.currentTerminal": "Current terminal · ",
  // Verb: reload the list.
  "conversation.saved.refresh": "Refresh",
  "conversation.saved.fork": "Fork conversation",
  "conversation.saved.resume": "Resume conversation",
  "conversation.saved.toolActivity": "Tool activity",
  "conversation.saved.longShortened": "Long message shortened in this view.",
  "conversation.saved.you": "You",
  "conversation.saved.assistant": "Assistant",
  "conversation.saved.loadEarlier": "Load earlier messages",
  "conversation.saved.confirmResume": "Continue this same saved conversation in its original workspace? The previous terminal must be stopped.",
  "conversation.saved.confirmIsolated": "Fork into a new Git worktree. Uncommitted changes stay in the original workspace.",
  "conversation.saved.confirmShared": "Create an independent conversation using the same workspace files.",
  "conversation.saved.newName": "New session name",
  "conversation.saved.sessionName": "Session name",
  "conversation.saved.workspace": "Workspace",
  "conversation.saved.sameFiles": "Use the same files",
  "conversation.saved.isolatedWorktree": "New isolated Git worktree",
  "conversation.saved.newBranch": "New branch (blank = automatic)",
  "conversation.saved.newBranchLabel": "New branch",
  // HEAD is Git's name for the current commit; keep it as is.
  "conversation.saved.base": "Base commit or branch (blank = HEAD)",
  "conversation.saved.baseLabel": "Base commit or branch",
  "conversation.saved.startResumed": "Start resumed session",
  "conversation.saved.createFork": "Create fork",
  "conversation.saved.cancel": "Cancel",
};

export default catalog;
