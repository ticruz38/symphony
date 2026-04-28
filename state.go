package main

import (
	"sync"
	"time"
)

// RunStatus represents the phase of a run attempt.
type RunStatus string

const (
	StatusPreparingWorkspace      RunStatus = "PreparingWorkspace"
	StatusBuildingPrompt          RunStatus = "BuildingPrompt"
	StatusLaunchingAgentProcess   RunStatus = "LaunchingAgentProcess"
	StatusInitializingSession     RunStatus = "InitializingSession"
	StatusStreamingTurn           RunStatus = "StreamingTurn"
	StatusFinishing               RunStatus = "Finishing"
	StatusSucceeded               RunStatus = "Succeeded"
	StatusFailed                  RunStatus = "Failed"
	StatusTimedOut                RunStatus = "TimedOut"
	StatusStalled                 RunStatus = "Stalled"
	StatusCanceledByReconciliation RunStatus = "CanceledByReconciliation"
)

// RunningEntry tracks an active agent session.
type RunningEntry struct {
	IssueID         string
	IssueIdentifier string
	State           string
	WorkspacePath   string
	StartedAt       time.Time
	Status          RunStatus
	Attempt         int
	TurnCount       int
	LastActivity    time.Time
	PID             int
	Error           string
}

// RetryEntry tracks a scheduled retry.
type RetryEntry struct {
	IssueID      string
	Identifier   string
	Attempt      int
	DueAt        time.Time
	Error        string
}

// OrchestratorState is the single authoritative in-memory state.
type OrchestratorState struct {
	mu                    sync.RWMutex
	PollInterval          time.Duration
	MaxConcurrentAgents   int
	Running               map[string]*RunningEntry // issue_id -> entry
	Claimed               map[string]bool          // issue IDs reserved/running/retrying
	RetryQueue            map[string]*RetryEntry   // issue_id -> retry
	Completed             map[string]bool
	TotalTurnsCompleted   int
	TotalTurnsFailed      int
}

func NewOrchestratorState() *OrchestratorState {
	return &OrchestratorState{
		PollInterval:        30 * time.Second,
		MaxConcurrentAgents: 10,
		Running:             make(map[string]*RunningEntry),
		Claimed:             make(map[string]bool),
		RetryQueue:          make(map[string]*RetryEntry),
		Completed:           make(map[string]bool),
	}
}

func (s *OrchestratorState) IsClaimed(issueID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Claimed[issueID]
}

func (s *OrchestratorState) Claim(issueID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Claimed[issueID] {
		return false
	}
	s.Claimed[issueID] = true
	return true
}

func (s *OrchestratorState) Release(issueID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Claimed, issueID)
	delete(s.Running, issueID)
	delete(s.RetryQueue, issueID)
}

func (s *OrchestratorState) AddRunning(entry *RunningEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Running[entry.IssueID] = entry
}

func (s *OrchestratorState) RemoveRunning(issueID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Running, issueID)
}

func (s *OrchestratorState) GetRunning(issueID string) *RunningEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Running[issueID]
}

func (s *OrchestratorState) RunningCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Running)
}

func (s *OrchestratorState) AllRunning() []*RunningEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*RunningEntry, 0, len(s.Running))
	for _, v := range s.Running {
		out = append(out, v)
	}
	return out
}

func (s *OrchestratorState) AddRetry(entry *RetryEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RetryQueue[entry.IssueID] = entry
}

func (s *OrchestratorState) RemoveRetry(issueID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.RetryQueue, issueID)
}

func (s *OrchestratorState) AllRetries() []*RetryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*RetryEntry, 0, len(s.RetryQueue))
	for _, v := range s.RetryQueue {
		out = append(out, v)
	}
	return out
}

func (s *OrchestratorState) AvailableSlots() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := s.MaxConcurrentAgents - len(s.Running)
	if n < 0 {
		return 0
	}
	return n
}

func (s *OrchestratorState) StateRunningCount(state string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	stateNorm := normalizeState(state)
	for _, entry := range s.Running {
		if normalizeState(entry.State) == stateNorm {
			count++
		}
	}
	return count
}

func (s *OrchestratorState) UpdateConfigFrom(cfg *Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PollInterval = time.Duration(cfg.Polling.IntervalMs) * time.Millisecond
	s.MaxConcurrentAgents = cfg.Agent.MaxConcurrentAgents
}
