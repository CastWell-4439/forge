package mcp

import (
	"context"
	"fmt"
)

// getWorkitem retrieves a work item by ID.
func (w *Worker) getWorkitem(ctx context.Context, params map[string]any) (string, error) {
	projectKey, _ := params["project_key"].(string)
	workItemID, _ := params["work_item_id"].(string)
	if projectKey == "" || workItemID == "" {
		return "", fmt.Errorf("get_workitem: project_key and work_item_id required")
	}
	return w.callTool(ctx, "get_workitem_brief", map[string]any{
		"project_key":  projectKey,
		"work_item_id": workItemID,
	})
}

// searchWorkitems searches work items using MQL.
func (w *Worker) searchWorkitems(ctx context.Context, params map[string]any) (string, error) {
	projectKey, _ := params["project_key"].(string)
	mql, _ := params["mql"].(string)
	if projectKey == "" || mql == "" {
		return "", fmt.Errorf("search_workitems: project_key and mql required")
	}
	return w.callTool(ctx, "search_by_mql", map[string]any{
		"project_key": projectKey,
		"mql":         mql,
	})
}

// listTodo lists current user's to-do items.
func (w *Worker) listTodo(ctx context.Context, params map[string]any) (string, error) {
	action := "todo"
	if a, ok := params["action"].(string); ok && a != "" {
		action = a
	}
	pageNum := 1
	if p, ok := params["page_num"].(float64); ok {
		pageNum = int(p)
	}
	return w.callTool(ctx, "list_todo", map[string]any{
		"action":   action,
		"page_num": pageNum,
	})
}

// updateField updates a work item field.
func (w *Worker) updateField(ctx context.Context, params map[string]any) (string, error) {
	projectKey, _ := params["project_key"].(string)
	workItemID, _ := params["work_item_id"].(string)
	fields, _ := params["fields"].([]any)
	if projectKey == "" || workItemID == "" {
		return "", fmt.Errorf("update_field: project_key and work_item_id required")
	}
	return w.callTool(ctx, "update_field", map[string]any{
		"project_key":  projectKey,
		"work_item_id": workItemID,
		"fields":       fields,
	})
}

// transitionState transitions a work item's state.
func (w *Worker) transitionState(ctx context.Context, params map[string]any) (string, error) {
	workItemID, _ := params["work_item_id"].(string)
	transitionID, _ := params["transition_id"].(string)
	if workItemID == "" || transitionID == "" {
		return "", fmt.Errorf("transition_state: work_item_id and transition_id required")
	}
	return w.callTool(ctx, "transition_state", map[string]any{
		"work_item_id":  workItemID,
		"transition_id": transitionID,
	})
}

// addComment adds a comment to a work item.
func (w *Worker) addComment(ctx context.Context, params map[string]any) (string, error) {
	projectKey, _ := params["project_key"].(string)
	workItemID, _ := params["work_item_id"].(string)
	content, _ := params["content"].(string)
	if workItemID == "" || content == "" {
		return "", fmt.Errorf("add_comment: work_item_id and content required")
	}
	return w.callTool(ctx, "add_comment", map[string]any{
		"project_key":  projectKey,
		"work_item_id": workItemID,
		"content":      content,
	})
}

// listComments lists comments on a work item.
func (w *Worker) listComments(ctx context.Context, params map[string]any) (string, error) {
	projectKey, _ := params["project_key"].(string)
	workItemID, _ := params["work_item_id"].(string)
	if projectKey == "" || workItemID == "" {
		return "", fmt.Errorf("list_comments: project_key and work_item_id required")
	}
	return w.callTool(ctx, "list_workitem_comments", map[string]any{
		"project_key":  projectKey,
		"work_item_id": workItemID,
	})
}
