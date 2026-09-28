// English strings for this area of the app. Keys are "area.name"; values may
// hold {placeholders}. Every other language has a file of the same name.
const catalog: Record<string, string> = {
  // Shared review actions and comment states
  "review.action.cancel": "Cancel",
  // Verb: mark a review comment as done.
  "review.action.resolve": "Resolve",
  // Verb: send a review comment again in the next round.
  "review.action.reopen": "Reopen",
  "review.state.draft": "Draft",
  "review.state.addressed": "Agent changed this",
  // Adjective: the comment has been marked done.
  "review.state.resolved": "Resolved",

  // CIChip
  // {checks} is a comma-separated list of failing CI check names.
  "review.ci.failing": "failing: {checks}",

  // CommentTray
  "review.tray.empty": "No comments yet — tap a line to comment.",
  "review.tray.writeSummary": "Write an overall note",
  "review.tray.noComments": "No comments yet",
  "review.tray.comments.one": "{count} comment",
  "review.tray.comments.other": "{count} comments",
  // {file}:{line} is a file path and line number.
  "review.tray.remove": "Remove comment on {file}:{line}",
  "review.tray.summaryLabel": "Overall summary (optional)",
  "review.tray.summaryPlaceholder": "Anything to say beyond the inline comments…",
  "review.tray.sending": "Sending…",
  // Shown after a comment's text when it is sent again in a later round.
  "review.tray.raisedAgain": "{text} (raised again)",

  // DiffModeToggle
  "review.mode.label": "Diff layout",
  "review.mode.unified": "Unified",
  "review.mode.split": "Side by side",

  // FileTree
  // Adjective: the file has been marked as reviewed.
  "review.tree.viewed": "viewed",
  "review.tree.label": "Changed files",
  "review.tree.filter": "Filter files",
  "review.tree.filterLabel": "Filter changed files",
  "review.tree.viewedCount": "{viewed}/{total} viewed",
  "review.tree.noMatch": "No matching files.",

  // ImageDiff
  // Mode that shows the old and new image side by side.
  "review.image.side": "2-up",
  "review.image.swipe": "Swipe",
  "review.image.onion": "Onion skin",
  "review.image.loading": "Loading images…",
  "review.image.comparison": "Image comparison",
  "review.image.before": "Before",
  "review.image.after": "After",
  // {path} is the image's file path.
  "review.image.altBefore": "{path} before",
  "review.image.altAfter": "{path} after",
  "review.image.newImage": "New image",
  // Adjective: the image was deleted.
  "review.image.deleted": "Deleted",
  "review.image.swipePosition": "Swipe position",
  "review.image.opacity": "Opacity of the new image",

  // ReviewRounds
  "review.rounds.open": "Not changed yet",
  "review.rounds.summary": "Sent comments · {addressed} changed by the agent · {open} not changed · {resolved} resolved",
  "review.rounds.resolveChanged": "Resolve {n} the agent changed",
  "review.rounds.reopenUnchanged": "Reopen {n} unchanged",
  "review.rounds.round": "Round {round}",

  // DiffViewer
  "review.diff.stateOpen": "Sent · not changed yet",
  // Appended after a comment's state; note the leading space.
  "review.diff.round": " · round {round}",
  "review.diff.commentPlaceholder": "Leave a comment on this line…",
  "review.diff.addComment": "Add comment",
  "review.diff.writtenByAgent": "Written by the agent",
  "review.diff.writtenByYou": "Written by you",
  // Screen-reader label for the authorship gutter: the line was written by the agent.
  "review.diff.authorAgent": "agent",
  // Screen-reader label for the authorship gutter: the line was written by you.
  "review.diff.authorYou": "you",
  "review.diff.commentsOnLine": "{n} comment(s) on this line",
  "review.diff.truncated": "This diff was too large to show in full and has been truncated.",
  "review.diff.noChanges": "No changes.",
  "review.diff.addedByAuthor": "Added lines by author",
  // Checkbox label: mark this file as reviewed.
  "review.diff.viewed": "Viewed",
  "review.diff.showAsText": "Show as text",
  "review.diff.noTextual": "No textual changes.",
  // Screen-reader label for a checkbox that picks one line for staging; {line} is a line number.
  "review.diff.chooseLine": "Choose line {line}",

  // ConflictResolver
  "review.conflicts.acceptOurs": "Accept ours",
  "review.conflicts.acceptTheirs": "Accept theirs",
  "review.conflicts.acceptBoth": "Accept both",
  // How a conflict region is currently resolved.
  "review.conflicts.choice.unresolved": "unresolved",
  "review.conflicts.choice.edited": "edited",
  "review.conflicts.choice.ours": "ours",
  "review.conflicts.choice.theirs": "theirs",
  "review.conflicts.choice.both": "both",
  "review.conflicts.choice.base": "base",
  "review.conflicts.resolved": "Resolved {path}.",
  "review.conflicts.loading": "Loading conflicts…",
  // {operation} is a git operation name as git reports it (merge, rebase, cherry-pick).
  "review.conflicts.noneLeft": "No conflicts left. Commit to finish the {operation}.",
  "review.conflicts.none": "No merge conflicts in this repository.",
  "review.conflicts.label": "Merge conflicts",
  "review.conflicts.filesLabel": "Conflicted files",
  "review.conflicts.files.one": "{count} conflicted file.",
  "review.conflicts.files.other": "{count} conflicted files.",
  // {operation} is a git operation name (merge, rebase); {branch} a branch name.
  "review.conflicts.stoppedOn.one": "A {operation} into {branch} stopped on {count} conflicted file.",
  "review.conflicts.stoppedOn.other": "A {operation} into {branch} stopped on {count} conflicted files.",
  "review.conflicts.loadingPath": "Loading {path}…",
  "review.conflicts.takeOurs": "Take ours for the whole file",
  "review.conflicts.takeTheirs": "Take theirs for the whole file",
  "review.conflicts.binary": "This file is binary: take one side as a whole.",
  "review.conflicts.oneSideDeleted": "One side deleted this file. Take a side, or edit the result.",
  "review.conflicts.oneSideNever": "One side never had this file. Take a side, or edit the result.",
  "review.conflicts.conflictN": "Conflict {n}",
  // {label} is the branch or commit git names for this side.
  "review.conflicts.ours": "Ours · {label}",
  "review.conflicts.base": "Base",
  "review.conflicts.theirs": "Theirs · {label}",
  // Fallback name for the incoming side when git gives none.
  "review.conflicts.incoming": "incoming",
  // {edited} is empty, or the "(edited by hand)" note.
  "review.conflicts.result": "Result {edited}",
  "review.conflicts.editedByHand": "(edited by hand)",
  "review.conflicts.stillUnresolved": "{n} conflict(s) still unresolved.",
  "review.conflicts.noMarkers": "No conflict markers left.",
  "review.conflicts.startFromFile": "Start from the file as it is now",
  "review.conflicts.saveWithMarkers": "Save with markers…",
  "review.conflicts.saveAnyway": "Save anyway",
  "review.conflicts.stillHasMarkers": "The result still has conflict markers.",
  "review.conflicts.saving": "Saving…",
  "review.conflicts.markResolved": "Mark resolved",

  // FocusReview
  "review.focus.label": "Review one file at a time",
  "review.focus.leave": "Leave file-by-file review",
  "review.focus.count": "File {index} of {total} · {viewed} viewed",
  "review.focus.viewed": "✓ Viewed",
  "review.focus.markViewed": "Mark viewed",
  "review.focus.prevFile": "‹ File",
  "review.focus.prevHunk": "▲ Prev hunk",
  "review.focus.nextHunk": "Next hunk ▼",
  "review.focus.nextFile": "File ›",
  "review.focus.send": "Send {n} 💬",

  // GitPanel
  "review.git.unstageHunk": "Unstage hunk",
  "review.git.stageHunk": "Stage hunk",
  "review.git.discardHunk": "Discard hunk",
  // Verb: throw away the changes.
  "review.git.discard": "Discard",
  "review.git.discardedHunk": "Discarded that hunk.",
  // Hunk buttons when some lines of the hunk are ticked; {count} is how many.
  "review.git.unstageLines.one": "Unstage {count} line",
  "review.git.unstageLines.other": "Unstage {count} lines",
  "review.git.stageLines.one": "Stage {count} line",
  "review.git.stageLines.other": "Stage {count} lines",
  "review.git.discardLines.one": "Discard {count} line",
  "review.git.discardLines.other": "Discard {count} lines",
  "review.git.discardedLines.one": "Discarded that {count} line.",
  "review.git.discardedLines.other": "Discarded that {count} lines.",
  // Verb.
  "review.git.unstage": "Unstage",
  // Verb.
  "review.git.stage": "Stage",
  "review.git.deleteFile": "Delete file",
  "review.git.discardChanges": "Discard changes",
  "review.git.discardedPath": "Discarded {path}.",
  // {agent} is an agent name (Claude, Codex…); {model} a model name.
  "review.git.messageBy": "Commit message written by {agent}.",
  "review.git.messageByModel": "Commit message written by {agent} ({model}).",
  // {step} is a step name from the server (commit, push, pr).
  "review.git.stepFailed": "Commit step \"{step}\" failed.",
  "review.git.amended": "Amended.",
  "review.git.committed": "Committed.",
  "review.git.forceRefused": "Force push refused: origin moved since you looked. Refresh and check.",
  "review.git.forcePushed": "Force pushed.",
  "review.git.hookSent": "Sent the hook output to the agent.",
  "review.git.loading": "Loading git status…",
  // Shown instead of a branch name when HEAD is not on a branch.
  "review.git.detached": "detached",
  "review.git.onOrigin": "on origin",
  "review.git.notPushed": "not pushed yet",
  // {hooks} is a comma-separated list of git hook names.
  "review.git.hooks": "hooks: {hooks}",
  "review.git.refresh": "Refresh",
  // {operation} is a git operation name (merge, rebase, cherry-pick).
  "review.git.inProgress": "A {operation} is in progress.",
  "review.git.stillInConflict": "{n} file(s) still in conflict.",
  "review.git.allResolved": "All conflicts are resolved — commit to finish it.",
  "review.git.resolveConflicts": "Resolve conflicts",
  "review.git.abort": "Abort {operation}",
  "review.git.abortThe": "Abort the {operation}",
  "review.git.aborted": "Aborted the {operation}.",
  "review.git.staged": "Staged ({n})",
  "review.git.unstageAll": "Unstage all",
  "review.git.nothingStaged": "Nothing staged. Commit takes every change.",
  // Heading: the list of unstaged changes.
  "review.git.changes": "Changes ({n})",
  "review.git.stageAll": "Stage all",
  "review.git.noUnstaged": "No unstaged changes.",
  "review.git.truncated": "Some patches were too large to show.",
  // Heading (noun): the commit form.
  "review.git.commitHeading": "Commit",
  "review.git.commitMessage": "Commit message",
  "review.git.writing": "Writing…",
  "review.git.writeMessage": "✨ Write message",
  "review.git.amendLast": "Amend the last commit",
  "review.git.amendLastPushed": "Amend the last commit (already pushed)",
  "review.git.pushToOrigin": "Push to origin",
  // {gh} is replaced by the command name "gh", shown as code.
  "review.git.openPr": "Open a PR (needs {gh} on the machine)",
  "review.git.prTitle": "PR title",
  "review.git.prBody": "PR body",
  "review.git.generating": "Generating…",
  "review.git.generateDescription": "✨ Generate description",
  "review.git.commitsStaged.one": "Commits the {count} staged file.",
  "review.git.commitsStaged.other": "Commits the {count} staged files.",
  "review.git.commitsEverything": "Nothing is staged, so this commits every change.",
  "review.git.committing": "Committing…",
  "review.git.amendCommit": "⎇ Amend commit",
  // Button (verb).
  "review.git.commit": "⎇ Commit",
  // {refs} is a list of remote branch names, shown in bold.
  "review.git.alreadyPushed": "The last commit is already on {refs}. Amending it rewrites published history, and origin will then need a force push.",
  "review.git.keepIt": "Keep it",
  "review.git.amendAnyway": "Amend anyway",
  "review.git.forcePushLease": "Force push (with lease)…",
  "review.git.forcePush": "Force push",
  // {branch} is a branch name; {remote} and {head} are short commit ids.
  "review.git.forcePrompt": "Replace origin/{branch} with your {head}? The push is refused if origin has moved since this status was loaded.",
  "review.git.forcePromptRemote": "Replace origin/{branch} ({remote}) with your {head}? The push is refused if origin has moved since this status was loaded.",
  // Result of a commit/push step.
  "review.git.stepOk": "ok",
  "review.git.stepFailedShort": "failed",
  // {hooks} is a git hook name such as pre-commit, or the word below.
  "review.git.hookRejected": "The {hooks} hook rejected this commit.",
  // Used as {hooks} in the sentence above when the hook's name is unknown.
  "review.git.hookCommit": "commit",
  "review.git.fixWithAgent": "🛠 Fix with agent",

  // SessionReview
  "review.session.sent": "Sent {n} comment(s) to the session as one message (round {round}).",
  "review.session.delete": "Delete",
  "review.session.tabChanges": "Changes ({n})",
  // Tab name (noun): the commit form.
  "review.session.tabCommit": "Commit",
  "review.session.tabConflicts": "Conflicts ({n})",
  "review.session.tabChecks": "Checks",
  "review.session.label": "Review and merge",
  "review.session.title": "Review & merge",
  // {name} is the session's name.
  "review.session.titleNamed": "Review & merge — {name}",
  "review.session.close": "Close",
  "review.session.tabsLabel": "Review",
  "review.session.repository": "Repository",
  "review.session.allRepositories": "All repositories",
  "review.session.chooseOne": "choose one…",
  "review.session.loading": "Loading the live diff…",
  "review.session.filesChanged": "{n} file(s) changed",
  "review.session.wrapOn": "⏎ wrap: on",
  "review.session.wrapOff": "⏎ wrap: off",
  "review.session.files": "🗂 files",
  "review.session.authorsHint": "Mark lines written by the agent (◆) and by you (●)",
  "review.session.authors": "◆ authors",
  "review.session.prevHunk": "Previous hunk",
  "review.session.nextHunk": "Next hunk",
  "review.session.fileByFile": "Review file by file",
  "review.session.legendAgent": "◆ agent",
  "review.session.legendYou": "● you",
  // Appended to the legend; note the leading " · ".
  "review.session.legendNoEvidence": " · uncommitted lines are only attributed once the agent reports an edit",
  "review.session.roundSummary": "Sent comments: {addressed} changed by the agent, {open} not changed yet.",
  "review.session.sendMany": "Send {n} comments to agent",
  "review.session.sendOne": "Send to agent",
  "review.session.chooseCommitRepo": "Choose the repository to commit in.",
  "review.session.chooseConflictRepo": "Choose the repository with the conflict.",

  // ChecksSection
  "review.checks.title": "Checks",
  "review.checks.starting": "Starting…",
  "review.checks.run": "Run check",
  "review.checks.none": "No checks have run yet.",
  "review.checks.running": "running…",
};

export default catalog;
