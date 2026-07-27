package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the resolved runtime configuration derived from WORKFLOW.md.
type Config struct {
	Tracker   TrackerConfig   `yaml:"tracker"`
	Polling   PollingConfig   `yaml:"polling"`
	Workspace WorkspaceConfig `yaml:"workspace"`
	Hooks     HooksConfig     `yaml:"hooks"`
	Agent     AgentConfig     `yaml:"agent"`
	Codex     CodexConfig     `yaml:"codex"`
}

// TrackerConfig defines issue tracker settings.
type TrackerConfig struct {
	Kind           string   `yaml:"kind"`
	Endpoint       string   `yaml:"endpoint"`
	APIKey         string   `yaml:"api_key"`
	ProjectSlug    string   `yaml:"project_slug"`
	ActiveStates   []string `yaml:"active_states"`
	TerminalStates []string `yaml:"terminal_states"`
}

// PollingConfig defines poll cadence.
type PollingConfig struct {
	IntervalMs int `yaml:"interval_ms"`
}

// WorkspaceConfig defines workspace settings.
type WorkspaceConfig struct {
	Root                      string   `yaml:"root"`
	WorktreeBare              string   `yaml:"worktree_bare"`
	WorktreeRemote            string   `yaml:"worktree_remote"`
	MergeOnTerminal           bool     `yaml:"merge_on_terminal"`
	MergeTarget               string   `yaml:"merge_target"`
	ChildRequiresParentBranch bool     `yaml:"child_requires_parent_branch"`
	ChildParentReadyStates    []string `yaml:"child_parent_ready_states"`
	ChildSyncOnStart          bool     `yaml:"child_sync_on_start"`
	ParentReviewState         string   `yaml:"parent_review_state"`
}

// HooksConfig defines lifecycle shell hooks.
type HooksConfig struct {
	AfterCreate  string `yaml:"after_create"`
	BeforeRun    string `yaml:"before_run"`
	AfterRun     string `yaml:"after_run"`
	BeforeMerge  string `yaml:"before_merge"`
	AfterMerge   string `yaml:"after_merge"`
	BeforeRemove string `yaml:"before_remove"`
	TimeoutMs    int    `yaml:"timeout_ms"`
}

// AgentConfig defines agent concurrency and retry settings.
type AgentConfig struct {
	MaxConcurrentAgents        int            `yaml:"max_concurrent_agents"`
	MaxTurns                   int            `yaml:"max_turns"`
	MaxRetryBackoffMs          int            `yaml:"max_retry_backoff_ms"`
	MaxConcurrentAgentsByState map[string]int `yaml:"max_concurrent_agents_by_state"`
}

// CodexConfig defines agent subprocess settings.
type CodexConfig struct {
	Command          string `yaml:"command"`
	ModelLabelPrefix string `yaml:"model_label_prefix"`
	TurnTimeoutMs    int    `yaml:"turn_timeout_ms"`
	ReadTimeoutMs    int    `yaml:"read_timeout_ms"`
	StallTimeoutMs   *int   `yaml:"stall_timeout_ms"`
}

// Workflow holds the parsed WORKFLOW.md payload.
type Workflow struct {
	Config         Config
	PromptTemplate string
}

var envVarRegex = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)$`)

// LoadWorkflow reads and parses WORKFLOW.md from the given path.
func LoadWorkflow(path string) (*Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("missing_workflow_file: %w", err)
	}

	content := string(data)
	var rawCfg map[string]interface{}
	var promptTemplate string

	if strings.HasPrefix(content, "---") {
		end := strings.Index(content[3:], "---")
		if end >= 0 {
			frontMatter := content[3 : end+3]
			promptTemplate = strings.TrimSpace(content[end+6:])
			if err := yaml.Unmarshal([]byte(frontMatter), &rawCfg); err != nil {
				return nil, fmt.Errorf("workflow_parse_error: %w", err)
			}
		} else {
			promptTemplate = strings.TrimSpace(content)
		}
	} else {
		promptTemplate = strings.TrimSpace(content)
	}

	if rawCfg == nil {
		rawCfg = make(map[string]interface{})
	}

	// Marshal back to YAML so we can unmarshal into typed Config
	cfgBytes, err := yaml.Marshal(rawCfg)
	if err != nil {
		return nil, fmt.Errorf("workflow_parse_error: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(cfgBytes, &cfg); err != nil {
		return nil, fmt.Errorf("workflow_parse_error: %w", err)
	}

	// Apply defaults
	applyDefaults(&cfg, filepath.Dir(path))

	// Resolve env vars
	if err := resolveEnvVars(&cfg); err != nil {
		return nil, err
	}

	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}

	return &Workflow{Config: cfg, PromptTemplate: promptTemplate}, nil
}

func applyDefaults(cfg *Config, workflowDir string) {
	if cfg.Tracker.Kind == "" {
		cfg.Tracker.Kind = "linear"
	}
	if cfg.Tracker.Endpoint == "" {
		cfg.Tracker.Endpoint = "https://api.linear.app/graphql"
	}
	if cfg.Tracker.ActiveStates == nil {
		cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	}
	if cfg.Tracker.TerminalStates == nil {
		cfg.Tracker.TerminalStates = []string{"Closed", "Cancelled", "Canceled", "Duplicate", "Done"}
	}
	if cfg.Polling.IntervalMs == 0 {
		cfg.Polling.IntervalMs = 30000
	}
	if cfg.Workspace.Root == "" {
		cfg.Workspace.Root = filepath.Join(os.TempDir(), "symphony_workspaces")
	}
	// Expand ~ and resolve relative paths
	cfg.Workspace.Root = resolvePath(cfg.Workspace.Root, workflowDir)
	cfg.Workspace.WorktreeBare = resolvePath(cfg.Workspace.WorktreeBare, workflowDir)
	if cfg.Workspace.MergeTarget == "" {
		cfg.Workspace.MergeTarget = "main"
	}
	if cfg.Workspace.ChildParentReadyStates == nil {
		cfg.Workspace.ChildParentReadyStates = []string{"In Review", "Done"}
	}
	if cfg.Workspace.ParentReviewState == "" {
		cfg.Workspace.ParentReviewState = "In Review"
	}
	cfg.Workspace.ChildRequiresParentBranch = true
	cfg.Workspace.ChildSyncOnStart = true

	if cfg.Hooks.TimeoutMs == 0 {
		cfg.Hooks.TimeoutMs = 60000
	}
	if cfg.Agent.MaxConcurrentAgents == 0 {
		cfg.Agent.MaxConcurrentAgents = 10
	}
	if cfg.Agent.MaxTurns == 0 {
		cfg.Agent.MaxTurns = 500
	}
	if cfg.Agent.MaxRetryBackoffMs == 0 {
		cfg.Agent.MaxRetryBackoffMs = 300000
	}
	if cfg.Agent.MaxConcurrentAgentsByState == nil {
		cfg.Agent.MaxConcurrentAgentsByState = make(map[string]int)
	}
	if cfg.Codex.Command == "" {
		cfg.Codex.Command = "codex exec --dangerously-bypass-approvals-and-sandbox"
	}
	if cfg.Codex.ModelLabelPrefix == "" {
		cfg.Codex.ModelLabelPrefix = "model:"
	}
	if cfg.Codex.TurnTimeoutMs == 0 {
		cfg.Codex.TurnTimeoutMs = 3600000
	}
	if cfg.Codex.ReadTimeoutMs == 0 {
		cfg.Codex.ReadTimeoutMs = 5000
	}
	if cfg.Codex.StallTimeoutMs == nil {
		defaultStall := 300000
		cfg.Codex.StallTimeoutMs = &defaultStall
	}
}

func resolvePath(p, base string) string {
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	p, _ = filepath.Abs(p)
	return p
}

func resolveEnvVars(cfg *Config) error {
	cfg.Tracker.APIKey = resolveEnv(cfg.Tracker.APIKey)
	cfg.Workspace.Root = resolveEnvPath(cfg.Workspace.Root)
	return nil
}

func resolveEnv(s string) string {
	m := envVarRegex.FindStringSubmatch(s)
	if len(m) == 2 {
		return os.Getenv(m[1])
	}
	return s
}

func resolveEnvPath(p string) string {
	m := envVarRegex.FindStringSubmatch(p)
	if len(m) == 2 {
		v := os.Getenv(m[1])
		if v != "" {
			return v
		}
	}
	return p
}

func validateConfig(cfg *Config) error {
	if cfg.Tracker.Kind != "linear" {
		return fmt.Errorf("unsupported_tracker_kind: %s", cfg.Tracker.Kind)
	}
	if cfg.Tracker.APIKey == "" {
		return fmt.Errorf("missing_tracker_api_key")
	}
	if cfg.Tracker.ProjectSlug == "" {
		return fmt.Errorf("missing_tracker_project_slug")
	}
	if cfg.Codex.Command == "" {
		return fmt.Errorf("missing_agent_command")
	}
	if cfg.Agent.MaxTurns <= 0 {
		return fmt.Errorf("invalid max_turns")
	}
	return nil
}

// ActiveStateSet returns a set of normalized active states.
func (c *Config) ActiveStateSet() map[string]bool {
	s := make(map[string]bool, len(c.Tracker.ActiveStates))
	for _, st := range c.Tracker.ActiveStates {
		s[normalizeState(st)] = true
	}
	return s
}

// TerminalStateSet returns a set of normalized terminal states.
func (c *Config) TerminalStateSet() map[string]bool {
	s := make(map[string]bool, len(c.Tracker.TerminalStates))
	for _, st := range c.Tracker.TerminalStates {
		s[normalizeState(st)] = true
	}
	return s
}

// ParentReadyStateSet returns states that allow child issues to start.
func (c *Config) ParentReadyStateSet() map[string]bool {
	s := make(map[string]bool, len(c.Workspace.ChildParentReadyStates))
	for _, st := range c.Workspace.ChildParentReadyStates {
		s[normalizeState(st)] = true
	}
	return s
}

// IsParentReady returns true if a parent issue state can be used as a child base.
func (c *Config) IsParentReady(state string) bool {
	return c.ParentReadyStateSet()[normalizeState(state)]
}

// IsTerminal returns true if the given state is terminal.
func (c *Config) IsTerminal(state string) bool {
	return c.TerminalStateSet()[normalizeState(state)]
}

// IsActive returns true if the given state is active.
func (c *Config) IsActive(state string) bool {
	return c.ActiveStateSet()[normalizeState(state)]
}

// WatchWorkflow watches the workflow file for changes and reloads it.
func WatchWorkflow(path string, onChange func(*Workflow)) error {
	// Simple polling-based watch to avoid fsnotify dependency for now.
	// Can be upgraded to fsnotify later.
	var lastMod time.Time
	for {
		info, err := os.Stat(path)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		if info.ModTime().After(lastMod) {
			lastMod = info.ModTime()
			wf, err := LoadWorkflow(path)
			if err != nil {
				// Log error but keep running with old config
				fmt.Fprintf(os.Stderr, "workflow_reload_error: %v\n", err)
			} else {
				onChange(wf)
			}
		}
		time.Sleep(5 * time.Second)
	}
}

// SanitizeIdentifier creates a workspace key from an issue identifier.
func SanitizeIdentifier(id string) string {
	var b bytes.Buffer
	for _, r := range id {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
