package git

import (
	"context"
	"fmt"
	"strconv"
)

// pull fetches latest from remote and merges.
func (w *Worker) pull(ctx context.Context, params map[string]any) (string, error) {
	branch, _ := params["branch"].(string)
	if branch == "" {
		branch = w.config.MainBranch
	}
	if _, err := w.runGit(ctx, "checkout", branch); err != nil {
		return "", err
	}
	return w.runGit(ctx, "pull", "origin", branch)
}

// log returns recent git log entries.
func (w *Worker) log(ctx context.Context, params map[string]any) (string, error) {
	count := 10
	if c, ok := params["count"].(float64); ok && c > 0 {
		count = int(c)
	}
	format := "%h %s (%an, %cr)"
	if f, ok := params["format"].(string); ok && f != "" {
		format = f
	}
	return w.runGit(ctx, "log", fmt.Sprintf("-n%d", count), fmt.Sprintf("--format=%s", format))
}

// diff shows the diff between branches or commits.
func (w *Worker) diff(ctx context.Context, params map[string]any) (string, error) {
	target, _ := params["target"].(string)
	if target == "" {
		// Default: diff working tree
		return w.runGit(ctx, "diff", "--stat")
	}
	source, _ := params["source"].(string)
	if source == "" {
		source = "HEAD"
	}
	maxLines := 500
	if m, ok := params["max_lines"].(float64); ok && m > 0 {
		maxLines = int(m)
	}
	out, err := w.runGit(ctx, "diff", source+".."+target)
	if err != nil {
		return "", err
	}
	// Truncate to max lines
	lines := splitLines(out)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, fmt.Sprintf("\n... (truncated at %d lines)", maxLines))
	}
	return joinLines(lines), nil
}

// blame shows blame info for a file.
func (w *Worker) blame(ctx context.Context, params map[string]any) (string, error) {
	file, _ := params["file"].(string)
	if file == "" {
		return "", fmt.Errorf("blame: file parameter required")
	}
	args := []string{"blame", "--porcelain"}
	if start, ok := params["start_line"].(float64); ok {
		end := start + 20
		if e, ok2 := params["end_line"].(float64); ok2 {
			end = e
		}
		args = append(args, fmt.Sprintf("-L%s,%s", strconv.Itoa(int(start)), strconv.Itoa(int(end))))
	}
	args = append(args, file)
	return w.runGit(ctx, args...)
}

// search greps the repository for a pattern.
func (w *Worker) search(ctx context.Context, params map[string]any) (string, error) {
	pattern, _ := params["pattern"].(string)
	if pattern == "" {
		return "", fmt.Errorf("search: pattern parameter required")
	}
	args := []string{"grep", "-n", "--color=never", pattern}
	if path, ok := params["path"].(string); ok && path != "" {
		args = append(args, "--", path)
	}
	return w.runGit(ctx, args...)
}

// --- helpers ---

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func joinLines(lines []string) string {
	result := ""
	for i, l := range lines {
		if i > 0 {
			result += "\n"
		}
		result += l
	}
	return result
}
