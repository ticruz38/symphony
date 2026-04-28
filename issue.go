package main

import "time"

// Issue is the normalized issue record used by orchestration, prompt rendering, and logs.
type Issue struct {
	ID           string     `json:"id"`
	Identifier   string     `json:"identifier"`
	Title        string     `json:"title"`
	Description  *string    `json:"description"`
	Priority     *int       `json:"priority"`
	State        string     `json:"state"`
	BranchName   *string    `json:"branch_name"`
	URL          *string    `json:"url"`
	Labels       []string   `json:"labels"`
	BlockedBy    []Blocker  `json:"blocked_by"`
	CreatedAt    *time.Time `json:"created_at"`
	UpdatedAt    *time.Time `json:"updated_at"`
}

// Blocker represents an issue that blocks another issue.
type Blocker struct {
	ID         *string `json:"id"`
	Identifier *string `json:"identifier"`
	State      *string `json:"state"`
}

// IsBlocked returns true if any blocker is in a non-terminal state.
func (i Issue) IsBlocked(terminalStates map[string]bool) bool {
	for _, b := range i.BlockedBy {
		if b.State == nil {
			continue
		}
		if !terminalStates[normalizeState(*b.State)] {
			return true
		}
	}
	return false
}

func normalizeState(s string) string {
	// simple lowercase normalization
	out := []rune(s)
	for i, r := range out {
		if r >= 'A' && r <= 'Z' {
			out[i] = r + ('a' - 'A')
		}
	}
	return string(out)
}
