package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// LinearClient interacts with the Linear GraphQL API.
type LinearClient struct {
	endpoint string
	apiKey   string
	projectSlug string
	client   *http.Client
}

// NewLinearClient creates a new Linear client.
func NewLinearClient(endpoint, apiKey, projectSlug string) *LinearClient {
	return &LinearClient{
		endpoint: endpoint,
		apiKey:   apiKey,
		projectSlug: projectSlug,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *LinearClient) doGraphQL(ctx context.Context, query string, variables map[string]interface{}) (map[string]interface{}, error) {
	body := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("linear_api_request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("linear_api_status: %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("linear_unknown_payload: %w", err)
	}

	if errs, ok := result["errors"]; ok && errs != nil {
		return nil, fmt.Errorf("linear_graphql_errors: %v", errs)
	}

	return result, nil
}

// FetchCandidateIssues returns issues in active states for the configured project.
func (c *LinearClient) FetchCandidateIssues(ctx context.Context, activeStates []string) ([]Issue, error) {
	query := `
	query FetchIssues($filter: IssueFilter) {
		issues(filter: $filter, first: 50) {
			nodes {
				id
				identifier
				title
				description
				priority
				state { name }
				branchName
				url
				labels { nodes { name } }
				inverseRelations { nodes { type, issue { id identifier state { name } } } }
				createdAt
				updatedAt
			}
			pageInfo { hasNextPage endCursor }
		}
	}`

	stateFilter := make([]map[string]interface{}, 0, len(activeStates))
	for _, s := range activeStates {
		stateFilter = append(stateFilter, map[string]interface{}{"name": map[string]interface{}{"eq": s}})
	}

	variables := map[string]interface{}{
		"filter": map[string]interface{}{
			"project": map[string]interface{}{"slugId": map[string]interface{}{"eq": c.projectSlug}},
			"state":   map[string]interface{}{"or": stateFilter},
		},
	}

	result, err := c.doGraphQL(ctx, query, variables)
	if err != nil {
		return nil, err
	}

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload")
	}
	issuesData, ok := data["issues"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload")
	}
	nodes, ok := issuesData["nodes"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload")
	}

	var issues []Issue
	for _, n := range nodes {
		issue, err := normalizeIssue(n)
		if err != nil {
			continue
		}
		issues = append(issues, issue)
	}

	return issues, nil
}

// FetchIssueStatesByIDs fetches current states for specific issue IDs.
func (c *LinearClient) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) (map[string]string, error) {
	if len(issueIDs) == 0 {
		return map[string]string{}, nil
	}

	query := `
	query FetchStates($ids: [ID!]!) {
		issues(filter: { id: { in: $ids } }) {
			nodes { id state { name } }
		}
	}`

	variables := map[string]interface{}{"ids": issueIDs}
	result, err := c.doGraphQL(ctx, query, variables)
	if err != nil {
		return nil, err
	}

	data, _ := result["data"].(map[string]interface{})
	issuesData, _ := data["issues"].(map[string]interface{})
	nodes, _ := issuesData["nodes"].([]interface{})

	states := make(map[string]string, len(nodes))
	for _, n := range nodes {
		node, _ := n.(map[string]interface{})
		id, _ := node["id"].(string)
		stateMap, _ := node["state"].(map[string]interface{})
		stateName, _ := stateMap["name"].(string)
		if id != "" && stateName != "" {
			states[id] = stateName
		}
	}
	return states, nil
}

// FetchIssuesByStates returns issues in the given states.
func (c *LinearClient) FetchIssuesByStates(ctx context.Context, states []string) ([]Issue, error) {
	stateFilter := make([]map[string]interface{}, 0, len(states))
	for _, s := range states {
		stateFilter = append(stateFilter, map[string]interface{}{"name": map[string]interface{}{"eq": s}})
	}

	query := `
	query FetchByStates($filter: IssueFilter) {
		issues(filter: $filter, first: 50) {
			nodes {
				id
				identifier
				title
				description
				priority
				state { name }
				branchName
				url
				labels { nodes { name } }
				inverseRelations { nodes { type, issue { id identifier state { name } } } }
				createdAt
				updatedAt
			}
			pageInfo { hasNextPage endCursor }
		}
	}`

	variables := map[string]interface{}{
		"filter": map[string]interface{}{
			"project": map[string]interface{}{"slugId": map[string]interface{}{"eq": c.projectSlug}},
			"state":   map[string]interface{}{"or": stateFilter},
		},
	}

	result, err := c.doGraphQL(ctx, query, variables)
	if err != nil {
		return nil, err
	}

	data, _ := result["data"].(map[string]interface{})
	issuesData, _ := data["issues"].(map[string]interface{})
	nodes, _ := issuesData["nodes"].([]interface{})

	var issues []Issue
	for _, n := range nodes {
		issue, err := normalizeIssue(n)
		if err != nil {
			continue
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

func normalizeIssue(raw interface{}) (Issue, error) {
	node, ok := raw.(map[string]interface{})
	if !ok {
		return Issue{}, fmt.Errorf("invalid issue node")
	}

	issue := Issue{}
	if v, ok := node["id"].(string); ok {
		issue.ID = v
	}
	if v, ok := node["identifier"].(string); ok {
		issue.Identifier = v
	}
	if v, ok := node["title"].(string); ok {
		issue.Title = v
	}
	if v, ok := node["description"].(string); ok {
		issue.Description = &v
	}
	if v, ok := node["priority"].(float64); ok {
		p := int(v)
		issue.Priority = &p
	}
	if stateMap, ok := node["state"].(map[string]interface{}); ok {
		if v, ok := stateMap["name"].(string); ok {
			issue.State = v
		}
	}
	if v, ok := node["branchName"].(string); ok {
		issue.BranchName = &v
	}
	if v, ok := node["url"].(string); ok {
		issue.URL = &v
	}
	if labelsMap, ok := node["labels"].(map[string]interface{}); ok {
		if nodes, ok := labelsMap["nodes"].([]interface{}); ok {
			for _, ln := range nodes {
				if lnode, ok := ln.(map[string]interface{}); ok {
					if name, ok := lnode["name"].(string); ok {
						issue.Labels = append(issue.Labels, normalizeState(name))
					}
				}
			}
		}
	}
	if relMap, ok := node["inverseRelations"].(map[string]interface{}); ok {
		if nodes, ok := relMap["nodes"].([]interface{}); ok {
			for _, rn := range nodes {
				if rnode, ok := rn.(map[string]interface{}); ok {
					relType, _ := rnode["type"].(string)
					if relType != "blocks" {
						continue
					}
					if issueNode, ok := rnode["issue"].(map[string]interface{}); ok {
						b := Blocker{}
						if v, ok := issueNode["id"].(string); ok {
							b.ID = &v
						}
						if v, ok := issueNode["identifier"].(string); ok {
							b.Identifier = &v
						}
						if s, ok := issueNode["state"].(map[string]interface{}); ok {
							if name, ok := s["name"].(string); ok {
								b.State = &name
							}
						}
						issue.BlockedBy = append(issue.BlockedBy, b)
					}
				}
			}
		}
	}
	if v, ok := node["createdAt"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			issue.CreatedAt = &t
		}
	}
	if v, ok := node["updatedAt"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			issue.UpdatedAt = &t
		}
	}

	return issue, nil
}
