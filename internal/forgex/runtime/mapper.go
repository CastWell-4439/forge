package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	forgestorage "github.com/castwell/forge/internal/storage"
)

const runIDPrefix = "forge_"

// RunIDForWorkflow returns the deterministic ForgeX run id for a Forge workflow.
func RunIDForWorkflow(workflowID string) string {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return runIDPrefix + "unknown"
	}
	return runIDPrefix + workflowID
}

func taskPacketForWorkflow(workflowID string, event *forgestorage.Event) model.TaskPacket {
	return model.TaskPacket{
		ID:   "forge_workflow_" + workflowID,
		Name: "Forge workflow " + workflowID,
		Goal: "Observe Forge runtime workflow events and persist them as ForgeX run artifacts.",
		Inputs: map[string]any{
			"forge_workflow_id": workflowID,
		},
		Constraints: []string{
			"observe-only integration",
			"do not change Forge workflow execution semantics",
		},
		Success: []string{
			"Forge workflow/task events are recorded in ForgeX events.jsonl",
			"Coordinator-side worker task calls are recorded in ForgeX tool_calls.jsonl when observed",
			"Optional policy and contract shadow checks are recorded without blocking Forge execution",
			"Terminal workflow events update ForgeX run metadata and report",
		},
		Metadata: map[string]string{
			"source":            "forge_runtime",
			"forge_workflow_id": workflowID,
			"first_event_type":  string(event.Type),
		},
	}
}

func initialRunForWorkflow(workflowID string, event *forgestorage.Event) model.Run {
	timestamp := event.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	return model.Run{
		ID:        RunIDForWorkflow(workflowID),
		TaskID:    "forge_workflow_" + workflowID,
		Name:      "Forge workflow " + workflowID,
		Status:    model.RunRunning,
		StartedAt: timestamp.UTC(),
		Summary:   "Observe-only Forge runtime run created from Coordinator event stream.",
	}
}

func eventToForgeX(event *forgestorage.Event) model.Event {
	timestamp := event.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	payload := payloadToAny(event.Payload)
	data := map[string]any{
		"source":             "forge_runtime",
		"forge_event_id":     event.ID,
		"forge_workflow_id":  event.WorkflowID,
		"forge_task_id":      event.TaskID,
		"forge_event_type":   string(event.Type),
		"forge_sequence_num": event.SequenceNum,
	}
	if payload != nil {
		data["forge_payload"] = payload
	}
	return model.Event{
		ID:        fmt.Sprintf("forge_evt_%d", event.SequenceNum),
		RunID:     RunIDForWorkflow(event.WorkflowID),
		Type:      mapEventType(event.Type),
		Message:   fmt.Sprintf("forge runtime event: %s", event.Type),
		Timestamp: timestamp.UTC(),
		Data:      data,
	}
}

func mapEventType(eventType forgestorage.EventType) model.EventType {
	switch eventType {
	case forgestorage.EventWorkflowSubmitted, forgestorage.EventWorkflowStarted:
		return model.EventRunStarted
	case forgestorage.EventTaskScheduled, forgestorage.EventTaskStarted:
		return model.EventStepStarted
	case forgestorage.EventTaskCompleted:
		return model.EventToolSucceeded
	case forgestorage.EventTaskFailed:
		return model.EventToolFailed
	case forgestorage.EventWorkflowCompleted, forgestorage.EventWorkflowFailed:
		return model.EventRunFinished
	default:
		return model.EventStepStarted
	}
}

func terminalRunStatus(eventType forgestorage.EventType) (model.RunStatus, bool) {
	switch eventType {
	case forgestorage.EventWorkflowCompleted:
		return model.RunSucceeded, true
	case forgestorage.EventWorkflowFailed:
		return model.RunFailed, true
	default:
		return "", false
	}
}

func taskCallToToolCall(call TaskCall) model.ToolCall {
	startedAt := call.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	endedAt := call.EndedAt
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}
	args := jsonToMap(call.Input)
	if args == nil {
		args = map[string]any{}
	}
	args["forge_task_id"] = call.TaskID
	args["forge_task_name"] = call.TaskName
	args["forge_workflow_id"] = call.WorkflowID
	args["forge_worker_id"] = call.WorkerID

	toolCall := model.ToolCall{
		ID:        fmt.Sprintf("forge_tool_%s", call.TaskID),
		RunID:     RunIDForWorkflow(call.WorkflowID),
		ToolName:  call.Handler,
		Args:      args,
		StartedAt: startedAt.UTC(),
		EndedAt:   endedAt.UTC(),
	}
	if call.Success {
		toolCall.Result = jsonToMap(call.Output)
	} else {
		toolCall.Error = call.Error
		if toolCall.Error == "" {
			toolCall.Error = "worker task failed"
		}
	}
	return toolCall
}

func jsonToMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err == nil {
		return decoded
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return map[string]any{"raw": string(raw)}
	}
	return map[string]any{"value": value}
}

func payloadToAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return string(raw)
	}
	return decoded
}
