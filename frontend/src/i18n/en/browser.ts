// English strings for this area of the app. Keys are "area.name"; values may
// hold {placeholders}. Every other language has a file of the same name.
const catalog: Record<string, string> = {
  // Device presets (model.ts)
  "browser.device.phone": "Phone",
  "browser.device.tablet": "Tablet",
  // A screen-size preset, not the Desktop tab.
  "browser.device.desktop": "Desktop",

  // BrowserPane: file sizes
  "browser.size.mb": "{size} MB",
  "browser.size.kb": "{size} KB",
  "browser.size.bytes": "{size} B",

  // BrowserPane: notices
  "browser.notice.viewNotSecure": "This page is secure and the view is not, so it opens in its own tab.",
  "browser.notice.liveOnlyLocal": "A live page shows this machine's dev servers; other sites open in the shared browser.",
  "browser.notice.openPageFirst": "Open a page first.",
  "browser.notice.onlyOnAgentMachine": "This address is only reachable on the agent's machine.",
  "browser.notice.liveFind": "In a live page, use your own browser's find (Ctrl+F); it searches the frame too.",
  // {name} is the session's name; {files} is a number of files; {source} names where the screenshot came from.
  "browser.notice.designSentShot": "Sent to {name}: {files} files · screenshot from {source}",
  "browser.notice.designSentNoShot": "Sent to {name}: {files} files · no screenshot",
  "browser.notice.fetchFailed": "Could not fetch {name} ({status})",

  // BrowserPane: header and toolbar
  "browser.pane.label": "Browser for {name}",
  "browser.pane.tabBrowser": "Browser",
  // Tab showing the session's live desktops.
  "browser.pane.tabDesktop": "Desktop",
  "browser.pane.close": "Close browser",
  "browser.toolbar.back": "Back",
  "browser.toolbar.forward": "Forward",
  "browser.toolbar.reload": "Reload",
  "browser.toolbar.address": "Address",
  "browser.toolbar.addressPlaceholder": "localhost:5173, a port, or any address",
  // Verb: navigate to the typed address.
  "browser.toolbar.go": "Go",
  "browser.toolbar.ports": "Ports",
  "browser.toolbar.deviceSize": "Device size",
  // Toggles Design Mode.
  "browser.toolbar.design": "Design",
  "browser.toolbar.openInTab": "Open in a new tab",
  // Verb: search the page.
  "browser.toolbar.find": "Find",

  // BrowserPane: tab strip
  "browser.tabs.label": "Tabs",
  // Title of an empty browser tab.
  "browser.tabs.untitled": "New tab",
  // Button that opens a new browser tab.
  "browser.tabs.new": "New tab",
  "browser.tabs.close": "Close tab {title}",

  // BrowserPane: find in page
  "browser.find.label": "Find in page",
  "browser.find.previous": "Previous match",
  "browser.find.next": "Next match",
  "browser.find.position": "{index} of {matches}",
  "browser.find.none": "No matches",

  // BrowserPane: modes, profiles, cookies
  "browser.mode.shared": "Shared browser",
  "browser.mode.live": "Live page",
  "browser.mode.relayOnlyShared": "Over the relay only the shared browser can be shown",
  "browser.mode.liveTitle": "Frame the dev server directly",
  "browser.profile.where": "Profiles live in {where}",
  "browser.profile.label": "Profile",
  "browser.profile.select": "Browser profile",
  "browser.profile.temporary": "temporary (cleared on close)",
  "browser.profile.new": "New profile…",
  "browser.profile.prompt": "Name the new profile (lowercase letters, digits, - or _):",
  "browser.cookies.button": "Cookies",
  "browser.where.host": "Chromium on the Lectern host",
  "browser.where.session": "Chromium on the session's machine",
  "browser.cookies.label": "Import cookies",
  "browser.cookies.intro":
    "Sign the session's browser in by importing cookies into its current profile. Everything is read and decrypted on the session's machine; Lectern only sees how many were imported.",
  "browser.cookies.onlySitesOptional": "Only these sites (optional)",
  "browser.cookies.onlySites": "Only these sites",
  "browser.cookies.fromFile": "From a cookies file…",
  "browser.cookies.file": "Cookies file",
  "browser.cookies.chromeDir": "Chrome profile directory",
  "browser.cookies.chromeDirPlaceholder": "Chrome profile on that machine (auto)",
  "browser.cookies.fromChrome": "From Chrome",
  "browser.cookies.importing": "Importing…",
  // {skipped} is a list such as "3 expired, 1 invalid" whose reasons come from the server.
  "browser.cookies.imported": "Imported {imported} cookies for {sites} sites",
  "browser.cookies.importedSkipped": "Imported {imported} cookies for {sites} sites · skipped {skipped}",

  // BrowserPane: ports
  "browser.ports.label": "Listening ports",
  "browser.ports.looking": "Looking for listening ports…",
  "browser.ports.none": "Nothing is listening on this machine's localhost.",
  "browser.ports.thisWorkspace": "this workspace",
  "browser.ports.notHttp": "not http",
  "browser.ports.all": "All {total} ports",
  "browser.ports.lookAgain": "Look again",
  "browser.ports.open": "Open :{port}",

  // BrowserPane: who controls the browser
  "browser.control.agentDriving": "● The agent is driving",
  "browser.control.agentDrivingAction": "● The agent is driving: {action}",
  "browser.control.agentMay": "The agent may drive this browser",
  "browser.control.takeOver": "Take over",
  "browser.control.stopAgent": "Stop agent",
  "browser.control.userHas": "You have control. The agent waits.",
  "browser.control.handBack": "Hand back to agent",
  "browser.control.stopped": "Agent control is stopped.",
  "browser.control.allow": "Allow agent",

  // BrowserPane: the page
  "browser.stage.page": "Page: {title}",
  "browser.stage.sessionBrowser": "The session's browser",
  "browser.stage.connecting": "Connecting to the browser…",
  // {name} is the session's name.
  "browser.stage.sharedEmpty": "Pick a port or type an address. It opens in a real browser on {name}'s machine, which the agent can drive too.",
  "browser.stage.frameTitle": "Dev server for {name}",
  "browser.stage.liveEmpty": "Pick a port or type a localhost address to frame the dev server directly.",

  // BrowserPane: downloads
  "browser.downloads.label": "Downloads",
  "browser.downloads.canceled": "canceled",
  "browser.downloads.progress": "{received}…",
  "browser.downloads.progressOf": "{received} of {total}…",
  // Verb: save the downloaded file.
  "browser.downloads.save": "Save",

  // BrowserPane: Design Mode
  "browser.design.label": "Design Mode",
  "browser.design.hint": "Hover to see an element's path; click to pick it. Shift-click adds more.",
  "browser.design.pickSeveral": "Pick several",
  "browser.design.elementInfo": "{width}×{height} · {styles} styles",
  // {selector} is a CSS selector.
  "browser.design.remove": "Remove {selector}",
  "browser.design.note": "Note for the agent",
  "browser.design.notePlaceholder": "What should change? (optional)",
  "browser.design.sending": "Sending…",
  // {picked} is the number of picked elements, or empty.
  "browser.design.send": "Send {picked} to agent",

  // BrowserPane: live desktops
  "browser.desk.stopped": "Agent control stopped.",
  "browser.desk.agentActive": "● The agent is controlling this desktop: {action}",
  "browser.desk.agentMay": "The agent may control this desktop.",
  "browser.desk.off": "Computer use is off for this desktop.",
  "browser.desk.allow": "Allow agent control",
  // {display} is a display name such as ":1".
  "browser.desk.alt": "Desktop {display}",
  "browser.desk.capturing": "Capturing…",
};

export default catalog;
