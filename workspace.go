package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

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
			if err := wm.runHook("after_create", wm.cfg.Hooks.AfterCreate, path); err != nil {
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

	return absPath, createdNow, nil
}

// RunBeforeRunHook executes the before_run hook.
func (wm *WorkspaceManager) RunBeforeRunHook(path string, issue Issue) error {
	if wm.cfg.Hooks.BeforeRun == "" {
		return nil
	}
	return wm.runHook("before_run", wm.cfg.Hooks.BeforeRun, path)
}

// RunAfterRunHook executes the after_run hook (errors logged, not fatal).
func (wm *WorkspaceManager) RunAfterRunHook(path string, issue Issue) {
	if wm.cfg.Hooks.AfterRun == "" {
		return
	}
	if err := wm.runHook("after_run", wm.cfg.Hooks.AfterRun, path); err != nil {
		wm.logger.Warn("after_run_hook_failed", map[string]string{
			"issue_id":         issue.ID,
			"issue_identifier": issue.Identifier,
			"error":            err.Error(),
		})
	}
}

// CleanWorkspace removes the workspace for a terminal issue.
func (wm *WorkspaceManager) CleanWorkspace(issue Issue) {
	key := SanitizeIdentifier(issue.Identifier)
	path := filepath.Join(wm.cfg.Workspace.Root, key)

	if wm.cfg.Hooks.BeforeRemove != "" {
		if err := wm.runHook("before_remove", wm.cfg.Hooks.BeforeRemove, path); err != nil {
			wm.logger.Warn("before_remove_hook_failed", map[string]string{
				"issue_id":         issue.ID,
				"issue_identifier": issue.Identifier,
				"error":            err.Error(),
			})
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
}

// StartupCleanup removes workspaces for terminal issues.
func (wm *WorkspaceManager) StartupCleanup(tracker Tracker) error {
	issues, err := tracker.FetchIssuesByStates(context.Background(), wm.cfg.Tracker.TerminalStates)
	if err != nil {
		wm.logger.Warn("startup_terminal_cleanup_failed", map[string]string{"error": err.Error()})
		return err
	}
	for _, issue := range issues {
		wm.CleanWorkspace(issue)
	}
	return nil
}

func (wm *WorkspaceManager) runHook(name, script, dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(wm.cfg.Hooks.TimeoutMs)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-lc", script)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

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
