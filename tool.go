package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// runToolMode executes when the binary is invoked as `symphony-tool`.
func runToolMode(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: symphony-tool <command> [options]")
		fmt.Fprintln(os.Stderr, "Commands:")
		fmt.Fprintln(os.Stderr, "  linear-comment          Post a comment on a Linear issue")
		fmt.Fprintln(os.Stderr, "  linear-update-state     Move a Linear issue to a different state")
		fmt.Fprintln(os.Stderr, "  linear-list-states      List available workflow states")
		fmt.Fprintln(os.Stderr, "  linear-update-description Replace an issue's description")
		fmt.Fprintln(os.Stderr, "  linear-add-label        Add a label to an issue")
		os.Exit(1)
	}

	cmd := args[0]
	switch cmd {
	case "linear-comment":
		toolLinearComment(args[1:])
	case "linear-update-state":
		toolLinearUpdateState(args[1:])
	case "linear-list-states":
		toolLinearListStates(args[1:])
	case "linear-update-description":
		toolLinearUpdateDescription(args[1:])
	case "linear-add-label":
		toolLinearAddLabel(args[1:])
	case "linear-download":
		toolLinearDownload(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		os.Exit(1)
	}
}

func newToolLinearClient() *LinearClient {
	endpoint := os.Getenv("LINEAR_ENDPOINT")
	apiKey := os.Getenv("LINEAR_API_KEY")
	projectSlug := os.Getenv("LINEAR_PROJECT_SLUG")
	if endpoint == "" {
		endpoint = "https://api.linear.app/graphql"
	}
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: LINEAR_API_KEY not set")
		os.Exit(1)
	}
	return NewLinearClient(endpoint, apiKey, projectSlug)
}

func toolLinearComment(args []string) {
	fs := flag.NewFlagSet("linear-comment", flag.ExitOnError)
	issueID := fs.String("issue-id", "", "Linear issue ID")
	body := fs.String("body", "", "Comment body")
	_ = fs.Parse(args)

	if *issueID == "" || *body == "" {
		fmt.Fprintln(os.Stderr, "Usage: symphony-tool linear-comment --issue-id <id> --body <text>")
		os.Exit(1)
	}

	client := newToolLinearClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.CreateComment(ctx, *issueID, *body); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Comment created successfully")
}

func toolLinearListStates(args []string) {
	client := newToolLinearClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	states, err := client.ListWorkflowStates(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	for _, s := range states {
		fmt.Printf("- %s\n", s.Name)
	}
}

func toolLinearUpdateState(args []string) {
	fs := flag.NewFlagSet("linear-update-state", flag.ExitOnError)
	issueID := fs.String("issue-id", "", "Linear issue ID")
	stateName := fs.String("state-name", "", "Target workflow state name")
	_ = fs.Parse(args)

	if *issueID == "" || *stateName == "" {
		fmt.Fprintln(os.Stderr, "Usage: symphony-tool linear-update-state --issue-id <id> --state-name <name>")
		os.Exit(1)
	}

	client := newToolLinearClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.UpdateIssueState(ctx, *issueID, *stateName); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Issue moved to state: %s\n", *stateName)
}

func toolLinearUpdateDescription(args []string) {
	fs := flag.NewFlagSet("linear-update-description", flag.ExitOnError)
	issueID := fs.String("issue-id", "", "Linear issue ID")
	description := fs.String("description", "", "Description text to append (Markdown supported)")
	replace := fs.Bool("replace", false, "Replace entire description instead of appending")
	_ = fs.Parse(args)

	if *issueID == "" || *description == "" {
		fmt.Fprintln(os.Stderr, "Usage: symphony-tool linear-update-description --issue-id <id> --description <text> [--replace]")
		os.Exit(1)
	}

	client := newToolLinearClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.UpdateIssueDescription(ctx, *issueID, *description, !*replace); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if *replace {
		fmt.Println("Description replaced successfully")
	} else {
		fmt.Println("Description appended successfully")
	}
}

func toolLinearAddLabel(args []string) {
	fs := flag.NewFlagSet("linear-add-label", flag.ExitOnError)
	issueID := fs.String("issue-id", "", "Linear issue ID")
	label := fs.String("label", "", "Label name")
	_ = fs.Parse(args)

	if *issueID == "" || *label == "" {
		fmt.Fprintln(os.Stderr, "Usage: symphony-tool linear-add-label --issue-id <id> --label <name>")
		os.Exit(1)
	}

	client := newToolLinearClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.AddIssueLabel(ctx, *issueID, *label); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Label added: %s\n", *label)
}

func toolLinearDownload(args []string) {
	fs := flag.NewFlagSet("linear-download", flag.ExitOnError)
	url := fs.String("url", "", "Linear upload URL")
	output := fs.String("output", "", "Output file path")
	_ = fs.Parse(args)

	if *url == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "Usage: symphony-tool linear-download --url <linear-upload-url> --output <path>")
		os.Exit(1)
	}

	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: LINEAR_API_KEY not set")
		os.Exit(1)
	}

	req, err := http.NewRequest("GET", *url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Authorization", apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	out, err := os.Create(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer out.Close()

	written, err := io.Copy(out, resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Downloaded %d bytes to %s\n", written, *output)
}

// isToolMode returns true when the binary was invoked as `symphony-tool`.
func isToolMode() bool {
	return filepath.Base(os.Args[0]) == "symphony-tool"
}
