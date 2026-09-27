// Wire shapes for the Tasks hub (internal/trackers, internal/api/trackers*.go).

export type Source = "github" | "gitlab" | "bitbucket" | "gitea" | "azure" | "linear" | "jira";

export interface Label {
  name: string;
  color?: string;
}

export interface Item {
  source: Source;
  kind: "pr" | "issue";
  id: string;
  connection_id?: number;
  title: string;
  url: string;
  state: string;
  status_type?: string;
  author?: string;
  assignees: string[];
  labels: Label[];
  updated_at: string;
  draft?: boolean;
  checks?: "pass" | "fail" | "pending" | "none";
  review?: "approved" | "changes_requested" | "review_required";
  conflicts?: boolean;
  head?: string;
  base?: string;
  priority?: string;
  parent?: string;
}

export interface WorkSource {
  source: Source;
  kind?: string;
  connection_id?: number;
  name: string;
  ok: boolean;
  error?: string;
  count: number;
}

export interface WorkResponse {
  items: Item[];
  sources: WorkSource[];
}

export interface Reaction {
  emoji: string;
  count: number;
}

export interface TimelineEvent {
  /** what a reaction to this comment names; absent when it cannot take one */
  id?: string;
  kind: "comment" | "review" | "commit" | "event";
  author?: string;
  body?: string;
  state?: string;
  at: string;
  url?: string;
  reactions?: Reaction[];
}

export interface Check {
  id: string;
  name: string;
  workflow?: string;
  status: "pass" | "fail" | "pending" | "skipping" | "cancel";
  url?: string;
  has_log: boolean;
}

export interface Reviewer {
  login: string;
  state: "requested" | "approved" | "changes_requested" | "commented" | "dismissed";
  team?: boolean;
}

export interface StackEntry {
  number: number;
  title: string;
  head: string;
  base: string;
  url: string;
  state: string;
  current?: boolean;
  depth: number;
}

export type MergeMethod = "merge" | "squash" | "rebase";

export interface MergeOptions {
  methods: MergeMethod[];
  default: MergeMethod;
  delete_branch_default: boolean;
  auto_merge_allowed: boolean;
  merge_queue: boolean;
  can_merge: boolean;
}

export interface CIWatch {
  state: string;
  label: string;
  attempts: number;
  max_attempts: number;
  active: boolean;
  session_id?: number;
  task_id?: number;
}

export interface PRDetail extends Item {
  body: string;
  head_sha: string;
  cross_repo: boolean;
  mergeable: "mergeable" | "conflicting" | "unknown" | string;
  merge_state: string;
  auto_merge: { method?: string; enabled_by?: string } | null;
  reviewers: Reviewer[];
  timeline: TimelineEvent[];
  check_runs: Check[];
  additions: number;
  deletions: number;
  changed_files: number;
  stack: StackEntry[] | null;
  merge: MergeOptions;
  reactions?: Reaction[];
  created_at?: string;
  merged_at?: string;
  closed_at?: string;
  project_id: number;
  ci?: CIWatch | null;
}

export interface Transition {
  id: string;
  name: string;
  type?: string;
}

export interface IssueDetail extends Item {
  body: string;
  timeline: TimelineEvent[];
  reactions?: Reaction[];
  created_at?: string;
  children: Item[];
  parent_item?: Item;
  transitions: Transition[];
  team?: string;
  uid?: string;
  branch_name: string;
  editable?: boolean;
  body_lossy?: boolean;
}

export interface Conflicts {
  files: string[];
  checked: boolean;
  detail?: string;
}

export interface TrackerConnection {
  id: number;
  project_id: number;
  kind: Source;
  name: string;
  config: Record<string, unknown>;
  secrets: Record<string, boolean>;
  borrowed?: boolean;
}

export interface ForgeInfo {
  kind?: "github" | "gitlab" | "bitbucket" | "gitea" | "azure";
  host?: string;
  repo?: string;
  url?: string;
  source?: "remote" | "connection";
  error?: string;
}

export interface TrackersResponse {
  forge: ForgeInfo;
  connections: TrackerConnection[];
}

export interface ForgeMeta {
  labels: Label[];
  users: { login: string; name?: string }[];
  labels_error?: string;
  users_error?: string;
}

/** What a list row or deep link points at. */
export interface ItemRef {
  source: Source;
  kind: "pr" | "issue";
  id: string;
  connection_id?: number;
}

export interface QueueEntry {
  id: string;
  number: number;
  title: string;
  url: string;
  author?: string;
  position: number;
  status: string;
  enqueued_at?: string;
  eta_seconds?: number;
  pipeline?: string;
}

export interface QueueResponse {
  supported: boolean;
  kind: string;
  base?: string;
  entries: QueueEntry[];
}
