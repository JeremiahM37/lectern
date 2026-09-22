package store

import (
	"context"
	"crypto/rand"
	"fmt"
)

// PromoteNewProjectAndBind atomically creates a project and binds one exact
// live session. A stale preview cannot leave an orphan project behind.
func (db *DB) PromoteNewProjectAndBind(ctx context.Context, p *Project, sessionID int64, tmux, originalBoot, boot, originalTracking, newTracking, cid, launchConfig string) (*Project, error) {
	if p.MemoryTopic == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		p.MemoryTopic, p.MemoryStatus = fmt.Sprintf("lectern-%x", b), "pending"
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO projects(name,target_id,repo_path,default_base_branch,workroot_override,policy_json,verify_cmd,keep_worktrees,review_gate,env_json,context_json,mcp_json,strict_mcp,permissions_json,setup_cmd,gate_matcher,default_agent,capability_profile,default_permission_mode,skill_sources_json,memory_topic,memory_status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.Name, p.TargetID, p.RepoPath, nz(p.DefaultBaseBranch, "main"), p.WorkrootOverride, nz(p.PolicyJSON, "{}"), p.VerifyCmd, p.KeepWorktrees, p.ReviewGate, nz(p.EnvJSON, "{}"), nz(p.ContextJSON, "[]"), nz(p.MCPJSON, "{}"), p.StrictMCP, nz(p.PermissionsJSON, "{}"), p.SetupCmd, p.GateMatcher, nz(p.DefaultAgent, "claude"), nz(p.CapabilityProfile, "restricted"), p.DefaultPermissionMode, nz(p.SkillSourcesJSON, "[]"), p.MemoryTopic, p.MemoryStatus, Now())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET project_id=?, workdir=?, agent=?, native_recovery_cid=?, launch_config_json=?, boot_id=?, tracking_identity=? WHERE id=? AND target_id=? AND ended_at IS NULL AND archived_at IS NULL AND (project_id IS NULL) AND tmux_session=? AND boot_id=? AND tracking_identity=?`, id, p.RepoPath, p.DefaultAgent, cid, launchConfig, boot, newTracking, sessionID, p.TargetID, tmux, originalBoot, originalTracking)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return nil, fmt.Errorf("promotion preview is stale; the session lifecycle changed")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.Project(id)
}
