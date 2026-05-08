package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

// LinearClient interacts with the Linear GraphQL API.
type LinearClient struct {
	endpoint    string
	apiKey      string
	projectSlug string
	client      *http.Client
}

// NewLinearClient creates a new Linear client.
func NewLinearClient(endpoint, apiKey, projectSlug string) *LinearClient {
	return &LinearClient{
		endpoint:    endpoint,
		apiKey:      apiKey,
		projectSlug: projectSlug,
		client:      &http.Client{Timeout: 30 * time.Second},
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
				parent { id identifier title description state { name } url }
				children { nodes { id identifier title description state { name } url } }
				inverseRelations { nodes { type, issue { id identifier state { name } } } }
				comments { nodes { id body user { name } createdAt parentId children { nodes { id body user { name } createdAt parentId children { nodes { id body user { name } createdAt parentId } } } } } }
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

// FetchIssueByID returns one issue with hierarchy fields.
func (c *LinearClient) FetchIssueByID(ctx context.Context, issueID string) (Issue, error) {
	query := `
	query FetchIssue($id: String!) {
		issue(id: $id) {
			id
			identifier
			title
			description
			priority
			state { name }
			branchName
			url
			labels { nodes { name } }
			parent { id identifier title description state { name } url }
			children { nodes { id identifier title description state { name } url } }
			inverseRelations { nodes { type, issue { id identifier state { name } } } }
			comments { nodes { id body user { name } createdAt parentId children { nodes { id body user { name } createdAt parentId children { nodes { id body user { name } createdAt parentId } } } } } }
			createdAt
			updatedAt
		}
	}`

	result, err := c.doGraphQL(ctx, query, map[string]interface{}{"id": issueID})
	if err != nil {
		return Issue{}, err
	}
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return Issue{}, fmt.Errorf("linear_unknown_payload")
	}
	rawIssue, ok := data["issue"].(map[string]interface{})
	if !ok || rawIssue == nil {
		return Issue{}, fmt.Errorf("issue not found: %s", issueID)
	}
	return normalizeIssue(rawIssue)
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
				parent { id identifier title description state { name } url }
				children { nodes { id identifier title description state { name } url } }
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
	if parentMap, ok := node["parent"].(map[string]interface{}); ok && parentMap != nil {
		parent := normalizeRelatedIssue(parentMap)
		if parent.ID != "" {
			issue.Parent = &parent
		}
	}
	if childrenMap, ok := node["children"].(map[string]interface{}); ok {
		if nodes, ok := childrenMap["nodes"].([]interface{}); ok {
			for _, childNode := range nodes {
				child := normalizeRelatedIssue(childNode)
				if child.ID != "" {
					issue.Children = append(issue.Children, child)
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
	if commentsMap, ok := node["comments"].(map[string]interface{}); ok {
		if nodes, ok := commentsMap["nodes"].([]interface{}); ok {
			for _, cn := range nodes {
				if cnode, ok := cn.(map[string]interface{}); ok {
					// Skip replies that already appear under their parent comment
					if parentID, _ := cnode["parentId"].(string); parentID != "" {
						continue
					}
				}
				if c := normalizeComment(cn); c.Body != "" {
					issue.Comments = append(issue.Comments, c)
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

var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func normalizeComment(raw interface{}) Comment {
	node, ok := raw.(map[string]interface{})
	if !ok {
		return Comment{}
	}
	c := Comment{}
	if v, ok := node["body"].(string); ok {
		c.Body = v
	}
	if userMap, ok := node["user"].(map[string]interface{}); ok {
		if v, ok := userMap["name"].(string); ok {
			c.UserName = v
		}
	}
	if v, ok := node["createdAt"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			c.CreatedAt = t
		}
	}
	if childrenMap, ok := node["children"].(map[string]interface{}); ok {
		if nodes, ok := childrenMap["nodes"].([]interface{}); ok {
			for _, child := range nodes {
				if childComment := normalizeComment(child); childComment.Body != "" {
					c.Children = append(c.Children, childComment)
				}
			}
		}
	}
	return c
}

func normalizeRelatedIssue(raw interface{}) RelatedIssue {
	node, ok := raw.(map[string]interface{})
	if !ok {
		return RelatedIssue{}
	}
	issue := RelatedIssue{}
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
	if v, ok := node["url"].(string); ok {
		issue.URL = &v
	}
	if stateMap, ok := node["state"].(map[string]interface{}); ok {
		if v, ok := stateMap["name"].(string); ok {
			issue.State = v
		}
	}
	return issue
}

func isUUID(s string) bool {
	return uuidRegex.MatchString(s)
}

// ResolveIssueID returns the internal UUID for an issue.
// If the input already looks like a UUID, it is returned as-is.
// Otherwise it is treated as a human identifier (e.g. "GOO-5") and looked up.
func (c *LinearClient) ResolveIssueID(ctx context.Context, idOrIdentifier string) (string, error) {
	if isUUID(idOrIdentifier) {
		return idOrIdentifier, nil
	}
	query := `
	query IssueById($id: String!) {
		issue(id: $id) {
			id
		}
	}`
	variables := map[string]interface{}{
		"id": idOrIdentifier,
	}
	result, err := c.doGraphQL(ctx, query, variables)
	if err != nil {
		return "", err
	}
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("linear_unknown_payload")
	}
	issue, ok := data["issue"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("issue not found: %s", idOrIdentifier)
	}
	id, ok := issue["id"].(string)
	if !ok || id == "" {
		return "", fmt.Errorf("linear_unknown_payload")
	}
	return id, nil
}

// CreateComment adds a comment to an issue.
func (c *LinearClient) CreateComment(ctx context.Context, issueID, body string) error {
	query := `
	mutation CommentCreate($input: CommentCreateInput!) {
		commentCreate(input: $input) {
			success
			comment { id }
		}
	}`
	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"issueId": issueID,
			"body":    body,
		},
	}
	result, err := c.doGraphQL(ctx, query, variables)
	if err != nil {
		return err
	}
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("linear_unknown_payload")
	}
	commentCreate, ok := data["commentCreate"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("linear_unknown_payload")
	}
	if success, ok := commentCreate["success"].(bool); ok && success {
		return nil
	}
	return fmt.Errorf("linear_comment_create_failed")
}

// WorkflowState represents a Linear workflow state.
type WorkflowState struct {
	ID   string
	Name string
}

// ListWorkflowStates returns all workflow states in the organization.
func (c *LinearClient) ListWorkflowStates(ctx context.Context) ([]WorkflowState, error) {
	query := `
	query WorkflowStates {
		workflowStates {
			nodes { id name }
		}
	}`
	result, err := c.doGraphQL(ctx, query, nil)
	if err != nil {
		return nil, err
	}
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload")
	}
	wsData, ok := data["workflowStates"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload")
	}
	nodes, ok := wsData["nodes"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload")
	}
	var states []WorkflowState
	for _, n := range nodes {
		node, ok := n.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := node["id"].(string)
		name, _ := node["name"].(string)
		if id != "" && name != "" {
			states = append(states, WorkflowState{ID: id, Name: name})
		}
	}
	return states, nil
}

// UpdateIssueState moves an issue to a workflow state matched by name.
func (c *LinearClient) UpdateIssueState(ctx context.Context, issueID, stateName string) error {
	resolvedID, err := c.ResolveIssueID(ctx, issueID)
	if err != nil {
		return fmt.Errorf("failed to resolve issue: %w", err)
	}

	states, err := c.ListWorkflowStates(ctx)
	if err != nil {
		return fmt.Errorf("failed to list workflow states: %w", err)
	}
	var stateID string
	targetNorm := normalizeState(stateName)
	for _, s := range states {
		if normalizeState(s.Name) == targetNorm {
			stateID = s.ID
			break
		}
	}
	if stateID == "" {
		var names []string
		for _, s := range states {
			names = append(names, s.Name)
		}
		return fmt.Errorf("workflow state not found: %q (available: %v)", stateName, names)
	}

	query := `
	mutation IssueUpdate($id: String!, $input: IssueUpdateInput!) {
		issueUpdate(id: $id, input: $input) {
			success
			issue { id state { name } }
		}
	}`
	variables := map[string]interface{}{
		"id": resolvedID,
		"input": map[string]interface{}{
			"stateId": stateID,
		},
	}
	result, err := c.doGraphQL(ctx, query, variables)
	if err != nil {
		return err
	}
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("linear_unknown_payload")
	}
	issueUpdate, ok := data["issueUpdate"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("linear_unknown_payload")
	}
	if success, ok := issueUpdate["success"].(bool); ok && success {
		return nil
	}
	return fmt.Errorf("linear_issue_update_failed")
}

// GetIssueDescription returns the current description of an issue.
func (c *LinearClient) GetIssueDescription(ctx context.Context, issueID string) (string, error) {
	resolvedID, err := c.ResolveIssueID(ctx, issueID)
	if err != nil {
		return "", fmt.Errorf("failed to resolve issue: %w", err)
	}
	query := `
	query IssueDescription($id: String!) {
		issue(id: $id) {
			description
		}
	}`
	variables := map[string]interface{}{"id": resolvedID}
	result, err := c.doGraphQL(ctx, query, variables)
	if err != nil {
		return "", err
	}
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("linear_unknown_payload")
	}
	issue, ok := data["issue"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("linear_unknown_payload")
	}
	desc, _ := issue["description"].(string)
	return desc, nil
}

// UpdateIssueDescription replaces an issue's description.
func (c *LinearClient) UpdateIssueDescription(ctx context.Context, issueID, description string, appendMode bool) error {
	resolvedID, err := c.ResolveIssueID(ctx, issueID)
	if err != nil {
		return fmt.Errorf("failed to resolve issue: %w", err)
	}

	finalDesc := description
	if appendMode {
		current, err := c.GetIssueDescription(ctx, issueID)
		if err == nil && current != "" {
			finalDesc = current + "\n\n---\n\n" + description
		}
	}

	query := `
	mutation IssueUpdate($id: String!, $input: IssueUpdateInput!) {
		issueUpdate(id: $id, input: $input) {
			success
			issue { id }
		}
	}`
	variables := map[string]interface{}{
		"id": resolvedID,
		"input": map[string]interface{}{
			"description": finalDesc,
		},
	}
	result, err := c.doGraphQL(ctx, query, variables)
	if err != nil {
		return err
	}
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("linear_unknown_payload")
	}
	issueUpdate, ok := data["issueUpdate"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("linear_unknown_payload")
	}
	if success, ok := issueUpdate["success"].(bool); ok && success {
		return nil
	}
	return fmt.Errorf("linear_issue_update_failed")
}

// IssueLabel represents a Linear issue label.
type IssueLabel struct {
	ID   string
	Name string
}

// ListIssueLabels returns all labels in the organization.
func (c *LinearClient) ListIssueLabels(ctx context.Context) ([]IssueLabel, error) {
	query := `
	query IssueLabels {
		issueLabels {
			nodes { id name }
		}
	}`
	result, err := c.doGraphQL(ctx, query, nil)
	if err != nil {
		return nil, err
	}
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload")
	}
	labelsData, ok := data["issueLabels"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload")
	}
	nodes, ok := labelsData["nodes"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload")
	}
	var labels []IssueLabel
	for _, n := range nodes {
		node, ok := n.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := node["id"].(string)
		name, _ := node["name"].(string)
		if id != "" && name != "" {
			labels = append(labels, IssueLabel{ID: id, Name: name})
		}
	}
	return labels, nil
}

// GetIssueLabelIDs returns the current label IDs on an issue.
func (c *LinearClient) GetIssueLabelIDs(ctx context.Context, issueID string) ([]string, error) {
	resolvedID, err := c.ResolveIssueID(ctx, issueID)
	if err != nil {
		return nil, err
	}
	query := `
	query IssueLabels($id: String!) {
		issue(id: $id) {
			labels { nodes { id } }
		}
	}`
	variables := map[string]interface{}{"id": resolvedID}
	result, err := c.doGraphQL(ctx, query, variables)
	if err != nil {
		return nil, err
	}
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload")
	}
	issue, ok := data["issue"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload")
	}
	labelsMap, ok := issue["labels"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload")
	}
	nodes, ok := labelsMap["nodes"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("linear_unknown_payload")
	}
	var ids []string
	for _, n := range nodes {
		node, ok := n.(map[string]interface{})
		if !ok {
			continue
		}
		if id, ok := node["id"].(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// AddIssueLabel adds a label to an issue by name.
func (c *LinearClient) AddIssueLabel(ctx context.Context, issueID, labelName string) error {
	labels, err := c.ListIssueLabels(ctx)
	if err != nil {
		return fmt.Errorf("failed to list labels: %w", err)
	}
	var labelID string
	targetNorm := normalizeState(labelName)
	for _, l := range labels {
		if normalizeState(l.Name) == targetNorm {
			labelID = l.ID
			break
		}
	}
	if labelID == "" {
		var names []string
		for _, l := range labels {
			names = append(names, l.Name)
		}
		return fmt.Errorf("label not found: %q (available: %v)", labelName, names)
	}

	currentIDs, err := c.GetIssueLabelIDs(ctx, issueID)
	if err != nil {
		return fmt.Errorf("failed to get current labels: %w", err)
	}

	// Append new label if not already present
	for _, id := range currentIDs {
		if id == labelID {
			return nil // already has label
		}
	}
	newIDs := append(currentIDs, labelID)

	resolvedID, err := c.ResolveIssueID(ctx, issueID)
	if err != nil {
		return fmt.Errorf("failed to resolve issue: %w", err)
	}

	query := `
	mutation IssueUpdate($id: String!, $input: IssueUpdateInput!) {
		issueUpdate(id: $id, input: $input) {
			success
			issue { id }
		}
	}`
	variables := map[string]interface{}{
		"id": resolvedID,
		"input": map[string]interface{}{
			"labelIds": newIDs,
		},
	}
	result, err := c.doGraphQL(ctx, query, variables)
	if err != nil {
		return err
	}
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("linear_unknown_payload")
	}
	issueUpdate, ok := data["issueUpdate"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("linear_unknown_payload")
	}
	if success, ok := issueUpdate["success"].(bool); ok && success {
		return nil
	}
	return fmt.Errorf("linear_issue_update_failed")
}
