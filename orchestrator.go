package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// Tracker is the interface for issue tracker adapters.
type Tracker interface {
	FetchCandidateIssues(ctx context.Context, activeStates []string) ([]Issue, error)
	FetchIssuesByStates(ctx context.Context, states []string) ([]Issue, error)
	FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) (map[string]string, error)
}

// Orchestrator manages polling, dispatch, retry, and reconciliation.
type Orchestrator struct {
	state       *OrchestratorState
	cfg         *Config
	tracker     Tracker
	wm          *WorkspaceManager
	agent       *AgentRunner
	logger      *Logger
	mu          sync.Mutex
	ticker      *time.Ticker
	retryTimers map[string]*time.Timer
	quit        chan struct{}
	resultCh    chan workerResult
	wf          *Workflow
}

type workerResult struct {
	issueID       string
	identifier    string
	workspacePath string
	attempt       int
	success       bool
	error         string
	status        RunStatus
	turnCount     int
}

// NewOrchestrator creates an orchestrator.
func NewOrchestrator(cfg *Config, tracker Tracker, wm *WorkspaceManager, agent *AgentRunner, logger *Logger, wf *Workflow) *Orchestrator {
	state := NewOrchestratorState()
	state.UpdateConfigFrom(cfg)
	return &Orchestrator{
		state:       state,
		cfg:         cfg,
		tracker:     tracker,
		wm:          wm,
		agent:       agent,
		logger:      logger,
		retryTimers: make(map[string]*time.Timer),
		quit:        make(chan struct{}),
		resultCh:    make(chan workerResult, 100),
		wf:          wf,
	}
}

// Start begins the orchestration loop.
func (o *Orchestrator) Start() {
	o.logger.Info("orchestrator_starting")
	o.ticker = time.NewTicker(o.state.PollInterval)
	go o.loop()
}

// Stop halts the orchestrator.
func (o *Orchestrator) Stop() {
	close(o.quit)
	o.ticker.Stop()
	o.mu.Lock()
	for _, t := range o.retryTimers {
		t.Stop()
	}
	o.mu.Unlock()
}

// UpdateWorkflow updates the active workflow and config.
func (o *Orchestrator) UpdateWorkflow(wf *Workflow) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.wf = wf
	o.cfg = &wf.Config
	o.state.UpdateConfigFrom(o.cfg)
	// Reset ticker with new interval
	o.ticker.Reset(o.state.PollInterval)
	o.logger.Info("workflow_reloaded", map[string]string{
		"poll_interval_ms": fmt.Sprintf("%d", o.state.PollInterval.Milliseconds()),
	})
}

func (o *Orchestrator) loop() {
	// Immediate first tick
	o.tick()
	for {
		select {
		case <-o.quit:
			return
		case <-o.ticker.C:
			o.tick()
		case res := <-o.resultCh:
			o.handleWorkerResult(res)
		}
	}
}

func (o *Orchestrator) tick() {
	o.mu.Lock()
	cfg := o.cfg
	o.mu.Unlock()

	// 1. Reconcile running issues
	o.reconcile(cfg)

	// 2. Validate config
	if err := validateConfig(cfg); err != nil {
		o.logger.Error("dispatch_validation_failed", map[string]string{"error": err.Error()})
		return
	}

	// 3. Fetch candidate issues
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	candidates, err := o.tracker.FetchCandidateIssues(ctx, cfg.Tracker.ActiveStates)
	cancel()
	if err != nil {
		o.logger.Error("candidate_fetch_failed", map[string]string{"error": err.Error()})
		return
	}

	// 4. Sort candidates
	terminalSet := cfg.TerminalStateSet()
	o.sortCandidates(candidates, terminalSet)

	// 5. Dispatch
	available := o.state.AvailableSlots()
	for _, issue := range candidates {
		if available <= 0 {
			break
		}
		if !o.isDispatchEligible(issue, cfg, terminalSet) {
			continue
		}
		if !o.state.Claim(issue.ID) {
			continue
		}
		o.dispatch(issue, cfg, 0)
		available--
	}
}

func (o *Orchestrator) reconcile(cfg *Config) {
	running := o.state.AllRunning()
	if len(running) == 0 {
		return
	}

	// Stall detection
	stallTimeout := time.Duration(cfg.Codex.StallTimeoutMs) * time.Millisecond
	now := time.Now()
	for _, entry := range running {
		if stallTimeout <= 0 {
			continue
		}
		if now.Sub(entry.LastActivity) > stallTimeout {
			o.logger.Warn("reconcile_stall_kill", map[string]string{
				"issue_id":         entry.IssueID,
				"issue_identifier": entry.IssueIdentifier,
			})
			if entry.PID > 0 {
				terminateProcess(entry.PID)
			}
			o.state.RemoveRunning(entry.IssueID)
			o.scheduleRetry(entry.IssueID, entry.IssueIdentifier, entry.Attempt+1, false, "stalled")
		}
	}

	// Tracker state refresh
	ids := make([]string, 0, len(running))
	for _, entry := range running {
		ids = append(ids, entry.IssueID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	states, err := o.tracker.FetchIssueStatesByIDs(ctx, ids)
	cancel()
	if err != nil {
		o.logger.Error("state_refresh_failed", map[string]string{"error": err.Error()})
		return
	}

	for _, entry := range running {
		stateName, ok := states[entry.IssueID]
		if !ok {
			o.logger.Info("issue_not_found_stopping", map[string]string{
				"issue_id":         entry.IssueID,
				"issue_identifier": entry.IssueIdentifier,
			})
			if entry.PID > 0 {
				terminateProcess(entry.PID)
			}
			o.state.RemoveRunning(entry.IssueID)
			o.state.Release(entry.IssueID)
			continue
		}

		if cfg.IsTerminal(stateName) {
			o.logger.Info("issue_terminal_stopping", map[string]string{
				"issue_id":         entry.IssueID,
				"issue_identifier": entry.IssueIdentifier,
				"state":            stateName,
			})
			if entry.PID > 0 {
				terminateProcess(entry.PID)
			}
			o.state.RemoveRunning(entry.IssueID)
			o.wm.CleanWorkspace(Issue{ID: entry.IssueID, Identifier: entry.IssueIdentifier})
			o.state.Release(entry.IssueID)
		} else if !cfg.IsActive(stateName) {
			o.logger.Info("issue_inactive_stopping", map[string]string{
				"issue_id":         entry.IssueID,
				"issue_identifier": entry.IssueIdentifier,
				"state":            stateName,
			})
			if entry.PID > 0 {
				terminateProcess(entry.PID)
			}
			o.state.RemoveRunning(entry.IssueID)
			o.state.Release(entry.IssueID)
		}
	}
}

func (o *Orchestrator) isDispatchEligible(issue Issue, cfg *Config, terminalSet map[string]bool) bool {
	if issue.ID == "" || issue.Identifier == "" || issue.Title == "" || issue.State == "" {
		return false
	}
	if !cfg.IsActive(issue.State) || cfg.IsTerminal(issue.State) {
		return false
	}
	if o.state.GetRunning(issue.ID) != nil {
		return false
	}
	if o.state.IsClaimed(issue.ID) {
		return false
	}

	// Per-state concurrency limit
	stateKey := normalizeState(issue.State)
	if perStateLimit, ok := cfg.Agent.MaxConcurrentAgentsByState[stateKey]; ok && perStateLimit > 0 {
		if o.state.StateRunningCount(stateKey) >= perStateLimit {
			return false
		}
	}

	if issue.IsBlocked(terminalSet) && stateKey == "todo" {
		return false
	}
	return true
}

func (o *Orchestrator) sortCandidates(candidates []Issue, terminalSet map[string]bool) {
	sort.SliceStable(candidates, func(i, j int) bool {
		pi, pj := 9999, 9999
		if candidates[i].Priority != nil {
			pi = *candidates[i].Priority
		}
		if candidates[j].Priority != nil {
			pj = *candidates[j].Priority
		}
		if pi != pj {
			return pi < pj
		}
		if candidates[i].CreatedAt != nil && candidates[j].CreatedAt != nil {
			if !candidates[i].CreatedAt.Equal(*candidates[j].CreatedAt) {
				return candidates[i].CreatedAt.Before(*candidates[j].CreatedAt)
			}
		}
		return candidates[i].Identifier < candidates[j].Identifier
	})
}

func (o *Orchestrator) dispatch(issue Issue, cfg *Config, attempt int) {
	logger := o.logger.With(map[string]string{
		"issue_id":         issue.ID,
		"issue_identifier": issue.Identifier,
	})
	logger.Info("dispatching")

	entry := &RunningEntry{
		IssueID:         issue.ID,
		IssueIdentifier: issue.Identifier,
		State:           issue.State,
		StartedAt:       time.Now(),
		Status:          StatusPreparingWorkspace,
		Attempt:         attempt,
		TurnCount:       0,
		LastActivity:    time.Now(),
	}
	o.state.AddRunning(entry)

	go o.runWorker(issue, cfg, entry)
}

func (o *Orchestrator) runWorker(issue Issue, cfg *Config, entry *RunningEntry) {
	logger := o.logger.With(map[string]string{
		"issue_id":         issue.ID,
		"issue_identifier": issue.Identifier,
	})

	workspacePath, _, err := o.wm.PrepareWorkspace(issue)
	if err != nil {
		logger.Error("workspace_preparation_failed", map[string]string{"error": err.Error()})
		o.resultCh <- workerResult{
			issueID: issue.ID, identifier: issue.Identifier,
			workspacePath: "", attempt: entry.Attempt,
			success: false, error: err.Error(), status: StatusFailed, turnCount: 0,
		}
		return
	}
	entry.WorkspacePath = workspacePath
	entry.Status = StatusBuildingPrompt

	if err := o.wm.RunBeforeRunHook(workspacePath, issue); err != nil {
		logger.Error("before_run_hook_failed", map[string]string{"error": err.Error()})
		o.wm.RunAfterRunHook(workspacePath, issue)
		o.resultCh <- workerResult{
			issueID: issue.ID, identifier: issue.Identifier,
			workspacePath: workspacePath, attempt: entry.Attempt,
			success: false, error: err.Error(), status: StatusFailed, turnCount: 0,
		}
		return
	}

	o.mu.Lock()
	wf := o.wf
	o.mu.Unlock()

	prompt, err := RenderPrompt(wf, issue, entry.Attempt)
	if err != nil {
		logger.Error("prompt_render_failed", map[string]string{"error": err.Error()})
		o.wm.RunAfterRunHook(workspacePath, issue)
		o.resultCh <- workerResult{
			issueID: issue.ID, identifier: issue.Identifier,
			workspacePath: workspacePath, attempt: entry.Attempt,
			success: false, error: err.Error(), status: StatusFailed, turnCount: 0,
		}
		return
	}
	entry.Status = StatusLaunchingAgentProcess

	turnResult := o.agent.RunTurn(issue, workspacePath, prompt, entry.TurnCount+1)
	entry.TurnCount = turnResult.TurnCount
	entry.LastActivity = time.Now()

	if turnResult.Success {
		entry.Status = StatusSucceeded
	} else {
		entry.Status = StatusFailed
	}

	o.wm.RunAfterRunHook(workspacePath, issue)

	o.resultCh <- workerResult{
		issueID:       issue.ID,
		identifier:    issue.Identifier,
		workspacePath: workspacePath,
		attempt:       entry.Attempt,
		success:       turnResult.Success,
		error:         turnResult.Error,
		status:        entry.Status,
		turnCount:     entry.TurnCount,
	}
}

func (o *Orchestrator) handleWorkerResult(res workerResult) {
	o.state.RemoveRunning(res.issueID)

	if res.success {
		o.state.TotalTurnsCompleted++
		o.logger.Info("worker_succeeded", map[string]string{
			"issue_id":         res.issueID,
			"issue_identifier": res.identifier,
			"turn_count":       fmt.Sprintf("%d", res.turnCount),
		})
		o.scheduleRetry(res.issueID, res.identifier, res.attempt+1, true, "")
	} else {
		o.state.TotalTurnsFailed++
		o.logger.Warn("worker_failed", map[string]string{
			"issue_id":         res.issueID,
			"issue_identifier": res.identifier,
			"error":            res.error,
			"status":           string(res.status),
		})
		o.scheduleRetry(res.issueID, res.identifier, res.attempt+1, false, res.error)
	}
}

func (o *Orchestrator) scheduleRetry(issueID, identifier string, attempt int, isContinuation bool, errMsg string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if t, ok := o.retryTimers[issueID]; ok {
		t.Stop()
		delete(o.retryTimers, issueID)
	}

	var delay time.Duration
	if isContinuation {
		delay = 1 * time.Second
	} else {
		backoffMs := 10000 * (1 << (attempt - 1))
		maxBackoff := o.cfg.Agent.MaxRetryBackoffMs
		if backoffMs > maxBackoff {
			backoffMs = maxBackoff
		}
		delay = time.Duration(backoffMs) * time.Millisecond
	}

	logger := o.logger.With(map[string]string{
		"issue_id":         issueID,
		"issue_identifier": identifier,
		"attempt":          fmt.Sprintf("%d", attempt),
		"delay_ms":         fmt.Sprintf("%d", delay.Milliseconds()),
	})
	logger.Info("retry_scheduled", map[string]string{"reason": errMsg})

	timer := time.AfterFunc(delay, func() {
		o.handleRetry(issueID, identifier, attempt)
	})
	o.retryTimers[issueID] = timer
	o.state.AddRetry(&RetryEntry{
		IssueID:    issueID,
		Identifier: identifier,
		Attempt:    attempt,
		DueAt:      time.Now().Add(delay),
		Error:      errMsg,
	})
}

func (o *Orchestrator) handleRetry(issueID, identifier string, attempt int) {
	o.mu.Lock()
	delete(o.retryTimers, issueID)
	o.state.RemoveRetry(issueID)
	o.mu.Unlock()

	// Fetch active candidates and find this issue
	o.mu.Lock()
	cfg := o.cfg
	o.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	candidates, err := o.tracker.FetchCandidateIssues(ctx, cfg.Tracker.ActiveStates)
	cancel()
	if err != nil {
		o.logger.Error("retry_candidate_fetch_failed", map[string]string{
			"issue_id": issueID,
			"error":    err.Error(),
		})
		// Requeue with longer delay
		o.scheduleRetry(issueID, identifier, attempt+1, false, "candidate_fetch_failed")
		return
	}

	var found *Issue
	for i := range candidates {
		if candidates[i].ID == issueID {
			found = &candidates[i]
			break
		}
	}

	if found == nil {
		o.logger.Info("retry_issue_not_found_releasing", map[string]string{"issue_id": issueID})
		o.state.Release(issueID)
		return
	}

	terminalSet := cfg.TerminalStateSet()
	if !o.isDispatchEligible(*found, cfg, terminalSet) {
		o.logger.Info("retry_issue_no_longer_eligible", map[string]string{
			"issue_id": issueID,
			"state":    found.State,
		})
		o.state.Release(issueID)
		return
	}

	if !o.state.Claim(issueID) {
		// Shouldn't happen since we just released if not eligible, but handle race
		return
	}

	o.dispatch(*found, cfg, attempt)
}

func terminateProcess(pid int) {
	proc, err := os.FindProcess(pid)
	if err == nil {
		proc.Kill()
	}
}
