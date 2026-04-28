package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// AgentRunner launches the coding agent subprocess (kimi-cli).
type AgentRunner struct {
	cfg    *Config
	logger *Logger
}

// NewAgentRunner creates an agent runner.
func NewAgentRunner(cfg *Config, logger *Logger) *AgentRunner {
	return &AgentRunner{cfg: cfg, logger: logger}
}

// TurnResult captures the outcome of a single turn.
type TurnResult struct {
	Success   bool
	ExitCode  int
	Error     string
	TurnCount int
}

// RunTurn executes one agent turn in the given workspace.
func (ar *AgentRunner) RunTurn(issue Issue, workspacePath string, prompt string, turnCount int) TurnResult {
	logger := ar.logger.With(map[string]string{
		"issue_id":         issue.ID,
		"issue_identifier": issue.Identifier,
		"turn":             fmt.Sprintf("%d", turnCount),
	})

	// Build command with prompt substitution
	cmdStr := ar.cfg.Codex.Command
	if strings.Contains(cmdStr, "{{.Prompt}}") {
		cmdStr = strings.ReplaceAll(cmdStr, "{{.Prompt}}", shellEscape(prompt))
	} else {
		// Append prompt as last argument if no placeholder
		cmdStr = cmdStr + " " + shellEscape(prompt)
	}

	logger.Info("turn_starting", map[string]string{
		"workspace_path": workspacePath,
		"command":        cmdStr,
	})

	// Turn timeout context
	turnTimeout := time.Duration(ar.cfg.Codex.TurnTimeoutMs) * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), turnTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-lc", cmdStr)
	cmd.Dir = workspacePath

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return TurnResult{Success: false, Error: fmt.Sprintf("stdout_pipe_error: %v", err)}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return TurnResult{Success: false, Error: fmt.Sprintf("stderr_pipe_error: %v", err)}
	}

	stallTimeout := time.Duration(ar.cfg.Codex.StallTimeoutMs) * time.Millisecond
	lastActivity := time.Now()
	var activityMu sync.Mutex
	updateActivity := func() {
		activityMu.Lock()
		lastActivity = time.Now()
		activityMu.Unlock()
	}

	// Stream stdout
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				updateActivity()
				logger.Info("agent_stdout", map[string]string{"output": string(buf[:n])})
			}
			if err != nil {
				return
			}
		}
	}()

	// Stream stderr
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderrPipe.Read(buf)
			if n > 0 {
				updateActivity()
				logger.Info("agent_stderr", map[string]string{"output": string(buf[:n])})
			}
			if err != nil {
				return
			}
		}
	}()

	if err := cmd.Start(); err != nil {
		return TurnResult{Success: false, Error: fmt.Sprintf("start_error: %v", err)}
	}

	pid := cmd.Process.Pid
	logger.Info("agent_process_started", map[string]string{"pid": fmt.Sprintf("%d", pid)})

	// Stall monitor goroutine
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				activityMu.Lock()
				elapsed := time.Since(lastActivity)
				activityMu.Unlock()
				if stallTimeout > 0 && elapsed > stallTimeout {
					logger.Warn("stall_detected_killing", map[string]string{
						"elapsed_ms": fmt.Sprintf("%d", elapsed.Milliseconds()),
					})
					cmd.Process.Kill()
					return
				}
			}
		}
	}()

	err = cmd.Wait()
	close(done)

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	if ctx.Err() == context.DeadlineExceeded {
		logger.Warn("turn_timed_out", map[string]string{"timeout_ms": fmt.Sprintf("%d", turnTimeout.Milliseconds())})
		return TurnResult{Success: false, ExitCode: exitCode, Error: "turn_timeout", TurnCount: turnCount}
	}

	// Check if killed due to stall
	activityMu.Lock()
	elapsed := time.Since(lastActivity)
	activityMu.Unlock()
	if stallTimeout > 0 && elapsed > stallTimeout {
		logger.Warn("turn_stalled", map[string]string{"stall_ms": fmt.Sprintf("%d", stallTimeout.Milliseconds())})
		return TurnResult{Success: false, ExitCode: exitCode, Error: "stalled", TurnCount: turnCount}
	}

	if exitCode != 0 {
		logger.Warn("turn_failed", map[string]string{"exit_code": fmt.Sprintf("%d", exitCode)})
		return TurnResult{Success: false, ExitCode: exitCode, Error: fmt.Sprintf("exit_code_%d", exitCode), TurnCount: turnCount}
	}

	logger.Info("turn_completed", map[string]string{"exit_code": "0"})
	return TurnResult{Success: true, ExitCode: 0, TurnCount: turnCount}
}

// shellEscape escapes a string for safe use in a single-quoted bash argument.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
