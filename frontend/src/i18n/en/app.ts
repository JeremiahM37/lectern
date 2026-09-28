// English strings for this area of the app. Keys are "area.name"; values may
// hold {placeholders}. Every other language has a file of the same name.
const catalog: Record<string, string> = {
  // Overview (LiveViews Deck)
  "app.deck.badEvent": "Could not read a live event.",
  "app.deck.empty": "No tasks are running.",
  "app.deck.emptyHint": "Overview shows background tasks side by side while they run. Your sessions are on the Sessions page.",

  // Approvals (LiveViews)
  "app.approvals.approved": "Approved — agent continuing",
  "app.approvals.denied": "Denied — agent notified",
  // Heading reads "<tool name> wants to run".
  // {id} is a task number, {title} the task title.
  "app.approvals.empty": "Nothing needs you right now.",
  "app.approvals.emptyHint": "When an agent wants to do something risky, it waits here for you, and your phone gets an alert.",

  // Palette
  "app.palette.refreshFailed": "Could not refresh. Showing available results.",
  "app.palette.dialogLabel": "Search Lectern",
  "app.palette.close": "Close search",
  "app.palette.results.one": "{count} result",
  "app.palette.results.other": "{count} results",
  "app.palette.showingFirst": " · showing the first 80; type to narrow",
  "app.palette.updating": " · updating…",
  "app.palette.searching": "Searching…",
  "app.palette.noMatches": "No matches. Try a session name, project, or action.",
  "app.palette.resultsLabel": "Search results",

  // Continuity (handoff)
  // Fallback name for a session whose agent is unknown.
  "app.continuity.session": "Session",
  // {agent} is an agent name such as Claude.
  "app.continuity.defaultModel": "{agent} · default model",
  "app.continuity.didNotStart": "The context was saved, but the new session did not start. Your original session is still available.",
  "app.continuity.stoppedEarly": "The switch stopped before a new session started. Your original session is still available.",

  // SessionLineage
  // {name} is a session name.
  "app.lineage.goBack": "Go back to {name}",
  "app.lineage.open": "Open {name}",
  "app.lineage.session": "Session {id}",
  "app.lineage.sessionLower": "session {id}",
  "app.lineage.ended": " · ended",
  "app.lineage.openChat": "Open chat for {name}",
  // Button: opens the chat view (noun).
  "app.lineage.chat": "Chat",
  "app.lineage.label": "Conversation lineage",
  "app.lineage.saving": "Saving context…",

  // SwitchProgress
  "app.switch.saving": "Saving context",
  // {destination} is a model or provider name.
  "app.switch.starting": "Starting {destination}",
  "app.switch.startingNew": "Starting the new session",
  "app.switch.ready": "Ready",
  "app.switch.incomplete": "The switch did not complete. Your original session is still available.",
  "app.switch.progressLabel": "Switch session {id} progress",
  "app.switch.openFailed": "New session started — could not open it",
  "app.switch.failed": "Switch failed",
  "app.switch.switchingTo": "Switching to {destination}",
  "app.switch.switching": "Switching",
  "app.switch.dismiss": "Dismiss switch progress",
  "app.switch.openNew": "Open new session",
  "app.switch.retry": "Retry",

  // Usage limits (limit-label, LimitBanner)
  // {account} is an account label; {agent} an agent or provider name; {time} a clock time such as "3:40pm".
  "app.limit.anotherAccount": "another account",
  "app.limit.swappedResuming": "Swapped to {account} — resuming…",
  "app.limit.resetResuming": "Limit reset — resuming…",
  "app.limit.swapping": "Limit — swapping to {account}…",
  "app.limit.continuesOn": "Limit — continues on {account}",
  "app.limit.handingOffTo": "Limit — handing off to {agent}…",
  "app.limit.handingOff": "Limit — handing off…",
  "app.limit.resumes": "Limit — resumes {time}",
  "app.limit.resumesAfterReset": "Limit — resumes after the reset",
  "app.limit.readyToResume": "Limit reset — ready to resume",
  "app.limit.resets": "Limit — resets {time}",
  "app.limit.resetUnknown": "Limit — reset time unknown",
  "app.limit.resumeAtReset": "Resume at reset",
  "app.limit.resumeNow": "Resume now",
  "app.limit.handOffTo": "Hand off to {agent}",
  "app.limit.handOff": "Hand off…",
  "app.limit.swapTo": "Swap to {account}",
  "app.limit.dismiss": "Dismiss",
  "app.limit.noFallback": "Set a fallback agent in the project's usage-limit policy to hand off.",
  "app.limit.dismissed": "Limit dismissed.",
  "app.limit.handingOffNotice": "Handing off to {agent}.",
  "app.limit.swappingNotice": "Swapping to {account}.",
  "app.limit.resumingNow": "Resuming now.",
  "app.limit.willResume": "Lectern will resume it after the reset.",

  // Pair (device pairing page) and QRCode
  // Suggested names for this device; the person can edit them.
  "app.pair.androidPhone": "Android phone",
  "app.pair.androidTablet": "Android tablet",
  "app.pair.windowsPc": "Windows PC",
  "app.pair.myDevice": "My device",
  // {status} is an HTTP status code.
  "app.pair.failed": "Pairing failed ({status})",
  // Heading and button: "pair" is a verb.
  "app.pair.title": "Pair this device",
  "app.pair.intro": "Enter the code shown on your Lectern's Settings → Devices page, or scan its QR code with your camera.",
  "app.pair.code": "Pairing code",
  "app.pair.name": "Name this device",
  "app.pair.pairing": "Pairing…",
  "app.pair.submit": "Pair device",
  "app.pair.qrLabel": "Pairing QR code — scan with your phone's camera",

  // RelayPair
  "app.relayPair.defaultName": "Relay device",
  "app.relayPair.titleShort": "Pair over the relay",
  "app.relayPair.noCode": "This link has no pairing code. Open Settings → Devices → Encrypted relay on your Lectern and scan a fresh QR code.",
  "app.relayPair.title": "Pair over the encrypted relay",
  // Followed by the relay's host name, then app.relayPair.throughAfter.
  "app.relayPair.through": "This phone will reach Lectern through ",
  "app.relayPair.throughAfter": ". The relay only passes encrypted messages; it cannot read or change them.",
  // Followed by a key fingerprint.
  "app.relayPair.key": "Lectern's key: ",
  "app.relayPair.keyMatch": "It should match the one shown under the QR code.",
  "app.relayPair.paired": "Paired",

  // RelayBanner
  // {detail} is a sentence explaining the reason (may be empty).
  "app.relayBanner.revoked": "This device is no longer paired. {detail} Pair it again from Settings → Devices on your Lectern.",
  "app.relayBanner.reaching": "Reaching Lectern through the encrypted relay… {detail}",

  // Relay connection messages (store, tunnel)
  "app.relay.noServiceWorker": "This browser cannot install the app (no service worker support).",
  "app.relay.notInstalled": "The app is not installed yet; reload and try again.",
  "app.relay.installUnfinished": "the app did not finish installing",
  // {error} is a reason such as app.relay.installUnfinished.
  "app.relay.pinFailed": "Could not pin the app shell: {error}",
  "app.relay.unknownError": "unknown error",
  "app.relay.unrecognised": "The relay did not recognise this device.",
  "app.relay.hostOffline": "Lectern is not connected to the relay.",
  "app.relay.timedOut": "The relay timed out.",
  "app.relay.busy": "The relay is busy; retrying.",
  "app.relay.dropped": "The relay connection dropped.",
  "app.relay.noAnswer": "The relay did not answer in time.",
  "app.relay.keyMissing": "This device's key is missing; pair it again.",
  "app.relay.newerCode": "This pairing code is from a newer Lectern.",
  "app.relay.codeUsed": "That pairing code was already used or has expired.",
  "app.relay.incomplete": "Lectern did not complete the pairing.",
  "app.relay.notPaired": "This device is not paired.",
  "app.relay.appTooOld": "This app is older than Lectern; reinstall it.",
  "app.relay.revoked": "Lectern no longer recognises this device. It was revoked or idle too long.",
  "app.relay.refused": "The relay no longer accepts this device. It was probably revoked.",

  // AllAgentsPicker
  "app.allAgents.title": "All agents",
  "app.allAgents.close": "Close",
  "app.allAgents.builtIn": " · built in",
  "app.allAgents.none": "No agents are registered yet.",

  // Push availability (push.ts)
  "app.push.insecure": "Open Lectern over https to enable alerts.",
  "app.push.iosNotInstalled": "On iPhone/iPad, add Lectern to the Home Screen first (Share → Add to Home Screen), then enable alerts from there.",
  "app.push.unsupported": "This browser does not support push notifications.",

  // Android app push (native/push.ts)
  "app.nativePush.notInApp": "not running in the Lectern app",
  "app.nativePush.noDistributor": "the push distributor did not answer; is ntfy (or another UnifiedPush app) installed?",
  "app.nativePush.failed": "push registration failed",

  // App: switching agents
  "app.switch.switched": "Switched. The original session is still available in Sessions.",
  "app.switch.waiting": "Switching… waiting for the current agent to save its handoff.",
  "app.switcher.label": "Switch agent or model",
  // {model} is the current model, profile or agent name.
  "app.switcher.title": "Switch {model}",
  "app.switcher.switching": "Switching…",

  // App: handoff results
  // Pieces of one line: "Handoff written — successor session started · remembered".
  "app.handoff.written": "Handoff written",
  "app.handoff.successorStarted": " — successor session started",
  "app.handoff.remembered": " · remembered",
  "app.handoff.failed": "Handoff failed: {error}",
  "app.handoff.unknown": "unknown",
  "app.handoff.unreadable": "Could not read handoff result.",

  // App: service worker and push notifications
  "app.offlineSupportError": "Offline support: {error}",
  "app.pushPrompt.enabled": "Push enabled on this device — sending a test notification",
  "app.pushPrompt.error": "Phone alerts are not on: {error}",
  "app.pushPrompt.unsupported": "this browser can't show notifications here. Install Lectern as an app, or open it over https.",
  "app.pushPrompt.notGranted": "this browser blocked notifications for Lectern. Allow them in the site settings (the icon next to the address), then try again.",
  "app.pushPrompt.testError": "Test notification: {error}",
  "app.pushPrompt.unsubscribed": "Unsubscribed",
  "app.pushPrompt.unsubscribeError": "Unsubscribe: {error}",

  // App: command palette entries
  "app.commands.routines": "Routines",
  "app.commands.routinesDetail": "Saved jobs and active runs",
  "app.commands.newSession": "New session",
  "app.commands.newSessionDetail": "Start an interactive agent",
  "app.newTask": "New task",
  "app.commands.newTaskDetail": "Plan or dispatch work",
  "app.commands.savedSearch": "Search saved conversations",
  "app.commands.discover": "Find running agents",
  "app.commands.launchProfiles": "Manage launch profiles",
  // "Open" is an adjective here: the terminals that are open.
  "app.commands.openTerminals": "Open terminals",
  // Category of commands that go to a page.
  "app.commands.navigate": "Navigate",
  "app.commands.targets": "Machines",
  "app.commands.projects": "Projects",
  "app.commands.notifications": "Notifications",
  "app.commands.devices": "Devices",
  "app.commands.about": "Usage and about",
  "app.commands.agents": "Agents",
  "app.commands.plugins": "Plugins",
  "app.commands.sessions": "Sessions",
  "app.commands.task": "Task {id}",
  "app.commands.tasks": "Tasks",
  "app.commands.editProject": "Edit project: {name}",
  "app.commands.projectsCategory": "Projects",

  // App chrome
  "app.liveConnection": "live connection",
  "app.fabTitle": "new task",
  // {n} is the number of live views.
  "app.liveCount": "{n} live",
  "app.morePages": "More pages",
  "app.forkStarted": "Fork workspace setup started. Follow progress in Sessions.",

  // Access-token dialog
  "app.token.title": "Access token",
  "app.token.required": "This Lectern requires an access token.",
  "app.token.connect": "Connect",
};

export default catalog;
