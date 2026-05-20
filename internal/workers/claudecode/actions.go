package claudecode

import (
	"context"
	"fmt"
)

// implement generates code for a new feature.
// Required params: workdir, task_description
// Optional params: files_hint, branch
func (w *Worker) implement(ctx context.Context, params map[string]any) (string, error) {
	workdir, err := getParam(params, "workdir")
	if err != nil {
		return "", err
	}
	desc, err := getParam(params, "task_description")
	if err != nil {
		return "", err
	}

	if err := w.validateBranch(ctx, workdir); err != nil {
		return "", fmt.Errorf("implement: %w", err)
	}

	filesHint := getOptionalParam(params, "files_hint", "")
	prompt := buildImplementPrompt(desc, filesHint)

	output, err := w.execClaude(ctx, workdir, prompt)
	if err != nil {
		return resultJSON("implement", "error", err.Error()), nil
	}
	return resultJSON("implement", "success", output), nil
}

// fix applies a bug fix based on the description.
// Required params: workdir, bug_description
// Optional params: error_log, files_hint
func (w *Worker) fix(ctx context.Context, params map[string]any) (string, error) {
	workdir, err := getParam(params, "workdir")
	if err != nil {
		return "", err
	}
	desc, err := getParam(params, "bug_description")
	if err != nil {
		return "", err
	}

	if err := w.validateBranch(ctx, workdir); err != nil {
		return "", fmt.Errorf("fix: %w", err)
	}

	errorLog := getOptionalParam(params, "error_log", "")
	filesHint := getOptionalParam(params, "files_hint", "")
	prompt := buildFixPrompt(desc, errorLog, filesHint)

	output, err := w.execClaude(ctx, workdir, prompt)
	if err != nil {
		return resultJSON("fix", "error", err.Error()), nil
	}
	return resultJSON("fix", "success", output), nil
}

// refactor restructures existing code without changing behavior.
// Required params: workdir, refactor_goal
// Optional params: files_hint, constraints
func (w *Worker) refactor(ctx context.Context, params map[string]any) (string, error) {
	workdir, err := getParam(params, "workdir")
	if err != nil {
		return "", err
	}
	goal, err := getParam(params, "refactor_goal")
	if err != nil {
		return "", err
	}

	if err := w.validateBranch(ctx, workdir); err != nil {
		return "", fmt.Errorf("refactor: %w", err)
	}

	filesHint := getOptionalParam(params, "files_hint", "")
	constraints := getOptionalParam(params, "constraints", "")
	prompt := buildRefactorPrompt(goal, filesHint, constraints)

	output, err := w.execClaude(ctx, workdir, prompt)
	if err != nil {
		return resultJSON("refactor", "error", err.Error()), nil
	}
	return resultJSON("refactor", "success", output), nil
}

// addTest writes tests for existing code.
// Required params: workdir, target_file
// Optional params: test_framework, coverage_goal
func (w *Worker) addTest(ctx context.Context, params map[string]any) (string, error) {
	workdir, err := getParam(params, "workdir")
	if err != nil {
		return "", err
	}
	target, err := getParam(params, "target_file")
	if err != nil {
		return "", err
	}

	if err := w.validateBranch(ctx, workdir); err != nil {
		return "", fmt.Errorf("add_test: %w", err)
	}

	framework := getOptionalParam(params, "test_framework", "")
	coverageGoal := getOptionalParam(params, "coverage_goal", "80%")
	prompt := buildTestPrompt(target, framework, coverageGoal)

	output, err := w.execClaude(ctx, workdir, prompt)
	if err != nil {
		return resultJSON("add_test", "error", err.Error()), nil
	}
	return resultJSON("add_test", "success", output), nil
}

// --- Prompt builders ---

func buildImplementPrompt(desc, filesHint string) string {
	p := fmt.Sprintf("Implement the following feature:\n\n%s", desc)
	if filesHint != "" {
		p += fmt.Sprintf("\n\nRelevant files to look at or modify:\n%s", filesHint)
	}
	p += "\n\nRequirements:\n- Write clean, well-documented code\n- Follow existing code style and conventions\n- Add error handling\n- Do NOT modify any config files or secrets"
	return p
}

func buildFixPrompt(desc, errorLog, filesHint string) string {
	p := fmt.Sprintf("Fix the following bug:\n\n%s", desc)
	if errorLog != "" {
		p += fmt.Sprintf("\n\nError log:\n```\n%s\n```", errorLog)
	}
	if filesHint != "" {
		p += fmt.Sprintf("\n\nLikely affected files:\n%s", filesHint)
	}
	p += "\n\nRequirements:\n- Identify root cause before fixing\n- Minimal change to fix the issue\n- Do NOT introduce new features\n- Do NOT modify config files or secrets"
	return p
}

func buildRefactorPrompt(goal, filesHint, constraints string) string {
	p := fmt.Sprintf("Refactor code with the following goal:\n\n%s", goal)
	if filesHint != "" {
		p += fmt.Sprintf("\n\nFiles to refactor:\n%s", filesHint)
	}
	if constraints != "" {
		p += fmt.Sprintf("\n\nConstraints:\n%s", constraints)
	}
	p += "\n\nRequirements:\n- Do NOT change external behavior\n- Improve readability and maintainability\n- Run existing tests to verify no regression\n- Do NOT modify config files or secrets"
	return p
}

func buildTestPrompt(target, framework, coverageGoal string) string {
	p := fmt.Sprintf("Write comprehensive tests for: %s", target)
	if framework != "" {
		p += fmt.Sprintf("\n\nTest framework: %s", framework)
	}
	p += fmt.Sprintf("\n\nCoverage goal: %s", coverageGoal)
	p += "\n\nRequirements:\n- Cover happy path, edge cases, and error cases\n- Use table-driven tests where appropriate\n- Mock external dependencies\n- Follow existing test patterns in the project"
	return p
}
