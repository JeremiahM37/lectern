// English strings for this area of the app. Keys are "area.name"; values may
// hold {placeholders}. Every other language has a file of the same name.
const catalog: Record<string, string> = {
  // Shared across the Tasks hub
  "trackers.cancel": "Cancel",
  // Verb: close a pull request or dialog.
  "trackers.close": "Close",
  // Verb: reopen a closed item.
  "trackers.reopen": "Reopen",
  "trackers.loading": "Loading…",
  "trackers.starting": "Starting…",
  "trackers.startSession": "Start session",
  // Badge on a pull request whose branch has merge conflicts.
  "trackers.conflictsBadge": "conflicts",
  "trackers.labels": "Labels",
  "trackers.labels.none": "None.",
  "trackers.labels.add": "Add label",
  // {name} is a label name.
  "trackers.labels.remove": "Remove label {name}",
  "trackers.description": "Description",
  "trackers.description.none": "No description.",
  // Notices after an action. {name} is a label or a person's login.
  "trackers.notice.removed": "Removed {name}",
  "trackers.notice.labelled": "Labelled {name}",
  "trackers.notice.commentPosted": "Comment posted",
  // Notice: the item is now closed / reopened.
  "trackers.notice.closed": "Closed",
  "trackers.notice.reopened": "Reopened",

  // logic.ts: merging
  "trackers.merge.method.merge": "Create a merge commit",
  "trackers.merge.method.squash": "Squash and merge",
  "trackers.merge.method.rebase": "Rebase and merge",
  "trackers.merge.enableAuto": "Enable auto-merge",
  "trackers.merge.addToQueue": "Add to merge queue",
  // {state} is "closed" or "merged".
  "trackers.merge.block.state": "This pull request is {state}.",
  "trackers.merge.block.draft": "Drafts cannot be merged. Mark it ready for review first.",
  "trackers.merge.block.permission": "Your login on this machine cannot merge into this repository.",
  "trackers.merge.block.conflicts": "This branch has conflicts that must be resolved first.",
  "trackers.merge.warn.checksFailing": "Some checks are failing.",
  "trackers.merge.warn.checksRunning": "Checks are still running.",
  "trackers.merge.warn.changesRequested": "A reviewer requested changes.",
  "trackers.merge.warn.reviewRequired": "A review is still required.",

  // logic.ts: board column titles
  "trackers.column.noStatus": "No status",
  "trackers.column.merged": "Merged",
  "trackers.column.closed": "Closed",
  "trackers.column.draft": "Draft",
  "trackers.column.checksFailing": "Checks failing",
  "trackers.column.conflicts": "Conflicts",
  "trackers.column.approved": "Approved",
  "trackers.column.changesRequested": "Changes requested",
  "trackers.column.inReview": "In review",
  "trackers.column.open": "Open",

  // logic.ts: relative times ({n} is a number)
  "trackers.ago.justNow": "just now",
  "trackers.ago.minutes": "{n}m ago",
  "trackers.ago.hours": "{n}h ago",
  "trackers.ago.days": "{n}d ago",

  // bits: state pill, badges
  // Pull request / issue state (adjectives).
  "trackers.state.open": "open",
  "trackers.state.closed": "closed",
  "trackers.state.merged": "merged",
  "trackers.state.draft": "draft",
  "trackers.checks.pass": "checks pass",
  "trackers.checks.fail": "checks failing",
  "trackers.checks.pending": "checks running",
  // Screen-reader names for a single check's status icon.
  "trackers.checkStatus.pass": "pass",
  "trackers.checkStatus.fail": "fail",
  "trackers.checkStatus.pending": "pending",
  "trackers.checkStatus.skipping": "skipping",
  "trackers.checkStatus.cancel": "cancel",
  "trackers.checkStatus.none": "none",
  "trackers.review.approved": "approved",
  "trackers.review.changesRequested": "changes requested",
  "trackers.review.reviewRequired": "review required",

  // bits: Timeline
  "trackers.timeline.empty": "No conversation yet.",
  // Stands in for an unknown comment author.
  "trackers.timeline.someone": "someone",
  // Follows a person's name and precedes a commit hash: "sam committed abc1234 Fix…".
  "trackers.timeline.committed": "committed",
  // These follow a reviewer's name: "sam approved these changes".
  "trackers.timeline.review.approved": "approved these changes",
  "trackers.timeline.review.changesRequested": "requested changes",
  "trackers.timeline.review.commented": "reviewed",
  "trackers.timeline.review.dismissed": "had a review dismissed",
  // Noun: the comment text field.
  "trackers.timeline.commentField": "Comment",
  "trackers.timeline.placeholder": "Leave a comment",
  "trackers.timeline.posting": "Posting…",
  // Verb: button that posts the comment.
  "trackers.timeline.comment": "Comment",

  // bits: NamePicker
  "trackers.picker.placeholder": "Type a name",
  "trackers.picker.add": "Add",

  // StartWork
  // {mark} is an item id such as "#12" or "ENG-4".
  "trackers.start.title": "Start work on {mark}",
  "trackers.start.startAs": "Start as",
  "trackers.start.session": "Session",
  "trackers.start.sessionHint": "interactive, own worktree",
  "trackers.start.task": "Task",
  "trackers.start.taskHint": "on the board, runs headless",
  "trackers.start.agent": "Agent",
  "trackers.start.projectDefault": "Project default",
  "trackers.start.prHint": "The session starts in a new worktree at the pull request's head and is told how to push to it.",
  "trackers.start.branch": "Branch",
  "trackers.start.baseBranch": "Base branch",
  "trackers.start.basePlaceholder": "project default",
  "trackers.start.note": "Note for the agent (optional)",
  "trackers.start.notePlaceholder": "Anything the issue does not say",
  "trackers.start.dispatchNow": "Dispatch now",
  "trackers.start.createAndDispatch": "Create and dispatch task",
  "trackers.start.createTask": "Create task",

  // TasksHub
  "trackers.hub.filter.open": "Open",
  "trackers.hub.filter.assigned": "Assigned to me",
  "trackers.hub.filter.review": "Review requested",
  "trackers.hub.filter.authored": "Created by me",
  "trackers.hub.filter.closed": "Closed",
  // Filter: items in any state.
  "trackers.hub.filter.all": "All",
  "trackers.hub.view.list": "List",
  "trackers.hub.view.board": "Board",
  "trackers.hub.view.table": "Table",
  "trackers.hub.startedSession": "Started session \"{name}\"",
  "trackers.hub.startedSessionOn": "Started session \"{name}\" on {branch}",
  "trackers.hub.createdTask": "Created task #{id}",
  "trackers.hub.title": "Issues & PRs",
  "trackers.hub.noProjects": "Add a project first; its repository's pull requests and issues show up here.",
  "trackers.hub.subtitle": "Pull requests and issues from GitHub, GitLab, Linear and Jira",
  "trackers.hub.project": "Project",
  "trackers.hub.refresh": "Refresh",
  "trackers.hub.connect": "Connect…",
  "trackers.hub.source": "Source",
  // Tab: items from every source.
  "trackers.hub.tab.all": "All",
  "trackers.hub.tab.pullRequests": "Pull requests",
  "trackers.hub.tab.mergeRequests": "Merge requests",
  "trackers.hub.tab.issues": "Issues",
  "trackers.hub.filter": "Filter",
  "trackers.hub.linearTeam": "Linear team",
  "trackers.hub.search": "Search tasks",
  "trackers.hub.searchPlaceholder": "Search title, id, branch, person, label",
  "trackers.hub.view": "View",
  "trackers.hub.setUp": "Set up",
  "trackers.hub.empty": "Nothing here.",
  "trackers.hub.emptySearch": "Nothing here matching that search.",
  // {name} is the author's login.
  "trackers.hub.by": "by {name}",
  "trackers.hub.col.id": "ID",
  "trackers.hub.col.title": "Title",
  "trackers.hub.col.status": "Status",
  "trackers.hub.col.assignees": "Assignees",
  "trackers.hub.col.updated": "Updated",
  "trackers.hub.details": "Details",

  // PRPage and IssuePage: header bar
  "trackers.detail.backToList": "Back to the list",
  "trackers.detail.back": "Back",
  // Link that opens the item on its tracker's own site.
  "trackers.detail.openExternal": "Open ↗",
  "trackers.detail.retry": "Retry",
  // {mark} is an item id such as "#12" or "ENG-4".
  "trackers.detail.loading": "Loading {mark}…",
  // {ago} is a relative time such as "3h ago".
  "trackers.detail.updated": "updated {ago}",

  // PRPage
  // {source} is GitHub or GitLab (may be empty while loading).
  "trackers.pr.kind": "{source} pull request",
  "trackers.pr.notice.autoOn": "Auto-merge is on for #{id}",
  "trackers.pr.notice.queued": "#{id} added to the merge queue",
  "trackers.pr.notice.merged": "Merged #{id}",
  "trackers.pr.notice.autoOff": "Auto-merge turned off",
  // {name} is a person's login.
  "trackers.pr.notice.askedReview": "Asked {name} to review",
  "trackers.pr.loadingLog": "Loading log…",
  "trackers.pr.emptyLog": "(empty log)",
  // Sits between an author's name and the base branch: "sam wants to merge into main from feature/x".
  "trackers.pr.wantsToMergeInto": "wants to merge into",
  "trackers.pr.from": "from",
  "trackers.pr.files": "{n} files",
  "trackers.pr.stack": "Stack",
  "trackers.pr.mergeSection": "Merge",
  "trackers.pr.noChecksReported": "No checks reported",
  "trackers.pr.failingCount": "{n} failing",
  "trackers.pr.noReviewDecision": "No review decision",
  // Followed by the base branch name.
  "trackers.pr.branchConflictsWith": "This branch conflicts with",
  "trackers.pr.ciSession": "session #{id} is on it",
  "trackers.pr.conflictingFiles": "Conflicting files",
  "trackers.pr.findingConflicts": "Working out which files conflict…",
  "trackers.pr.noConflictingFiles": "No conflicting files found; the base may have moved.",
  "trackers.pr.resolveWithAgent": "Resolve with agent",
  // {method} is the merge method (merge, squash, rebase); {name} is a login.
  "trackers.pr.autoOn": "Auto-merge is on.",
  "trackers.pr.autoOnMethod": "Auto-merge is on ({method}).",
  "trackers.pr.autoOnBy": "Auto-merge is on, set by {name}.",
  "trackers.pr.autoOnMethodBy": "Auto-merge is on ({method}), set by {name}.",
  "trackers.pr.turnOff": "Turn off",
  "trackers.pr.mergeMethod": "Merge method",
  "trackers.pr.deleteBranch": "Delete branch",
  "trackers.pr.merging": "Merging…",
  "trackers.pr.enabling": "Enabling…",
  "trackers.pr.autoWhenReady": "Auto-merge when ready",
  "trackers.pr.ciBusy": "The CI loop is already working on this",
  "trackers.pr.fixChecks": "Fix checks with agent",
  "trackers.pr.checks": "Checks",
  "trackers.pr.noChecks": "No checks.",
  "trackers.pr.hideLog": "Hide log",
  // Button that shows a check's log.
  "trackers.pr.log": "Log",
  "trackers.pr.details": "Details ↗",
  "trackers.pr.reviewers": "Reviewers",
  "trackers.pr.noReviewers": "Nobody yet.",
  // Marks a reviewer that is a team rather than a person.
  "trackers.pr.team": "team",
  "trackers.pr.reviewer.requested": "requested",
  "trackers.pr.reviewer.approved": "approved",
  "trackers.pr.reviewer.changesRequested": "changes requested",
  "trackers.pr.reviewer.commented": "commented",
  "trackers.pr.reviewer.dismissed": "dismissed",
  "trackers.pr.removeReviewer": "Remove reviewer {name}",
  "trackers.pr.requestReview": "Request review",
  "trackers.pr.conversation": "Conversation",
  // Confirmation dialog. {action} is the merge button's label, e.g. "Squash and merge".
  "trackers.pr.confirm.question": "{action}?",
  "trackers.pr.confirm.autoTitle": "Enable auto-merge?",
  "trackers.pr.confirm.closeTitle": "Close pull request?",
  "trackers.pr.confirm.reopenTitle": "Reopen pull request?",
  // The sentence reads: "[When every requirement passes,] #12 Title will be merged into main with squash and merge[, and its branch is deleted]."
  "trackers.pr.confirm.whenPasses": "When every requirement passes,",
  "trackers.pr.confirm.willBeMergedInto": "will be merged into",
  "trackers.pr.confirm.joinsQueueInto": "joins the merge queue into",
  "trackers.pr.confirm.isMergedInto": "is merged into",
  // Followed by the merge method in lower case.
  "trackers.pr.confirm.with": "with",
  "trackers.pr.confirm.branchDeleted": ", and its branch is deleted.",
  // Ends the sentence when the branch is kept.
  "trackers.pr.confirm.end": ".",
  // Followed by a short commit hash, then trackers.pr.confirm.shaRule.
  "trackers.pr.confirm.onlyCommit": "Only commit",
  "trackers.pr.confirm.shaRule": "is merged; if the branch moves first, the merge is refused.",
  "trackers.pr.confirm.willClose": "{mark} will be closed without merging.",
  "trackers.pr.confirm.willReopen": "{mark} will be reopened.",
  "trackers.pr.confirm.go": "Confirm merge",

  // IssuePage
  // {source} is GitHub, GitLab, Linear or Jira.
  "trackers.issue.kind": "{source} issue",
  // Followed by the author's name.
  "trackers.issue.openedBy": "opened by",
  // {names} is a comma-separated list of logins.
  "trackers.issue.assignedTo": "assigned to {names}",
  "trackers.issue.workOnThis": "Work on this",
  "trackers.issue.startSessionOrTask": "Start session or task",
  "trackers.issue.closeIssue": "Close issue",
  // Followed by a branch name.
  "trackers.issue.suggestedBranch": "Suggested branch",
  "trackers.issue.moveWithTransition": "Move with transition",
  "trackers.issue.status": "Status",
  // {state} is the issue's current Jira status.
  "trackers.issue.chooseTransition": "{state} — choose…",
  // {name} is a status or transition name.
  "trackers.issue.movedTo": "Moved to {name}",
  "trackers.issue.related": "Related issues",
  // Heading: the parent issue.
  "trackers.issue.parent": "Parent",
  "trackers.issue.subIssues": "Sub-issues",
  "trackers.issue.subIssuesDone": "{done}/{total} done",
  "trackers.issue.comments": "Comments",

  // TrackerSettings
  "trackers.settings.field.repository": "Repository",
  "trackers.settings.field.host": "Host",
  "trackers.settings.field.projectPath": "Project path",
  "trackers.settings.field.teamKey": "Team key",
  "trackers.settings.field.apiKey": "API key",
  "trackers.settings.field.siteUrl": "Site URL",
  "trackers.settings.field.deployment": "Deployment",
  "trackers.settings.field.email": "Account email",
  "trackers.settings.field.token": "Token",
  "trackers.settings.field.projectKey": "Project key",
  "trackers.settings.field.jql": "JQL (optional)",
  "trackers.settings.hint.originRemote": "Leave empty to use the clone's origin remote.",
  "trackers.settings.hint.githubEnterprise": "Only for GitHub Enterprise.",
  "trackers.settings.hint.gitlabSelfManaged": "For a self-managed GitLab.",
  "trackers.settings.hint.teamKey": "The team the hub opens on.",
  "trackers.settings.hint.linearKey": "Linear → Settings → API. Empty reuses this project's Linear trigger key.",
  "trackers.settings.hint.deployment": "Empty guesses from the URL: *.atlassian.net is Cloud.",
  "trackers.settings.hint.email": "Jira Cloud only: the account the API token belongs to.",
  "trackers.settings.hint.token": "Cloud: an API token. Server / Data Center: a personal access token.",
  "trackers.settings.hint.jql": "Replaces the project filter.",
  // {error} is an error message from the server.
  "trackers.settings.loadFailed": "Trackers: {error}",
  // {source} is GitHub, GitLab, Linear or Jira.
  "trackers.settings.saved": "{source} saved",
  "trackers.settings.connected": "{source} connected",
  "trackers.settings.testing": "Testing…",
  // {name} is a connection's name.
  "trackers.settings.confirmDisconnect": "Disconnect {name}?",
  "trackers.settings.name": "Name",
  "trackers.settings.flavor.server": "Server / Data Center",
  "trackers.settings.secretSet": "•••••• set — leave empty to keep",
  // Verb: save the edited connection.
  "trackers.settings.save": "Save",
  // Verb: create the connection.
  "trackers.settings.connect": "Connect",
  "trackers.settings.title": "Tasks hub",
  // Followed by the command names "gh / glab", then trackers.settings.intro2.
  "trackers.settings.intro1": "Where this project's pull requests and issues come from. GitHub and GitLab use the",
  "trackers.settings.setHere": "set here",
  "trackers.settings.fromOrigin": "from the origin remote",
  "trackers.settings.checking": "Checking the repository…",
  // {name} is a setting's key, e.g. "api_key".
  "trackers.settings.secretIsSet": "{name} set",
  "trackers.settings.secretNotSet": "{name} not set",
  "trackers.settings.borrowed": "key from trigger",
  // Verb: test the connection.
  "trackers.settings.test": "Test",
  "trackers.settings.edit": "Edit",
  "trackers.settings.disconnect": "Disconnect",
  "trackers.settings.addSource": "+ {source}",
  "trackers.settings.addRepository": "+ {source} repository",

  // Tasks hub tabs and reactions (merge from connect-ui)
  // Tab name Azure DevOps uses for its issues.
  "trackers.hub.tab.workItems": "Work items",
  // Tab: GitHub's merge queue.
  "trackers.hub.tab.mergeQueue": "Merge queue",
  // Tab: GitLab's merge train.
  "trackers.hub.tab.mergeTrain": "Merge train",
  "trackers.notice.reactionAdded": "Reaction added",
  // Button on an existing reaction. {emoji} is the emoji, {n} how many people used it.
  "trackers.reactions.reactCount": "React {emoji} ({n})",
  // {emoji} is a reaction name such as +1, heart or rocket.
  "trackers.reactions.react": "React {emoji}",
  // {label} is an item such as "#12" or a comment (trackers.reactions.commentBy).
  "trackers.reactions.add": "Add a reaction to {label}",
  // {name} is the comment author's login.
  "trackers.reactions.commentBy": "{name}'s comment",
  // Stands in for trackers.reactions.commentBy when the author is unknown.
  "trackers.reactions.thisComment": "this's comment",

  // IssuePage: editing the description
  // Verb: edit the description.
  "trackers.issue.edit": "Edit",
  "trackers.issue.lossyWarning": "This description has formatting the editor cannot keep (a table, panel, colour or attachment). Saving replaces it.",
  "trackers.issue.saving": "Saving…",
  "trackers.issue.saveDescription": "Save description",
  "trackers.issue.descriptionSaved": "Description saved",

  // RichEditor
  "trackers.editor.bold": "Bold (Ctrl+B)",
  "trackers.editor.italic": "Italic (Ctrl+I)",
  "trackers.editor.strike": "Strikethrough",
  "trackers.editor.code": "Inline code",
  // Noun: insert a link.
  "trackers.editor.link": "Link (Ctrl+K)",
  "trackers.editor.heading": "Heading",
  "trackers.editor.bullet": "Bulleted list",
  "trackers.editor.number": "Numbered list",
  // Noun: a block quote.
  "trackers.editor.quote": "Quote",
  "trackers.editor.codeblock": "Code block",
  // {label} is the field's name, e.g. "Comment".
  "trackers.editor.toolbar": "{label} formatting",
  // Tab: type the text (as opposed to Preview).
  "trackers.editor.write": "Write",
  "trackers.editor.preview": "Preview",
  "trackers.editor.nothingToPreview": "Nothing to preview.",

  // format.ts: sample text inserted when nothing is selected
  "trackers.format.hint.bold": "bold text",
  "trackers.format.hint.italic": "italic text",
  "trackers.format.hint.strike": "struck text",
  // Noun: sample code.
  "trackers.format.hint.code": "code",
  "trackers.format.hint.link": "link text",

  // MergeQueue
  // Fills {word} in the sentences below: GitHub's queue.
  "trackers.queue.word.queue": "merge queue",
  // Fills {word} in the sentences below: GitLab's train.
  "trackers.queue.word.train": "merge train",
  // {n} is a pull request number; {word} is trackers.queue.word.*.
  "trackers.queue.removed": "#{n} removed from the {word}",
  "trackers.queue.targetBranch": "Target branch",
  "trackers.queue.defaultBranch": "default branch",
  // Verb: show the queue for the branch.
  "trackers.queue.show": "Show",
  "trackers.queue.unsupported": "This code host has no {word}.",
  // Followed by a branch name, then trackers.queue.end.
  "trackers.queue.empty": "Nothing is waiting in the {word} for",
  // Ends the sentence begun by trackers.queue.empty.
  "trackers.queue.end": ".",
  // {base} is a branch name.
  "trackers.queue.listLabel": "{word} for {base}",
  "trackers.queue.position": "position {n}",
  // {status} is the pipeline state from GitLab, e.g. "running".
  "trackers.queue.pipeline": "pipeline {status}",
  // {eta} is trackers.queue.eta.*.
  "trackers.queue.mergesIn": "merges in {eta}",
  "trackers.queue.eta.minute": "about a minute",
  "trackers.queue.eta.minutes": "about {n} min",
  "trackers.queue.eta.hours": "about {n} h",
  // {ago} is a relative time such as "3h ago".
  "trackers.queue.queued": "queued {ago}",
  "trackers.queue.removeLabel": "Remove #{n} from the {word}",
  // Verb: take the pull request out of the queue.
  "trackers.queue.remove": "Remove",
  "trackers.queue.removing": "Removing…",
  "trackers.queue.confirmTitle": "Remove #{n} from the {word}?",
  // {title} is the pull request title; followed by a branch name, then trackers.queue.leavesEnd or leavesEndTrain.
  "trackers.queue.leaves": "{title} leaves the {word} for",
  "trackers.queue.leavesEnd": ". It stays open.",
  // GitLab only: merge-when-ready is GitLab's auto-merge.
  "trackers.queue.leavesEndTrain": " and its merge-when-ready is turned off. It stays open.",

  // TrackerSettings: Bitbucket, Gitea and Azure DevOps
  "trackers.settings.field.apiUrl": "API URL (optional)",
  // Bitbucket Cloud account the token belongs to.
  "trackers.settings.field.cloudAccount": "Account (Cloud)",
  "trackers.settings.field.accessToken": "Access token",
  "trackers.settings.field.serverUrl": "Server URL (optional)",
  "trackers.settings.field.pat": "Personal access token",
  // Placeholder: sample repository paths; keep workspace/repo and PROJECT/repo as they are.
  "trackers.settings.placeholder.bitbucketRepo": "workspace/repo or PROJECT/repo",
  "trackers.settings.hint.bitbucketHost": "For Bitbucket Data Center, its host name.",
  "trackers.settings.hint.bitbucketDeployment": "Empty guesses from the host: bitbucket.org is Cloud.",
  "trackers.settings.hint.bitbucketApiUrl": "Data Center behind a path prefix.",
  "trackers.settings.hint.bitbucketAccount": "Cloud API token or app password: the account it belongs to. Leave empty for a repository/workspace access token.",
  "trackers.settings.hint.bitbucketToken": "Cloud: API token, app password or access token. Data Center: an HTTP access token.",
  // Settings → Applications is the path in Gitea's own menus.
  "trackers.settings.hint.giteaToken": "Settings → Applications → a token with repository and issue scopes.",
  "trackers.settings.hint.azureServerUrl": "Only for Azure DevOps Server.",
  // Code and Work Items are Azure DevOps scope names.
  "trackers.settings.hint.azureToken": "Scopes: Code (read & write), Work Items (read & write).",
  // Deployment option: work it out from the host name.
  "trackers.settings.flavor.guessHost": "Guess from the host",
  // Deployment option: the vendor's cloud service.
  "trackers.settings.flavor.cloudAny": "Cloud",
  // Follows the command names "gh / glab" after trackers.settings.intro1.
  "trackers.settings.intro2Tokens": "login on this project's machine; Bitbucket, Gitea/Forgejo, Azure DevOps, Linear and Jira need a token, which is kept on the server and never shown again.",
};

export default catalog;
