package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/JeremiahM37/lectern/internal/store"
	"github.com/JeremiahM37/lectern/internal/worktree"
)

func (m *Manager) claimWorkspaceOperation(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.workspaceOperations == nil {
		m.workspaceOperations = map[int64]bool{}
	}
	if m.workspaceOperations[id] {
		return false
	}
	m.workspaceOperations[id] = true
	return true
}
func (m *Manager) releaseWorkspaceOperation(id int64) {
	m.mu.Lock()
	delete(m.workspaceOperations, id)
	m.mu.Unlock()
}
func (m *Manager) publishWorkspaceOperation(sessionID int64) {
	if row, err := m.DB.Session(sessionID); err == nil {
		m.publish(row)
	}
}

func (m *Manager) ExtendWorkspace(ctx context.Context, sessionID, projectID int64, base string) (*store.WorkspaceOperation, error) {
	if m.setupActive(sessionID) {
		return nil, fmt.Errorf("initial workspace setup is still running")
	}
	row, ex, err := m.resolve(sessionID)
	if err != nil {
		return nil, err
	}
	var plan worktree.Interactive
	if json.Unmarshal([]byte(row.WorktreeJSON), &plan) != nil || len(plan.Repositories) == 0 {
		return nil, fmt.Errorf("this session does not own a grouped workspace")
	}
	project, err := m.DB.Project(projectID)
	if err != nil {
		return nil, err
	}
	if project.TargetID != row.TargetID {
		return nil, fmt.Errorf("the added project must use the workspace target")
	}
	canonical, err := canonicalWorkspaceAllocation(ctx, ex, plan.Path)
	if err != nil {
		return nil, err
	}
	use, err := m.reserveWorkspacePaths(row.TargetID, true, plan.Path, canonical)
	if err != nil {
		return nil, err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			m.releaseWorkspacePaths(use)
		}
	}()
	pending, err := m.DB.WorkspaceOperations(sessionID, true)
	if err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		return nil, fmt.Errorf("a repository addition is already active; inspect or cancel it first")
	}
	// Read the target's latest receipt before planning, including any extension
	// published by a previous server whose response was lost.
	if err := worktree.RunInteractive(ctx, ex, "status", &plan); err != nil {
		return nil, err
	}
	if plan.OperationActive {
		return nil, fmt.Errorf("a target workspace operation is still running")
	}
	pid := project.ID
	next, err := worktree.PlanWorkspaceExtension(&plan, worktree.RepositorySource{Name: project.Name, Repo: project.RepoPath, Base: base, ProjectID: &pid, SetupCommand: project.SetupCmd, SetupEnv: m.ProjectEnv(&pid)}, sessionID)
	if err != nil {
		return nil, err
	}
	// Hold the in-memory claim across persistence so a recovery pass cannot
	// mistake the interval before the goroutine starts for a server restart.
	m.mu.Lock()
	operation, err := m.DB.BeginWorkspaceOperation(sessionID, row.WorktreeJSON, store.J(next))
	if err == nil {
		if m.workspaceOperations == nil {
			m.workspaceOperations = map[int64]bool{}
		}
		m.workspaceOperations[operation.ID] = true
	}
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	handedOff = true
	worker, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Minute)
	go func() {
		defer cancel()
		defer m.releaseWorkspacePaths(use)
		defer m.releaseWorkspaceOperation(operation.ID)
		timeout := 120.0
		if next.HasSetupCommand() {
			timeout = 900
		}
		err := worktree.RunInteractiveWithTimeout(worker, ex, "extend", next, timeout)
		if err == nil {
			if err = m.DB.FinishWorkspaceOperation(operation.ID, "complete", "", store.J(next)); err != nil {
				m.Log.Error("save workspace extension", "operation", operation.ID, "error", err)
			}
			m.publishWorkspaceOperation(sessionID)
			return
		}
		m.DB.NoteWorkspaceOperation(operation.ID, err.Error())
		recovery, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if fresh, e := m.DB.WorkspaceOperation(operation.ID); e == nil {
			m.reconcileWorkspaceOperation(recovery, fresh)
		}
	}()
	m.publishWorkspaceOperation(sessionID)
	return operation, nil
}

func (m *Manager) WorkspaceOperation(sessionID, operationID int64) (*store.WorkspaceOperation, error) {
	op, err := m.DB.WorkspaceOperation(operationID)
	if err != nil {
		return nil, err
	}
	if op.SessionID != sessionID {
		return nil, store.ErrNotFound
	}
	return op, nil
}

func (m *Manager) CancelWorkspaceOperation(ctx context.Context, sessionID, operationID int64) (*store.WorkspaceOperation, error) {
	op, err := m.WorkspaceOperation(sessionID, operationID)
	if err != nil {
		return nil, err
	}
	if !op.Active() {
		return op, nil
	}
	if err = m.DB.CancelWorkspaceOperation(op.ID); err != nil {
		return nil, err
	}
	var plan worktree.Interactive
	if err = json.Unmarshal([]byte(op.PlanJSON), &plan); err != nil {
		return nil, err
	}
	delivery, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer stop()
	_, ex, err := m.resolve(sessionID)
	if err == nil {
		err = worktree.RunInteractive(delivery, ex, "cancel", &plan)
	}
	if err != nil {
		m.DB.NoteWorkspaceOperation(op.ID, "Cancellation recorded; target delivery unavailable. Retry when reachable.")
		return nil, fmt.Errorf("cancellation recorded, but target delivery failed: %w", err)
	}
	return m.DB.WorkspaceOperation(op.ID)
}

// Recovery fences the captured operation before inspecting its receipt. This
// also stops a delayed worker that has not yet published the expanded root.
func (m *Manager) reconcileWorkspaceOperation(ctx context.Context, op *store.WorkspaceOperation) {
	if !op.Active() {
		return
	}
	note := func(message string) { m.DB.NoteWorkspaceOperation(op.ID, message) }
	var requested worktree.Interactive
	if json.Unmarshal([]byte(op.PlanJSON), &requested) != nil {
		note("Saved extension plan is unreadable; inspect the operation")
		return
	}
	_, ex, err := m.resolve(op.SessionID)
	if err != nil {
		note("Target unavailable while checking extension")
		return
	}
	if err = worktree.RunInteractive(ctx, ex, "cancel", &requested); err != nil {
		note("Could not fence the interrupted extension; retry when the target is reachable")
		return
	}
	expectedToken, expectedCount := requested.ControlToken, len(requested.Repositories)
	actual := requested
	if err = worktree.RunInteractive(ctx, ex, "status", &actual); err != nil {
		note("Could not inspect the retained workspace; retry when the target is reachable")
		return
	}
	if actual.OperationActive {
		note("Waiting for the interrupted checkout to stop; the original terminal remains available")
		return
	}
	state, detail := "failed", "Repository addition was interrupted; allocated files were retained for inspection"
	if op.Error != "" {
		detail = op.Error
	}
	if actual.ControlToken == expectedToken && len(actual.Repositories) == expectedCount && actual.State == "ready" {
		state, detail = "complete", ""
	} else {
		if actual.ControlToken == expectedToken {
			actual.State = "failed"
			if actual.Error != "" {
				detail = actual.Error
			}
		}
		if op.CancelRequested {
			state = "cancelled"
		}
	}
	if err = m.DB.FinishWorkspaceOperation(op.ID, state, detail, store.J(&actual)); err != nil {
		note("Could not save recovered extension state")
		return
	}
	m.publishWorkspaceOperation(op.SessionID)
}

func (m *Manager) RecoverWorkspaceOperation(ctx context.Context, sessionID, operationID int64) (*store.WorkspaceOperation, error) {
	op, err := m.WorkspaceOperation(sessionID, operationID)
	if err != nil {
		return nil, err
	}
	if op.Active() && m.claimWorkspaceOperation(op.ID) {
		defer m.releaseWorkspaceOperation(op.ID)
		m.reconcileWorkspaceOperation(ctx, op)
	}
	return m.DB.WorkspaceOperation(op.ID)
}

func (m *Manager) RecoverWorkspaceOperations(ctx context.Context) {
	operations, err := m.DB.WorkspaceOperations(0, true)
	if err != nil {
		return
	}
	for _, op := range operations {
		if op.CancelRequested {
			m.retryWorkspaceCancellation(ctx, op)
		}
		if !m.claimWorkspaceOperation(op.ID) {
			continue
		}
		go func(op *store.WorkspaceOperation) {
			defer m.releaseWorkspaceOperation(op.ID)
			recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			m.reconcileWorkspaceOperation(recovery, op)
		}(op)
	}
}

// A recorded cancellation remains actionable if its first target delivery failed.
// Keep at most one delivery in flight per operation, separately from the worker.
func (m *Manager) retryWorkspaceCancellation(ctx context.Context, op *store.WorkspaceOperation) {
	m.mu.Lock()
	if m.workspaceCancelDeliveries == nil {
		m.workspaceCancelDeliveries = map[int64]bool{}
	}
	if m.workspaceCancelDeliveries[op.ID] {
		m.mu.Unlock()
		return
	}
	m.workspaceCancelDeliveries[op.ID] = true
	m.mu.Unlock()
	go func() {
		defer func() { m.mu.Lock(); delete(m.workspaceCancelDeliveries, op.ID); m.mu.Unlock() }()
		m.CancelWorkspaceOperation(ctx, op.SessionID, op.ID)
	}()
}
