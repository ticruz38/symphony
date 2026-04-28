package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	var workflowPath string
	flag.StringVar(&workflowPath, "workflow", "WORKFLOW.md", "Path to WORKFLOW.md")
	flag.Parse()

	logger := NewLogger(os.Stderr)

	// Load initial workflow
	wf, err := LoadWorkflow(workflowPath)
	if err != nil {
		logger.Error("startup_failed", map[string]string{"error": err.Error()})
		fmt.Fprintf(os.Stderr, "Startup failed: %v\n", err)
		os.Exit(1)
	}
	logger.Info("workflow_loaded", map[string]string{"path": workflowPath})

	cfg := &wf.Config

	// Initialize components
	tracker := NewLinearClient(cfg.Tracker.Endpoint, cfg.Tracker.APIKey, cfg.Tracker.ProjectSlug)
	wm := NewWorkspaceManager(cfg, logger)
	agent := NewAgentRunner(cfg, logger)
	orchestrator := NewOrchestrator(cfg, tracker, wm, agent, logger, wf)

	// Startup cleanup
	if err := wm.StartupCleanup(tracker); err != nil {
		logger.Warn("startup_cleanup_issue", map[string]string{"error": err.Error()})
	}

	// Start orchestrator
	orchestrator.Start()

	// Watch workflow for hot reload
	go func() {
		WatchWorkflow(workflowPath, func(newWf *Workflow) {
			logger.Info("workflow_hot_reload")
			orchestrator.UpdateWorkflow(newWf)
		})
	}()

	logger.Info("symphony_started", map[string]string{
		"poll_interval_ms": fmt.Sprintf("%d", cfg.Polling.IntervalMs),
		"max_concurrent":   fmt.Sprintf("%d", cfg.Agent.MaxConcurrentAgents),
		"max_turns":        fmt.Sprintf("%d", cfg.Agent.MaxTurns),
		"workspace_root":   cfg.Workspace.Root,
	})

	// Wait for interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("shutting_down")
	orchestrator.Stop()
	logger.Info("shutdown_complete")
}
