package main

import (
	"bytes"
	"fmt"
	"text/template"
)

// RenderPrompt renders the workflow prompt template with issue and attempt data.
func RenderPrompt(wf *Workflow, issue Issue, attempt int) (string, error) {
	tmpl, err := template.New("prompt").Option("missingkey=error").Parse(wf.PromptTemplate)
	if err != nil {
		return "", fmt.Errorf("template_parse_error: %w", err)
	}

	data := map[string]interface{}{
		"issue":   issue,
		"attempt": attempt,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("template_render_error: %w", err)
	}

	result := buf.String()
	if result == "" {
		result = "You are working on an issue from Linear."
	}
	return result, nil
}

// RenderContinuationPrompt builds a brief prompt for continuation turns.
func RenderContinuationPrompt(wf *Workflow, issue Issue, attempt int, turnCount int) string {
	// For kimi-cli, we use a short continuation instruction.
	// The full prompt history is not preserved in a thread; instead the workspace files serve as state.
	return fmt.Sprintf("Continue working on %s: %s. This is turn %d of the session. Review the current workspace state and proceed with the next steps.", issue.Identifier, issue.Title, turnCount)
}
