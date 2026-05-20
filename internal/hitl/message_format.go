package hitl

import (
	"fmt"
	"strings"
	"time"
)

// MessageFormatter formats HITL requests for display in chat (Feishu/OpenClaw).
type MessageFormatter struct{}

// NewMessageFormatter creates a new formatter.
func NewMessageFormatter() *MessageFormatter {
	return &MessageFormatter{}
}

// FormatRequest formats a HITL request into a human-readable message.
func (f *MessageFormatter) FormatRequest(req *Request) string {
	var sb strings.Builder

	sb.WriteString("🔔 **Forge HITL 审批请求**\n\n")
	sb.WriteString(fmt.Sprintf("**请求 ID**: `%s`\n", req.ID))
	sb.WriteString(fmt.Sprintf("**工作流**: `%s`\n", req.WorkflowID))
	if req.TaskID != "" {
		sb.WriteString(fmt.Sprintf("**任务**: `%s`\n", req.TaskID))
	}
	sb.WriteString("\n---\n\n")
	sb.WriteString(fmt.Sprintf("**消息**: %s\n", req.Message))

	if len(req.Options) > 0 {
		sb.WriteString("\n**可选操作**:\n")
		for i, opt := range req.Options {
			sb.WriteString(fmt.Sprintf("  %d. `%s`\n", i+1, opt))
		}
	}

	sb.WriteString("\n---\n")
	sb.WriteString(fmt.Sprintf("⏰ 超时: %s\n", f.formatTimeout(req.TimeoutAt)))
	sb.WriteString(fmt.Sprintf("\n💡 回复格式: `forge respond %s <decision> [feedback]`", req.ID))

	return sb.String()
}

// FormatNotification formats a one-way notification (no response needed).
func (f *MessageFormatter) FormatNotification(req *Request) string {
	var sb strings.Builder

	sb.WriteString("ℹ️ **Forge 通知**\n\n")
	sb.WriteString(fmt.Sprintf("**工作流**: `%s`\n", req.WorkflowID))
	if req.TaskID != "" {
		sb.WriteString(fmt.Sprintf("**任务**: `%s`\n", req.TaskID))
	}
	sb.WriteString("\n")
	sb.WriteString(req.Message)

	return sb.String()
}

// FormatTimeout formats a timeout notification.
func (f *MessageFormatter) FormatTimeout(req *Request) string {
	return fmt.Sprintf("⚠️ **HITL 请求超时**: `%s`\n工作流 `%s` 的审批请求已超时，将按默认策略处理。",
		req.ID, req.WorkflowID)
}

// FormatResponseConfirmation formats a confirmation after user responds.
func (f *MessageFormatter) FormatResponseConfirmation(req *Request, resp *Response) string {
	var sb strings.Builder

	sb.WriteString("✅ **审批已处理**\n\n")
	sb.WriteString(fmt.Sprintf("**请求**: `%s`\n", req.ID))
	sb.WriteString(fmt.Sprintf("**决定**: `%s`\n", resp.Decision))
	if resp.Feedback != "" {
		sb.WriteString(fmt.Sprintf("**反馈**: %s\n", resp.Feedback))
	}

	return sb.String()
}

// formatTimeout formats the timeout time.
func (f *MessageFormatter) formatTimeout(t time.Time) string {
	if t.IsZero() {
		return "无限期"
	}
	remaining := time.Until(t)
	if remaining <= 0 {
		return "已超时"
	}
	if remaining > 24*time.Hour {
		return fmt.Sprintf("%.0f 天后 (%s)", remaining.Hours()/24, t.Format("01-02 15:04"))
	}
	if remaining > time.Hour {
		return fmt.Sprintf("%.0f 小时后 (%s)", remaining.Hours(), t.Format("15:04"))
	}
	return fmt.Sprintf("%.0f 分钟后", remaining.Minutes())
}
