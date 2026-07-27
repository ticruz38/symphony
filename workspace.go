package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// ensureSymphonyToolSymlink creates a .symphony/ directory and a symlink to the current binary.
func (wm *WorkspaceManager) ensureSymphonyToolSymlink(workspacePath string, issue Issue) {
	symphonyDir := filepath.Join(workspacePath, ".symphony")
	if err := os.MkdirAll(symphonyDir, 0755); err != nil {
		wm.logger.Warn("symphony_dir_create_failed", map[string]string{
			"issue_id":         issue.ID,
			"issue_identifier": issue.Identifier,
			"error":            err.Error(),
		})
		return
	}

	exe, err := os.Executable()
	if err != nil {
		wm.logger.Warn("symphony_tool_exe_path_failed", map[string]string{
			"issue_id":         issue.ID,
			"issue_identifier": issue.Identifier,
			"error":            err.Error(),
		})
		return
	}

	linkPath := filepath.Join(symphonyDir, "symphony-tool")
	if _, err := os.Lstat(linkPath); err == nil {
		// Symlink already exists
		return
	}

	if err := os.Symlink(exe, linkPath); err != nil {
		wm.logger.Warn("symphony_tool_symlink_failed", map[string]string{
			"issue_id":         issue.ID,
			"issue_identifier": issue.Identifier,
			"exe":              exe,
			"link":             linkPath,
			"error":            err.Error(),
		})
	}
}

// WorkspaceManager handles workspace directory lifecycle.
type WorkspaceManager struct {
	cfg    *Config
	logger *Logger
}

// NewWorkspaceManager creates a workspace manager.
func NewWorkspaceManager(cfg *Config, logger *Logger) *WorkspaceManager {
	return &WorkspaceManager{cfg: cfg, logger: logger}
}

// PrepareWorkspace ensures the workspace exists for the issue.
func (wm *WorkspaceManager) PrepareWorkspace(issue Issue) (string, bool, error) {
	key := SanitizeIdentifier(issue.Identifier)
	path := filepath.Join(wm.cfg.Workspace.Root, key)

	if wm.cfg.Workspace.WorktreeBare != "" {
		return wm.prepareWorktree(path, issue)
	}

	createdNow := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0755); err != nil {
			return "", false, fmt.Errorf("workspace_create_failed: %w", err)
		}
		createdNow = true
		wm.logger.Info("workspace_created", map[string]string{
			"issue_id":         issue.ID,
			"issue_identifier": issue.Identifier,
			"workspace_path":   path,
		})
		if wm.cfg.Hooks.AfterCreate != "" {
			if err := wm.runHook("after_create", wm.cfg.Hooks.AfterCreate, path, issue); err != nil {
				// Remove partially created workspace on after_create failure
				os.RemoveAll(path)
				return "", false, fmt.Errorf("after_create_hook_failed: %w", err)
			}
		}
	}

	// Validate workspace is inside root
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false, err
	}
	absRoot, err := filepath.Abs(wm.cfg.Workspace.Root)
	if err != nil {
		return "", false, err
	}
	if !hasPrefixDir(absPath, absRoot) {
		return "", false, fmt.Errorf("invalid_workspace_cwd: %s not under %s", absPath, absRoot)
	}

	wm.ensureSymphonyToolSymlink(absPath, issue)
	return absPath, createdNow, nil
}

func (wm *WorkspaceManager) prepareWorktree(path string, issue Issue) (string, bool, error) {
	barePath := wm.cfg.Workspace.WorktreeBare
	remote := wm.cfg.Workspace.WorktreeRemote

	// Ensure bare repo exists
	if err := wm.ensureBareRepo(barePath, remote); err != nil {
		return "", false, fmt.Errorf("bare_repo_setup_failed: %w", err)
	}

	// Fetch latest changes
	if err := wm.fetchBareRepo(barePath); err != nil {
		wm.logger.Warn("bare_repo_fetch_failed", map[string]string{
			"issue_id":         issue.ID,
			"issue_identifier": issue.Identifier,
			"error":            err.Error(),
		})
	}

	branch := issueBranch(issue.Identifier)
	baseRef := wm.cfg.Workspace.MergeTarget
	if baseRef == "" {
		baseRef = "HEAD"
	}
	if issue.Parent != nil && issue.Parent.Identifier != "" {
		baseRef = issueBranch(issue.Parent.Identifier)
	} else if wm.cfg.Workspace.WorktreeRemote != "" && wm.cfg.Workspace.MergeTarget != "" {
		baseRef = "refs/remotes/origin/" + wm.cfg.Workspace.MergeTarget
	}
	syncOnResume := issue.Parent == nil || issue.Parent.Identifier == "" || wm.cfg.Workspace.ChildSyncOnStart

	// Ensure branch exists
	branchExisted := wm.branchExists(barePath, branch)
	if err := wm.ensureBranch(barePath, branch, baseRef); err != nil {
		return "", false, fmt.Errorf("branch_creation_failed: %w", err)
	}

	// Check if worktree already exists
	if _, err := os.Stat(path); err == nil {
		absPath, _ := filepath.Abs(path)
		if err := wm.commitWorktreeChanges(absPath, issue); err != nil {
			return "", false, fmt.Errorf("workspace_checkpoint_failed: %w", err)
		}
		if syncOnResume {
			wm.syncWorktreeWithBase(absPath, baseRef, issue)
		}
		wm.ensureSymphonyToolSymlink(absPath, issue)
		return absPath, false, nil
	}

	// Create worktree
	if err := wm.createWorktree(barePath, path, branch); err != nil {
		return "", false, fmt.Errorf("worktree_creation_failed: %w", err)
	}
	if branchExisted && syncOnResume {
		wm.syncWorktreeWithBase(path, baseRef, issue)
	}

	wm.logger.Info("worktree_created", map[string]string{
		"issue_id":         issue.ID,
		"issue_identifier": issue.Identifier,
		"workspace_path":   path,
		"branch":           branch,
	})

	// Run after_create hook inside the new worktree
	if wm.cfg.Hooks.AfterCreate != "" {
		if err := wm.runHook("after_create", wm.cfg.Hooks.AfterCreate, path, issue); err != nil {
			wm.removeWorktree(barePath, path)
			os.RemoveAll(path)
			return "", false, fmt.Errorf("after_create_hook_failed: %w", err)
		}
	}

	absPath, _ := filepath.Abs(path)
	wm.ensureSymphonyToolSymlink(absPath, issue)
	return absPath, true, nil
}

// syncWorktreeWithBase brings a resumed issue branch up to date before the
// agent validates or extends it. A real merge conflict is intentionally left
// in the worktree for the issue agent to resolve; silently selecting either
// side would risk losing previously merged work.
func (wm *WorkspaceManager) syncWorktreeWithBase(path, baseRef string, issue Issue) {
	if baseRef == "" || baseRef == "HEAD" {
		return
	}

	cmd := exec.Command("git", "-C", path, "merge", baseRef, "--no-edit")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		wm.logger.Warn("workspace_target_sync_needs_resolution", map[string]string{
			"issue_id":         issue.ID,
			"issue_identifier": issue.Identifier,
			"workspace_path":   path,
			"base_ref":         baseRef,
			"error":            err.Error(),
		})
		return
	}

	wm.logger.Info("workspace_target_synced", map[string]string{
		"issue_id":         issue.ID,
		"issue_identifier": issue.Identifier,
		"workspace_path":   path,
		"base_ref":         baseRef,
	})
}

func (wm *WorkspaceManager) ensureBareRepo(barePath, remote string) error {
	if _, err := os.Stat(filepath.Join(barePath, "HEAD")); err == nil {
		return nil // already exists
	}
	if remote == "" {
		return fmt.Errorf("worktree_remote not configured and bare repo does not exist: %s", barePath)
	}
	if err := os.MkdirAll(filepath.Dir(barePath), 0755); err != nil {
		return err
	}
	cmd := exec.Command("git", "clone", "--bare", "--mirror", remote, barePath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (wm *WorkspaceManager) fetchBareRepo(barePath string) error {
	args := []string{"-C", barePath, "fetch", "origin"}
	if target := strings.TrimSpace(wm.cfg.Workspace.MergeTarget); target != "" {
		// Older shared bare repositories may not have remote.origin.fetch
		// configured. In that case a successful plain fetch only refreshes
		// FETCH_HEAD and leaves the ref used to create/resume worktrees stale.
		args = append(args, "+"+target+":refs/remotes/origin/"+target)
	}
	cmd := exec.Command("git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func issueBranch(identifier string) string {
	return "symphony/" + identifier
}

func (wm *WorkspaceManager) branchExists(barePath, branch string) bool {
	cmd := exec.Command("git", "-C", barePath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return cmd.Run() == nil
}

func (wm *WorkspaceManager) ensureBranch(barePath, branch, baseRef string) error {
	// Check if branch exists
	if wm.branchExists(barePath, branch) {
		return nil // branch exists
	}
	// Create branch from the selected base ref.
	cmd := exec.Command("git", "-C", barePath, "branch", branch, baseRef)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (wm *WorkspaceManager) createWorktree(barePath, path, branch string) error {
	cmd := exec.Command("git", "-C", barePath, "worktree", "add", path, branch)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (wm *WorkspaceManager) removeWorktree(barePath, path string) {
	exec.Command("git", "-C", barePath, "worktree", "remove", path).Run()
	exec.Command("git", "-C", barePath, "worktree", "prune").Run()
}

func (wm *WorkspaceManager) mergeWorktreeBranch(barePath, branch, target string) error {
	return wm.mergeBranchIntoBranch(barePath, branch, target, "", true, nil)
}

func (wm *WorkspaceManager) commitWorktreeChanges(path string, issue Issue) error {
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		if _, fileErr := os.Stat(filepath.Join(path, ".git")); fileErr != nil {
			return fmt.Errorf("not a git worktree: %s", path)
		}
	}

	statusCmd := exec.Command("git", "-C", path, "status", "--porcelain")
	statusOut, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("status failed: %w", err)
	}
	if len(strings.TrimSpace(string(statusOut))) == 0 {
		wm.logger.Info("worktree_no_changes_to_commit", map[string]string{
			"issue_id":         issue.ID,
			"issue_identifier": issue.Identifier,
			"workspace_path":   path,
		})
		return nil
	}

	addCmd := exec.Command("git", "-C", path, "add", "-A", "--", ".")
	addCmd.Stdout = os.Stdout
	addCmd.Stderr = os.Stderr
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("add failed: %w", err)
	}

	// .symphony contains the local helper symlink; it is orchestration state, not issue work.
	resetToolCmd := exec.Command("git", "-C", path, "reset", "-q", "--", ".symphony")
	_ = resetToolCmd.Run()

	diffCmd := exec.Command("git", "-C", path, "diff", "--cached", "--quiet")
	if err := diffCmd.Run(); err == nil {
		wm.logger.Info("worktree_no_staged_changes_to_commit", map[string]string{
			"issue_id":         issue.ID,
			"issue_identifier": issue.Identifier,
			"workspace_path":   path,
		})
		return nil
	}

	msg := fmt.Sprintf("fix: resolve %s - %s", issue.Identifier, issue.Title)
	commitCmd := exec.Command("git", "-C", path, "commit", "-m", msg)
	commitCmd.Stdout = os.Stdout
	commitCmd.Stderr = os.Stderr
	commitCmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Symphony",
		"GIT_AUTHOR_EMAIL=symphony@localhost",
		"GIT_COMMITTER_NAME=Symphony",
		"GIT_COMMITTER_EMAIL=symphony@localhost",
	)
	if err := commitCmd.Run(); err != nil {
		return fmt.Errorf("commit failed: %w", err)
	}

	headCmd := exec.Command("git", "-C", path, "rev-parse", "--short", "HEAD")
	headOut, _ := headCmd.Output()
	wm.logger.Info("worktree_changes_committed", map[string]string{
		"issue_id":         issue.ID,
		"issue_identifier": issue.Identifier,
		"workspace_path":   path,
		"commit":           strings.TrimSpace(string(headOut)),
	})
	return nil
}

func (wm *WorkspaceManager) worktreeHasChanges(path string) (bool, error) {
	// The local helper symlink is orchestration state, not issue work. Ignoring
	// it lets canceled/duplicate no-code workspaces be cleaned normally.
	statusCmd := exec.Command(
		"git", "-C", path,
		"status", "--porcelain", "--untracked-files=all",
		"--", ".", ":(exclude).symphony",
	)
	statusOut, err := statusCmd.Output()
	if err != nil {
		return false, fmt.Errorf("status failed: %w", err)
	}
	return len(strings.TrimSpace(string(statusOut))) > 0, nil
}

func (wm *WorkspaceManager) mergeBranchIntoBranch(
	barePath, branch, target, strategy string,
	deleteMergedBranch bool,
	beforePush func(mergedWorktreePath string) error,
) error {
	// The app, access, and landing daemons share one bare repository and one
	// merge target. Serialize fetch/merge/validation/push across processes so
	// two individually valid terminal issues cannot race each other's push.
	lockPath := barePath + ".symphony-merge.lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open merge lock failed: %w", err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquire merge lock failed: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	// Fetch and update the remote-tracking target. A plain `git fetch origin main`
	// only refreshes FETCH_HEAD, which can leave origin/main stale and make the
	// final push non-fast-forward.
	targetRemoteRef := "refs/remotes/origin/" + target
	fetchRefspec := target + ":" + targetRemoteRef
	fetchCmd := exec.Command("git", "-C", barePath, "fetch", "origin", fetchRefspec)
	fetchCmd.Stdout = os.Stdout
	fetchCmd.Stderr = os.Stderr
	if err := fetchCmd.Run(); err != nil && target == wm.cfg.Workspace.MergeTarget {
		return fmt.Errorf("fetch target failed: %w", err)
	}

	// Create temp worktree for merging
	tmpDir := filepath.Join(os.TempDir(), "symphony_merge_"+SanitizeIdentifier(branch))
	defer os.RemoveAll(tmpDir)

	// Try the freshly updated remote-tracking target first, then local target.
	worktreeCmd := exec.Command("git", "-C", barePath, "worktree", "add", "--detach", tmpDir, targetRemoteRef)
	if err := worktreeCmd.Run(); err != nil {
		worktreeCmd = exec.Command("git", "-C", barePath, "worktree", "add", "--detach", tmpDir, target)
		if err := worktreeCmd.Run(); err != nil {
			return fmt.Errorf("create merge worktree failed: %w", err)
		}
	}
	defer exec.Command("git", "-C", barePath, "worktree", "remove", tmpDir).Run()

	// Merge the branch
	args := []string{"-C", tmpDir, "merge", branch, "--no-edit"}
	if strategy != "" {
		args = append(args, "-X", strategy)
	}
	mergeCmd := exec.Command("git", args...)
	mergeCmd.Stdout = os.Stdout
	mergeCmd.Stderr = os.Stderr
	if err := mergeCmd.Run(); err != nil {
		// Abort the failed merge
		exec.Command("git", "-C", tmpDir, "merge", "--abort").Run()
		return fmt.Errorf("merge failed (conflicts?): %w", err)
	}

	// Validate the exact candidate that will be pushed, after combining the
	// issue branch with the latest target. Validating the stale issue worktree
	// can miss integration failures introduced by concurrent merged issues.
	if beforePush != nil {
		if err := beforePush(tmpDir); err != nil {
			return fmt.Errorf("merged result validation failed: %w", err)
		}
	}

	if target == wm.cfg.Workspace.MergeTarget {
		// Push merged target to origin
		pushCmd := exec.Command("git", "-C", tmpDir, "push", "origin", "HEAD:"+target)
		pushCmd.Stdout = os.Stdout
		pushCmd.Stderr = os.Stderr
		if err := pushCmd.Run(); err != nil {
			return fmt.Errorf("push failed: %w", err)
		}
	} else {
		updateCmd := exec.Command("git", "-C", barePath, "branch", "-f", target, "HEAD")
		updateCmd.Stdout = os.Stdout
		updateCmd.Stderr = os.Stderr
		if err := updateCmd.Run(); err != nil {
			return fmt.Errorf("update target branch failed: %w", err)
		}
	}

	if deleteMergedBranch {
		// Only delete the branch after successful merge/push.
		wm.deleteBranch(barePath, branch)
	}

	return nil
}

// RunBeforeRunHook executes the before_run hook.
func (wm *WorkspaceManager) RunBeforeRunHook(path string, issue Issue) error {
	if wm.cfg.Hooks.BeforeRun == "" {
		return nil
	}
	return wm.runHook("before_run", wm.cfg.Hooks.BeforeRun, path, issue)
}

// RunAfterRunHook executes the after_run hook (errors logged, not fatal).
func (wm *WorkspaceManager) RunAfterRunHook(path string, issue Issue) {
	if wm.cfg.Hooks.AfterRun == "" {
		return
	}
	if err := wm.runHook("after_run", wm.cfg.Hooks.AfterRun, path, issue); err != nil {
		wm.logger.Warn("after_run_hook_failed", map[string]string{
			"issue_id":         issue.ID,
			"issue_identifier": issue.Identifier,
			"error":            err.Error(),
		})
	}
}

// CleanWorkspace removes the workspace for a terminal issue.
func (wm *WorkspaceManager) CleanWorkspace(issue Issue) bool {
	key := SanitizeIdentifier(issue.Identifier)
	path := filepath.Join(wm.cfg.Workspace.Root, key)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		wm.logger.Info("workspace_absent_skipping_cleanup", map[string]string{
			"issue_id":         issue.ID,
			"issue_identifier": issue.Identifier,
			"workspace_path":   path,
		})
		return true
	}

	if wm.cfg.Hooks.BeforeRemove != "" {
		if err := wm.runHook("before_remove", wm.cfg.Hooks.BeforeRemove, path, issue); err != nil {
			wm.logger.Warn("before_remove_hook_failed", map[string]string{
				"issue_id":         issue.ID,
				"issue_identifier": issue.Identifier,
				"error":            err.Error(),
			})
		}
	}

	if wm.cfg.Workspace.WorktreeBare != "" {
		shouldMerge := wm.cfg.Workspace.MergeOnTerminal && normalizeState(issue.State) == "done"
		// Attempt merge before destroying the worktree. Only Done issues are merged;
		// canceled/duplicate terminal issues are cleanup-only.
		if shouldMerge {
			branch := issueBranch(issue.Identifier)
			target := wm.cfg.Workspace.MergeTarget
			strategy := ""
			if issue.Parent != nil && issue.Parent.Identifier != "" {
				target = issueBranch(issue.Parent.Identifier)
			}
			if err := wm.commitWorktreeChanges(path, issue); err != nil {
				wm.logger.Error("commit_failed_aborting_cleanup", map[string]string{
					"issue_id":         issue.ID,
					"issue_identifier": issue.Identifier,
					"branch":           branch,
					"workspace_path":   path,
					"error":            err.Error(),
				})
				// Keep worktree alive for manual recovery.
				return false
			}
			var validateMergedResult func(string) error
			if target == wm.cfg.Workspace.MergeTarget && wm.cfg.Hooks.BeforeMerge != "" {
				validateMergedResult = func(mergedWorktreePath string) error {
					wm.ensureSymphonyToolSymlink(mergedWorktreePath, issue)
					return wm.runHook("before_merge", wm.cfg.Hooks.BeforeMerge, mergedWorktreePath, issue)
				}
			}
			if err := wm.mergeBranchIntoBranch(
				wm.cfg.Workspace.WorktreeBare,
				branch,
				target,
				strategy,
				false,
				validateMergedResult,
			); err != nil {
				wm.logger.Error("merge_failed_aborting_cleanup", map[string]string{
					"issue_id":         issue.ID,
					"issue_identifier": issue.Identifier,
					"branch":           branch,
					"target":           target,
					"error":            err.Error(),
				})
				// Keep worktree alive for manual conflict resolution
				return false
			}
			if target == wm.cfg.Workspace.MergeTarget && wm.cfg.Hooks.AfterMerge != "" {
				if err := wm.runHook("after_merge", wm.cfg.Hooks.AfterMerge, path, issue); err != nil {
					wm.logger.Error("after_merge_hook_failed_aborting_cleanup", map[string]string{
						"issue_id":         issue.ID,
						"issue_identifier": issue.Identifier,
						"branch":           branch,
						"target":           target,
						"workspace_path":   path,
						"error":            err.Error(),
					})
					// Keep worktree alive so the deployment failure is visible and retryable.
					return false
				}
			}
		} else if changed, err := wm.worktreeHasChanges(path); err != nil {
			wm.logger.Warn("terminal_non_done_status_failed_skipping_cleanup", map[string]string{
				"issue_id":         issue.ID,
				"issue_identifier": issue.Identifier,
				"state":            issue.State,
				"workspace_path":   path,
				"error":            err.Error(),
			})
			return false
		} else if changed {
			wm.logger.Info("terminal_non_done_dirty_skipping_cleanup", map[string]string{
				"issue_id":         issue.ID,
				"issue_identifier": issue.Identifier,
				"state":            issue.State,
				"workspace_path":   path,
			})
			return false
		}
		wm.removeWorktree(wm.cfg.Workspace.WorktreeBare, path)
		if shouldMerge {
			wm.deleteBranch(wm.cfg.Workspace.WorktreeBare, issueBranch(issue.Identifier))
		}
	}

	if err := os.RemoveAll(path); err != nil {
		wm.logger.Warn("workspace_cleanup_failed", map[string]string{
			"issue_id":         issue.ID,
			"issue_identifier": issue.Identifier,
			"workspace_path":   path,
			"error":            err.Error(),
		})
	} else {
		wm.logger.Info("workspace_cleaned", map[string]string{
			"issue_id":         issue.ID,
			"issue_identifier": issue.Identifier,
			"workspace_path":   path,
		})
	}
	return true
}

func (wm *WorkspaceManager) HasWorkspace(issue Issue) bool {
	key := SanitizeIdentifier(issue.Identifier)
	path := filepath.Join(wm.cfg.Workspace.Root, key)
	_, err := os.Stat(path)
	return err == nil
}

func (wm *WorkspaceManager) deleteBranch(barePath, branch string) {
	if err := exec.Command("git", "-C", barePath, "branch", "-D", branch).Run(); err != nil {
		wm.logger.Warn("branch_delete_failed", map[string]string{
			"branch": branch,
			"error":  err.Error(),
		})
	}
}

// StartupCleanup removes workspaces for terminal issues.
func (wm *WorkspaceManager) StartupCleanup(tracker Tracker) error {
	issues, err := tracker.FetchIssuesByStates(context.Background(), wm.cfg.Tracker.TerminalStates)
	if err != nil {
		wm.logger.Warn("startup_terminal_cleanup_failed", map[string]string{"error": err.Error()})
		return err
	}
	sort.SliceStable(issues, func(i, j int) bool {
		ti := issueMergeTime(issues[i])
		tj := issueMergeTime(issues[j])
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return issues[i].Identifier < issues[j].Identifier
	})
	for _, issue := range issues {
		if !wm.HasWorkspace(issue) {
			continue
		}
		hasOpenChildren := false
		for _, child := range issue.Children {
			if !wm.cfg.IsTerminal(child.State) {
				hasOpenChildren = true
				wm.logger.Info("startup_cleanup_deferred_open_children", map[string]string{
					"issue_id":         issue.ID,
					"issue_identifier": issue.Identifier,
					"child_identifier": child.Identifier,
					"child_state":      child.State,
				})
				break
			}
		}
		if hasOpenChildren {
			continue
		}
		wm.CleanWorkspace(issue)
	}
	return nil
}

func (wm *WorkspaceManager) runHook(name, script, dir string, issue Issue) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(wm.cfg.Hooks.TimeoutMs)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-lc", script)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"SYMPHONY_ISSUE_ID="+issue.ID,
		"SYMPHONY_ISSUE_IDENTIFIER="+issue.Identifier,
	)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hook %s failed: %w", name, err)
	}
	return nil
}

func hasPrefixDir(path, prefix string) bool {
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(path, prefix) || path == strings.TrimSuffix(prefix, string(filepath.Separator))
}
