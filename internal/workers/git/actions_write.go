package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// createBranch creates and checks out a new branch.
func (w *Worker) createBranch(ctx context.Context, params map[string]any) (string, error) {
	name, _ := params["name"].(string)
	if name == "" {
		return "", fmt.Errorf("create_branch: name required")
	}
	base, _ := params["base"].(string)
	if base == "" {
		base = w.config.MainBranch
	}
	// Ensure we're on the base branch and up to date
	if _, err := w.runGit(ctx, "checkout", base); err != nil {
		return "", err
	}
	if _, err := w.runGit(ctx, "pull", "origin", base); err != nil {
		return "", err
	}
	return w.runGit(ctx, "checkout", "-b", name)
}

// commit stages and commits changes.
func (w *Worker) commit(ctx context.Context, params map[string]any) (string, error) {
	message, _ := params["message"].(string)
	if message == "" {
		return "", fmt.Errorf("commit: message required")
	}
	// Stage all changes
	files, _ := params["files"].(string)
	if files == "" {
		files = "."
	}
	if _, err := w.runGit(ctx, "add", files); err != nil {
		return "", err
	}
	return w.runGit(ctx, "commit", "-m", message)
}

// push pushes the current branch to remote.
func (w *Worker) push(ctx context.Context, params map[string]any) (string, error) {
	branch, _ := params["branch"].(string)
	if branch == "" {
		// Get current branch
		var err error
		branch, err = w.runGit(ctx, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return "", err
		}
	}
	return w.runGit(ctx, "push", "origin", branch)
}

// createMR creates a merge request via GitLab API.
func (w *Worker) createMR(ctx context.Context, params map[string]any) (string, error) {
	if w.config.GitLabURL == "" || w.config.GitLabToken == "" {
		return "", fmt.Errorf("create_mr: gitlab_url and gitlab_token required in project config")
	}

	title, _ := params["title"].(string)
	sourceBranch, _ := params["source_branch"].(string)
	targetBranch, _ := params["target_branch"].(string)
	description, _ := params["description"].(string)

	if title == "" || sourceBranch == "" {
		return "", fmt.Errorf("create_mr: title and source_branch required")
	}
	if targetBranch == "" {
		targetBranch = w.config.TestTarget
	}

	payload := map[string]any{
		"source_branch": sourceBranch,
		"target_branch": targetBranch,
		"title":         title,
		"description":   description,
	}
	body, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests", w.config.GitLabURL, w.config.ProjectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("PRIVATE-TOKEN", w.config.GitLabToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create_mr: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var errBody map[string]any
		json.NewDecoder(resp.Body).Decode(&errBody)
		return "", fmt.Errorf("create_mr: status %d: %v", resp.StatusCode, errBody)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	mrURL, _ := result["web_url"].(string)
	return mrURL, nil
}
