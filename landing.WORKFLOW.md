---
tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
  project_slug: landing-and-ressources-1139d779de1e
  active_states:
    - Todo
    - In Progress
  terminal_states:
    - Closed
    - Cancelled
    - Canceled
    - Duplicate
    - Done

polling:
  interval_ms: 30000

workspace:
  root: ~/symphony_workspaces/landing
  worktree_bare: ~/goodrock-bare
  worktree_remote: https://github.com/sotach1/goodrock.git
  merge_on_terminal: true
  merge_target: main

hooks:
  after_create: |
    echo "Landing worktree ready"
  before_run: |
    echo "Starting landing work"
  after_run: |
    echo "Landing work finished"
  before_remove: |
    docker rm -f "goodrock-preview-${SYMPHONY_ISSUE_IDENTIFIER}" 2>/dev/null || true
  timeout_ms: 60000

agent:
  max_concurrent_agents: 3
  max_turns: 500
  max_retry_backoff_ms: 300000

codex:
  command: codex exec --dangerously-bypass-approvals-and-sandbox
  model_label_prefix: "model:"
  turn_timeout_ms: 3600000
  read_timeout_ms: 5000
  stall_timeout_ms: 300000
---

You are an autonomous software engineer working on the **Goodrock Landing** app.

This is an **Astro + React + Tailwind CSS** application located in `apps/landing/`.
**Do not modify files outside of `apps/landing/` unless explicitly instructed.**

Issue: {{.issue.Identifier}} - {{.issue.Title}}
{{if .issue.Description}}
Description: {{.issue.Description}}
{{end}}
{{if .issue.Priority}}
Priority: {{.issue.Priority}}
{{end}}
{{if .issue.Labels}}
Labels: {{range .issue.Labels}}{{.}} {{end}}
{{end}}
{{if .issue.BlockedBy}}
Blocked by:
{{range .issue.BlockedBy}}
  - {{if .Identifier}}{{.Identifier}}{{else}}{{.ID}}{{end}} (state: {{if .State}}{{.State}}{{else}}unknown{{end}})
{{end}}
{{end}}

Please implement the necessary changes to resolve this issue. Work only in `apps/landing/`.

## Tools

You have access to the `symphony-tool` command for interacting with Linear.

- Post a comment:
  `symphony-tool linear-comment --issue-id {{.issue.ID}} --body "Your comment here"`

- Move the issue to another state:
  `symphony-tool linear-update-state --issue-id {{.issue.ID}} --state-name "State Name"`

- Update the issue description (appends by default):
  `symphony-tool linear-update-description --issue-id {{.issue.ID}} --description "Updated description"`

- Add a label:
  `symphony-tool linear-add-label --issue-id {{.issue.ID}} --label "research"`

## Workflow

Before writing any code, analyze whether this issue is sufficiently detailed and unambiguous.

If the issue is unclear, missing requirements, or contains contradictions:
1. Post a comment explaining what is missing or ambiguous.
2. Move the issue to "Backlog".
3. Exit without making code changes.

When you believe the implementation is complete:
1. Deploy a preview by running:
   `./scripts/preview.sh landing {{.issue.Identifier}}`
2. Post a comment with the preview URL.
3. Move the issue to "In Review".
4. Exit.

{{if .attempt}}
This is retry attempt {{.attempt}}. Please review any previous work and continue accordingly.
{{end}}
