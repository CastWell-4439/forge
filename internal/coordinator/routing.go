package coordinator

import "fmt"

// RouteAction defines what to do after a task completes.
type RouteAction string

const (
	// RouteActionContinue proceeds to the next task in DAG order (default).
	RouteActionContinue RouteAction = "continue"
	// RouteActionGoto jumps to a specific task (for loops/retries).
	RouteActionGoto RouteAction = "goto"
	// RouteActionAbort immediately fails the workflow.
	RouteActionAbort RouteAction = "abort"
	// RouteActionSkip skips the current task's downstream (continue without children).
	RouteActionSkip RouteAction = "skip"
)

// ResultRoute maps a task result status to an action.
type ResultRoute struct {
	Action RouteAction `yaml:"action"`
	Target string      `yaml:"target"` // task name for "goto"
}

// OnResult defines routing rules for different task outcomes.
// Keys are result statuses: "success", "failure", "rejected", "timeout", etc.
type OnResult map[string]ResultRoute

// ParseOnResult parses the on_result field from YAML.
// Supports shorthand (string) and full form (struct).
//
// Shorthand: "continue", "abort", "goto:task_name"
// Full: { action: "goto", target: "retry_step" }
func ParseOnResult(raw map[string]any) (OnResult, error) {
	if raw == nil {
		return nil, nil
	}

	result := make(OnResult)
	for status, val := range raw {
		switch v := val.(type) {
		case string:
			route, err := parseShorthand(v)
			if err != nil {
				return nil, fmt.Errorf("on_result[%s]: %w", status, err)
			}
			result[status] = route
		case map[string]any:
			action, _ := v["action"].(string)
			target, _ := v["target"].(string)
			route := ResultRoute{
				Action: RouteAction(action),
				Target: target,
			}
			if err := validateRoute(route); err != nil {
				return nil, fmt.Errorf("on_result[%s]: %w", status, err)
			}
			result[status] = route
		default:
			return nil, fmt.Errorf("on_result[%s]: unsupported type %T", status, val)
		}
	}
	return result, nil
}

// Resolve determines the routing action for a given task result status.
// Returns RouteActionContinue if no specific route is defined.
func (r OnResult) Resolve(status string) ResultRoute {
	if r == nil {
		return ResultRoute{Action: RouteActionContinue}
	}
	if route, ok := r[status]; ok {
		return route
	}
	// Default: continue
	return ResultRoute{Action: RouteActionContinue}
}

// parseShorthand parses shorthand routing strings.
func parseShorthand(s string) (ResultRoute, error) {
	switch {
	case s == "continue":
		return ResultRoute{Action: RouteActionContinue}, nil
	case s == "abort":
		return ResultRoute{Action: RouteActionAbort}, nil
	case s == "skip":
		return ResultRoute{Action: RouteActionSkip}, nil
	case len(s) > 5 && s[:5] == "goto:":
		target := s[5:]
		if target == "" {
			return ResultRoute{}, fmt.Errorf("goto requires a target task name")
		}
		return ResultRoute{Action: RouteActionGoto, Target: target}, nil
	default:
		return ResultRoute{}, fmt.Errorf("unknown route action %q (use continue/abort/skip/goto:<task>)", s)
	}
}

// validateRoute checks a ResultRoute for validity.
func validateRoute(r ResultRoute) error {
	switch r.Action {
	case RouteActionContinue, RouteActionAbort, RouteActionSkip:
		return nil
	case RouteActionGoto:
		if r.Target == "" {
			return fmt.Errorf("goto action requires a target")
		}
		return nil
	default:
		return fmt.Errorf("unknown action %q", r.Action)
	}
}
