package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeBranchIntoBranchValidatesMergedResult(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Symphony Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "symphony-test@localhost")
	t.Setenv("GIT_COMMITTER_NAME", "Symphony Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "symphony-test@localhost")

	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	bare := filepath.Join(root, "integration.git")

	runGit(t, root, "init", "--bare", origin)
	runGit(t, root, "init", "-b", "main", seed)
	writeTestFile(t, filepath.Join(seed, "base.txt"), "base\n")
	runGit(t, seed, "add", "base.txt")
	runGit(t, seed, "commit", "-m", "base")
	runGit(t, seed, "remote", "add", "origin", origin)
	runGit(t, seed, "push", "-u", "origin", "main")
	runGit(t, root, "clone", "--bare", origin, bare)

	wm := NewWorkspaceManager(&Config{
		Workspace: WorkspaceConfig{
			WorktreeBare: bare,
			MergeTarget:  "main",
		},
	}, NewLogger(&bytes.Buffer{}))

	t.Run("validates the combined target and issue content before push", func(t *testing.T) {
		issueBranch := "symphony/GOOD"
		issueWorktree := filepath.Join(root, "good-worktree")
		runGit(t, bare, "branch", issueBranch, "refs/heads/main")
		runGit(t, bare, "worktree", "add", issueWorktree, issueBranch)
		writeTestFile(t, filepath.Join(issueWorktree, "issue.txt"), "issue\n")
		runGit(t, issueWorktree, "add", "issue.txt")
		runGit(t, issueWorktree, "commit", "-m", "issue")

		writeTestFile(t, filepath.Join(seed, "main.txt"), "main\n")
		runGit(t, seed, "add", "main.txt")
		runGit(t, seed, "commit", "-m", "main")
		runGit(t, seed, "push", "origin", "main")

		validated := false
		err := wm.mergeBranchIntoBranch(bare, issueBranch, "main", "", false, func(path string) error {
			validated = true
			if got := readTestFile(t, filepath.Join(path, "main.txt")); got != "main\n" {
				return fmt.Errorf("merged target content = %q", got)
			}
			if got := readTestFile(t, filepath.Join(path, "issue.txt")); got != "issue\n" {
				return fmt.Errorf("merged issue content = %q", got)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("mergeBranchIntoBranch() error = %v", err)
		}
		if !validated {
			t.Fatal("merged result was not validated")
		}

		verify := filepath.Join(root, "verify-good")
		runGit(t, root, "clone", "-b", "main", origin, verify)
		if got := readTestFile(t, filepath.Join(verify, "main.txt")); got != "main\n" {
			t.Fatalf("pushed main content = %q", got)
		}
		if got := readTestFile(t, filepath.Join(verify, "issue.txt")); got != "issue\n" {
			t.Fatalf("pushed issue content = %q", got)
		}
	})

	t.Run("reports conflicts instead of silently preferring the issue branch", func(t *testing.T) {
		runGit(t, seed, "pull", "--ff-only", "origin", "main")
		writeTestFile(t, filepath.Join(seed, "conflict.txt"), "base\n")
		runGit(t, seed, "add", "conflict.txt")
		runGit(t, seed, "commit", "-m", "conflict base")
		runGit(t, seed, "push", "origin", "main")
		runGit(t, bare, "fetch", "origin", "main:refs/remotes/origin/main")

		issueBranch := "symphony/CONFLICT"
		issueWorktree := filepath.Join(root, "conflict-worktree")
		runGit(t, bare, "branch", issueBranch, "refs/remotes/origin/main")
		runGit(t, bare, "worktree", "add", issueWorktree, issueBranch)
		writeTestFile(t, filepath.Join(issueWorktree, "conflict.txt"), "issue\n")
		runGit(t, issueWorktree, "add", "conflict.txt")
		runGit(t, issueWorktree, "commit", "-m", "issue conflict")

		writeTestFile(t, filepath.Join(seed, "conflict.txt"), "main\n")
		runGit(t, seed, "add", "conflict.txt")
		runGit(t, seed, "commit", "-m", "main conflict")
		runGit(t, seed, "push", "origin", "main")

		validated := false
		err := wm.mergeBranchIntoBranch(bare, issueBranch, "main", "", false, func(string) error {
			validated = true
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatalf("mergeBranchIntoBranch() error = %v, want conflict", err)
		}
		if validated {
			t.Fatal("validator ran for a conflicted merge")
		}

		verify := filepath.Join(root, "verify-conflict")
		runGit(t, root, "clone", "-b", "main", origin, verify)
		if got := readTestFile(t, filepath.Join(verify, "conflict.txt")); got != "main\n" {
			t.Fatalf("remote conflict resolution was overwritten: %q", got)
		}
	})
}

func TestPrepareWorkspaceCheckpointsChangesAndSyncsCurrentMain(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Symphony Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "symphony-test@localhost")
	t.Setenv("GIT_COMMITTER_NAME", "Symphony Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "symphony-test@localhost")

	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	bare := filepath.Join(root, "integration.git")
	workspaces := filepath.Join(root, "workspaces")

	runGit(t, root, "init", "--bare", origin)
	runGit(t, root, "init", "-b", "main", seed)
	writeTestFile(t, filepath.Join(seed, "base.txt"), "base\n")
	runGit(t, seed, "add", "base.txt")
	runGit(t, seed, "commit", "-m", "base")
	runGit(t, seed, "remote", "add", "origin", origin)
	runGit(t, seed, "push", "-u", "origin", "main")
	runGit(t, root, "clone", "--bare", origin, bare)
	runGit(t, bare, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	runGit(t, bare, "fetch", "origin")

	wm := NewWorkspaceManager(&Config{
		Workspace: WorkspaceConfig{
			Root:           workspaces,
			WorktreeBare:   bare,
			WorktreeRemote: origin,
			MergeTarget:    "main",
		},
	}, NewLogger(&bytes.Buffer{}))
	issue := Issue{ID: "issue-id", Identifier: "GOO-TEST", Title: "Test resume"}

	path, created, err := wm.PrepareWorkspace(issue)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first PrepareWorkspace() did not create a workspace")
	}
	writeTestFile(t, filepath.Join(path, "issue.txt"), "issue\n")

	writeTestFile(t, filepath.Join(seed, "main.txt"), "main\n")
	runGit(t, seed, "add", "main.txt")
	runGit(t, seed, "commit", "-m", "main update")
	runGit(t, seed, "push", "origin", "main")

	resumedPath, created, err := wm.PrepareWorkspace(issue)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("resumed PrepareWorkspace() unexpectedly created a workspace")
	}
	if resumedPath != path {
		t.Fatalf("resumed path = %q, want %q", resumedPath, path)
	}
	if got := readTestFile(t, filepath.Join(path, "issue.txt")); got != "issue\n" {
		t.Fatalf("checkpointed issue content = %q", got)
	}
	if got := readTestFile(t, filepath.Join(path, "main.txt")); got != "main\n" {
		t.Fatalf("synced main content = %q", got)
	}
	runGit(t, path, "merge-base", "--is-ancestor", "refs/remotes/origin/main", "HEAD")

	status := exec.Command(
		"git", "-C", path,
		"status", "--porcelain", "--untracked-files=all",
		"--", ".", ":(exclude).symphony",
	)
	output, err := status.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, output)
	}
	if len(output) != 0 {
		t.Fatalf("resumed workspace has uncommitted issue changes:\n%s", output)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
