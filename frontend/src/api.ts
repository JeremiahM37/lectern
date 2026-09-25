export { ApiError, authToken, createClient, withToken } from './api/client';
export type { ClientOptions, JsonValue, RequestOptions } from './api/client';

import { createClient, type ClientOptions, type JsonValue } from './api/client';
import type { Approval, Project, SessionView, Target, TaskView } from './types';

// Keep these return contracts tied to the Go response structs. Unknown nested
// provider payloads must be narrowed at their display boundary.
export function createDeckApi(options: ClientOptions = {}) {
  const request = createClient(options);
  return {
    request,
    tasks: (signal?: AbortSignal) => request<TaskView[]>('/tasks', { signal }),
    task: (id: number, signal?: AbortSignal) => request<TaskView>(`/tasks/${id}`, { signal }),
    projects: (signal?: AbortSignal) => request<Project[]>('/projects', { signal }),
    targets: (signal?: AbortSignal) => request<Target[]>('/targets', { signal }),
    approvals: (signal?: AbortSignal) => request<Approval[]>('/approvals', { signal }),
    sessions: (options: { archived?: boolean; all?: boolean; signal?: AbortSignal } = {}) => {
      const query = new URLSearchParams();
      if (options.archived) query.set('archived', 'true');
      if (options.all) query.set('all', 'true');
      if (!options.archived && !options.all) query.set('include_setup_failures', 'true');
      return request<SessionView[]>(`/sessions${query.size ? `?${query}` : ''}`, { signal: options.signal });
    },
    createTask: (body: {project_id:number;title:string;prompt:string;priority?:number;agent?:string;model?:string;permission_mode?:string;base_branch?:string;orchestrate?:boolean;budget_usd?:number}) => request<TaskView>('/tasks', {method:'POST', body}),
    patchTask: (id:number, body: Record<string, JsonValue>) => request<TaskView>(`/tasks/${id}`, {method:'PATCH', body}),
    deleteTask: (id:number) => request<null>(`/tasks/${id}`, {method:'DELETE'}),
    taskAction: (id:number, action:'dispatch'|'followup'|'complete'|'cancel'|'commit'|'cleanup'|'takeover'|'review', body:Record<string,JsonValue>={}) => request<Record<string,JsonValue>>(`/tasks/${id}/${action}`, {method:'POST', body}),
    taskEvents: (id:number, signal?:AbortSignal) => request<import('./types').Event[]>(`/tasks/${id}/events`, {signal}),
    taskDiff: (id:number, signal?:AbortSignal) => request<Record<string,JsonValue>>(`/tasks/${id}/diff`, {signal}),
    taskPRDescription: (id:number) => request<{title:string;body:string}>(`/tasks/${id}/pr-description`, {method:'POST'}),
    // Review and merge for sessions: a live diff (possibly several
    // repositories), commit/push/PR reusing the task implementation, and a
    // headless-generated PR description — see docs/agent-events.md section 4.
    sessionDiff: (id:number, signal?:AbortSignal) => request<Record<string,JsonValue>>(`/sessions/${id}/diff`, {signal}),
    sessionReview: (id:number, body:Record<string,JsonValue>) => request<Record<string,JsonValue>>(`/sessions/${id}/review`, {method:'POST', body}),
    sessionCommit: (id:number, body:Record<string,JsonValue>, repo?:string) => request<Record<string,JsonValue>>(`/sessions/${id}/commit${repo?`?repo=${encodeURIComponent(repo)}`:''}`, {method:'POST', body}),
    sessionPRDescription: (id:number) => request<{title:string;body:string}>(`/sessions/${id}/pr-description`, {method:'POST'}),
    decideApproval: (id:number, decision:'approved'|'denied', note='', always_allow=false) => request<null>(`/approvals/${id}/decision`, {method:'POST',body:{decision,note,always_allow}}),
    patchSession: (id:number, body:Record<string,JsonValue>) => request<SessionView>(`/sessions/${id}`, {method:'PATCH',body}),
    sessionAction: (id:number, action:'terminal'|'restore'|'handoff'|'promote'|'archive'|'send'|'resume'|'fork', body:Record<string,JsonValue>={}) => request<Record<string,JsonValue>>(`/sessions/${id}/${action}`, {method:'POST',body}),
    deleteSession: (id:number, kill=false) => request<null>(`/sessions/${id}${kill?'?kill=true':''}`, {method:'DELETE'}),
    archiveHistory: (id:number) => request<{text:string;note:string}>(`/sessions/${id}/archive/history`),
    checkTarget: (id:number) => request<Target>(`/targets/${id}/check`, {method:'POST'}),
    createProject: (body:Record<string,JsonValue>) => request<Project>('/projects',{method:'POST',body}),
    patchProject: (id:number, body:Record<string,JsonValue>) => request<Project>(`/projects/${id}`,{method:'PATCH',body}),
    settings: () => request<Record<string,JsonValue>>('/settings'),
    saveSettings: (body:Record<string,JsonValue>) => request<Record<string,JsonValue>>('/settings',{method:'PUT',body}),
  };
}
