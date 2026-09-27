// English strings for this area of the app. Keys are "area.name"; values may
// hold {placeholders}. Every other language has a file of the same name.
// The find bar, appearance dialog and snippets dialog use "terminal.*" and
// "quick.*" keys from core.ts; everything else on the terminal page is here.
const catalog: Record<string, string> = {
  // Connection status (pane title, header, engine)
  "terminalPage.status.connecting": "Connecting…",
  // Same as "Connecting…" but without the ellipsis; shown as a tooltip and in the header.
  "terminalPage.status.connectingPlain": "Connecting",
  "terminalPage.status.connected": "Connected",
  "terminalPage.status.offline": "Offline",
  "terminalPage.status.reconnecting": "Reconnecting…",
  "terminalPage.status.terminalConnected": "Terminal connected",
  "terminalPage.status.terminalOffline": "Terminal offline",
  "terminalPage.status.terminalReconnecting": "Terminal reconnecting",

  // Pane labels
  "terminalPage.pane.projectShell": "Project shell",
  // Noun: the pane running the AI coding agent.
  "terminalPage.pane.agent": "Agent",
  "terminalPage.pane.companionShell": "Companion shell",

  // Header and Tools menu
  "terminalPage.header.back": "Back to Lectern",
  "terminalPage.tools.navLabel": "Terminal tools",
  "terminalPage.tools.attach": "Attach files",
  // Noun: opens the workspace file browser.
  "terminalPage.tools.files": "Files",
  "terminalPage.tools.openInTerminal": "Open in terminal",
  "terminalPage.tools.toggle": "Tools",
  "terminalPage.tools.pausedToggle": "Paused · Tools",
  // Keeps its leading space; follows "Tools".
  "terminalPage.tools.hint": " · Ctrl+] then m",
  // Followed by the key "Ctrl+]".
  "terminalPage.tools.helpControls": "Controls:",
  // Between two keys: "Ctrl+] then m".
  "terminalPage.tools.helpThen": "then",
  "terminalPage.tools.helpEsc": "Esc returns to typing. Ctrl+] twice sends Ctrl+].",
  "terminalPage.tools.compose": "Write or paste text",
  "terminalPage.tools.savedReplies": "Saved replies",
  "terminalPage.tools.searchSaved": "Search saved conversations",
  "terminalPage.tools.saved": "Saved conversations",
  "terminalPage.tools.desktopSetup": "Terminal connection setup",
  "terminalPage.tools.mcpSettingsLabel": "Open project MCP settings",
  "terminalPage.tools.mcpSettings": "Project MCP settings",
  "terminalPage.tools.reportKeyboard": "Report keyboard layout",
  "terminalPage.tools.hideShell": "Hide shell",
  "terminalPage.tools.splitShell": "Split shell",
  "terminalPage.tools.appearance": "Appearance",
  "terminalPage.tools.resumeView": "Resume view",
  "terminalPage.tools.pauseView": "Pause view",
  "terminalPage.tools.jumpToLive": "Jump to live",
  "terminalPage.tools.reconnectHint": "Reconnect this view and redraw the terminal; the agent keeps running",
  "terminalPage.tools.reconnect": "Reconnect view",

  // Notices
  // {error} is a message from the server.
  "terminalPage.notice.reviveFailed": "Could not restart the agent: {error}",
  "terminalPage.notice.uploadsUnavailable": "Uploads unavailable for this workspace.",
  "terminalPage.notice.connectBeforeUpload": "Connect to the terminal before uploading.",
  // {name} is a file name.
  "terminalPage.notice.tooLarge": "{name}: exceeds 25 MiB",
  "terminalPage.notice.uploading": "Uploading {name}…",
  "terminalPage.notice.uploaded": "{name} uploaded. Path inserted; press Enter when ready.",
  // {path} is where the file was saved; {error} explains why it was not inserted.
  "terminalPage.notice.uploadedTo": "Uploaded to {path}. {error}",
  "terminalPage.notice.copiedScreen": "Copied the whole screen buffer.",
  "terminalPage.notice.copiedReport": "Copied. Paste it into a message.",
  "terminalPage.notice.noOlderOutput": "No older output is retained in tmux. Full-screen chats may keep their history inside the app.",
  "terminalPage.notice.forkStarted": "Fork started. Find it in Sessions; this terminal stays attached.",
  "terminalPage.errors.disconnected": "Terminal disconnected. Reconnect before inserting a path.",
  "terminalPage.errors.clipboard": "Clipboard access is unavailable. Use your browser's Copy command.",

  // Pinch-to-zoom hint; {size} is like "15px".
  "terminalPage.fontHint": "{size} · {columns} columns",

  // Selection bar (paused view on a phone)
  "terminalPage.select.label": "Text selection",
  "terminalPage.select.hint": "Hold any text to select it",
  "terminalPage.select.copyAll": "Copy all",
  "terminalPage.select.done": "Done",

  // Unresponsive / exited agent banners
  "terminalPage.unresponsive.title": "The agent is not reacting to your keystrokes.",
  "terminalPage.unresponsive.body": "It is still running, but it has taken several keys and drawn nothing back.",
  "terminalPage.unresponsive.bodySession": "It is still running, but it has taken several keys and drawn nothing back — its process has probably hung. Restarting it resumes this same conversation.",
  "terminalPage.unresponsive.restarting": "Restarting…",
  "terminalPage.unresponsive.restart": "Restart agent, keep conversation",
  "terminalPage.banner.dismiss": "Dismiss",
  "terminalPage.exited.title": "The agent exited.",
  "terminalPage.exited.body": "This terminal is at a shell prompt. Revive starts the agent again here, resuming its conversation when one was saved.",
  "terminalPage.exited.reviving": "Reviving…",
  "terminalPage.exited.revive": "↻ Revive",
  // Button returning to live output.
  "terminalPage.returnLive": "↓ Live",

  // Key bar
  "terminalPage.keybar.label": "Terminal keys",
  "terminalPage.keybar.hideKeyboard": "Hide keyboard",
  "terminalPage.keybar.showKeyboard": "Show keyboard",
  "terminalPage.keybar.holdCtrl": "Hold Ctrl for the next key",
  "terminalPage.keybar.holdAlt": "Hold Alt for the next key",
  "terminalPage.keybar.escape": "Send Escape",
  "terminalPage.keybar.tab": "Send Tab",
  "terminalPage.keybar.backtab": "Send Shift-Tab",
  "terminalPage.keybar.left": "Send Left arrow",
  "terminalPage.keybar.up": "Send Up arrow",
  "terminalPage.keybar.down": "Send Down arrow",
  "terminalPage.keybar.right": "Send Right arrow",
  "terminalPage.keybar.interrupt": "Send Ctrl-C",
  "terminalPage.keybar.slash": "Send slash",
  "terminalPage.keybar.dash": "Send dash",
  "terminalPage.keybar.pipe": "Send pipe",
  "terminalPage.keybar.tilde": "Send tilde",
  "terminalPage.keybar.home": "Send Home",
  "terminalPage.keybar.end": "Send End",
  "terminalPage.keybar.pageup": "Send Page Up",
  "terminalPage.keybar.pagedown": "Send Page Down",

  // Compose dialog
  "terminalPage.compose.help": "Edit a longer prompt here. Insert puts it in the terminal; Send also presses Enter.",
  "terminalPage.compose.textLabel": "Text to insert in terminal",
  // Verb: puts the text in the terminal without pressing Enter.
  "terminalPage.compose.insert": "Insert",
  "terminalPage.compose.send": "Send ↵",

  // Keyboard report dialog (the report itself stays English: it is a diagnostic to paste)
  "terminalPage.keyboardReport.title": "Keyboard layout report",
  // Verb.
  "terminalPage.keyboardReport.copy": "Copy",
  "terminalPage.keyboardReport.help": "The exact numbers this device's browser reports right now — paste this into a message rather than describing what you see.",

  // Shared buttons
  // Verb: reload.
  "terminalPage.common.refresh": "Refresh",
  // Verb.
  "terminalPage.common.download": "Download",

  // HistoryDialog
  "terminalPage.history.title": "Session history",
  "terminalPage.history.placeholder": "Search full tmux history",
  "terminalPage.history.searchLabel": "Search full history",
  "terminalPage.history.loading": "Loading history…",
  // {lines} is a formatted number.
  "terminalPage.history.snapshotAgent": "Snapshot of agent tmux history · up to {lines} retained lines",
  "terminalPage.history.snapshotShell": "Snapshot of companion shell tmux history · up to {lines} retained lines",
  // Appended to the snapshot line; keeps its leading space.
  "terminalPage.history.truncated": " · limited to final 8 MiB",

  // PDFPreview
  "terminalPage.pdf.previous": "Previous page",
  "terminalPage.pdf.next": "Next page",
  "terminalPage.pdf.pageOf": "Page {page} of {pages}",
  "terminalPage.pdf.loading": "Loading PDF…",
  "terminalPage.pdf.pageLabel": "PDF page",
  "terminalPage.pdf.canvasUnavailable": "Canvas unavailable",

  // WorkspaceFiles
  "terminalPage.files.title": "Workspace files",
  "terminalPage.files.parent": "↑ Parent",
  // Noun: the row is a directory.
  "terminalPage.files.folder": "folder",
  "terminalPage.files.size": "{size} KiB",
  "terminalPage.files.insertPath": "Insert path",
  "terminalPage.files.empty": "This folder is empty.",
  "terminalPage.files.limits": "Up to 500 entries per folder · previews and downloads up to 25 MiB",
  "terminalPage.files.previewLimited": "Preview limited to first 1 MiB. Download for the full file.",
  "terminalPage.files.preview": "Preview",
  "terminalPage.files.binary": "Binary file. Use Download to open it on your device.",

  // Desktop setup dialog
  "terminalPage.desktop.title": "Open in your terminal",
  "terminalPage.desktop.intro": "Attach to this same session in your default terminal. Closing either terminal leaves the session running.",
  "terminalPage.desktop.firstTime": "First-time setup",
  "terminalPage.desktop.defaultTerminal": "Your default terminal",
  "terminalPage.desktop.runLinux": "Download the launcher, then run:",
  "terminalPage.desktop.runWindows": "Download the launcher, then run in PowerShell:",
  "terminalPage.desktop.downloadLinux": "Download Linux setup",
  "terminalPage.desktop.downloadWindows": "Download Windows setup",
  // Followed by the command name "lectern".
  "terminalPage.desktop.aliasBefore": "Both use your",
  // Follows the command name "lectern".
  "terminalPage.desktop.aliasAfter": "SSH alias. Your terminal theme and existing sessions stay intact.",
  "terminalPage.desktop.instructions": "Connection and setup instructions ↗",
  "terminalPage.desktop.cliSummary": "Manage Lectern entirely from a terminal",
  // Followed by the command "lectern".
  "terminalPage.desktop.cliBefore": "Install the terminal client, then run",
  // Follows the command "lectern"; starts with the sentence's full stop.
  "terminalPage.desktop.cliAfter": ". Sessions, tasks, routines, settings and context uploads are available without the web UI.",
  "terminalPage.desktop.linuxInstaller": "Linux client installer",
  "terminalPage.desktop.windowsInstaller": "Windows client installer",
  "terminalPage.desktop.manualSummary": "Connect from an already-open terminal",
  "terminalPage.desktop.runThis": "Run this in your terminal:",
  "terminalPage.desktop.manualLabel": "Manual SSH command",
  "terminalPage.desktop.copyCommand": "Copy command",
  "terminalPage.desktop.copied": "SSH command copied.",

  // Review
  "terminalPage.review.title": "Review changes",
  "terminalPage.review.noTextual": "No textual changes (the file may have been renamed or its permissions changed).",
  "terminalPage.review.close": "Close review",
  "terminalPage.review.repository": "Repository",
  // Label for the working-tree / staged selector.
  "terminalPage.review.changes": "Changes",
  // {files} is a number of files.
  "terminalPage.review.working": "Working tree ({files})",
  "terminalPage.review.staged": "Staged ({files})",
  "terminalPage.review.wrap": "Wrap lines",
  "terminalPage.review.loading": "Loading changes…",
  "terminalPage.review.truncated": "Large diff: showing the first 512 KiB. Open the file for the remainder.",
  // {files} is a number; {time} is a clock time.
  "terminalPage.review.summary": "{files} changed files · Updated {time}",
  "terminalPage.review.filter": "Find a changed file",
  "terminalPage.review.changedFiles": "Changed files",
  "terminalPage.review.noMatching": "No matching files.",
  "terminalPage.review.noChanges": "No changes in this view.",
  "terminalPage.review.noFile": "No file selected",
  "terminalPage.review.diff": "File diff",
  "terminalPage.review.loadFailed": "Changes could not be loaded. Refresh to try again.",
  "terminalPage.review.nothing": "Nothing to review here. Check the other changes view or refresh after editing.",
  "terminalPage.review.footer": "Read-only snapshot · Refresh to see the agent’s latest edits",

  // Terminal page (main.tsx)
  "terminalPage.main.loading": "Loading…",
  // Noun: dialog title.
  "terminalPage.main.terminal": "Terminal",

  // Terminal tabs and the New terminal picker
  "terminalPage.tabs.invalidAddress": "This terminal does not have a valid Lectern address.",
  // {label} is the tab's name.
  "terminalPage.tabs.frameTitle": "{label} terminal",
  "terminalPage.tabs.newTerminal": "New terminal",
  "terminalPage.tabs.newHint": "Open a blank shell on the default machine",
  "terminalPage.tabs.opening": "Opening…",
  "terminalPage.tabs.newElsewhere": "New terminal in a project or on another machine",
  "terminalPage.tabs.search": "Search projects and machines",
  "terminalPage.tabs.recent": "Recent",
  "terminalPage.tabs.otherProjects": "Other projects",
  "terminalPage.tabs.projects": "Projects",
  "terminalPage.tabs.machines": "Machines",
  "terminalPage.tabs.noMatches": "No matching projects or machines",
};

export default catalog;
