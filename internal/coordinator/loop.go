package coordinator

import "fmt"

const (
	// DefaultMaxIterations prevents infinite loops.
	DefaultMaxIterations = 10
	// AbsoluteMaxIterations is the hard cap regardless of config.
	AbsoluteMaxIterations = 100
)

// LoopConfig defines loop/retry behavior for a task group.
type LoopConfig struct {
	MaxIterations int    `yaml:"max_iterations"` // 0 = use default
	BreakOn       string `yaml:"break_on"`       // CEL expression to break loop
}

// LoopState tracks the current iteration state during execution.
type LoopState struct {
	Iterations map[string]int // task_name -> current iteration count
}

// NewLoopState creates a fresh loop state.
func NewLoopState() *LoopState {
	return &LoopState{
		Iterations: make(map[string]int),
	}
}

// CanIterate checks if the task can perform another iteration.
// Returns (allowed, currentCount, error).
func (ls *LoopState) CanIterate(taskName string, maxIter int) (bool, int, error) {
	if maxIter <= 0 {
		maxIter = DefaultMaxIterations
	}
	if maxIter > AbsoluteMaxIterations {
		maxIter = AbsoluteMaxIterations
	}

	current := ls.Iterations[taskName]
	if current >= maxIter {
		return false, current, fmt.Errorf("loop: task %q reached max iterations (%d)", taskName, maxIter)
	}
	return true, current, nil
}

// Increment records one iteration for the task.
func (ls *LoopState) Increment(taskName string) int {
	ls.Iterations[taskName]++
	return ls.Iterations[taskName]
}

// Reset clears the iteration count for a task.
func (ls *LoopState) Reset(taskName string) {
	delete(ls.Iterations, taskName)
}

// ResolveGoto determines if a goto should execute based on loop limits and break condition.
// Returns (shouldGoto, error).
func (ls *LoopState) ResolveGoto(targetTask string, maxIter int, breakExpr string, cel *CELEvaluator, ctx map[string]any) (bool, error) {
	canLoop, count, err := ls.CanIterate(targetTask, maxIter)
	if err != nil {
		return false, err // max iterations reached
	}
	if !canLoop {
		return false, nil
	}

	// Check break condition
	if breakExpr != "" && cel != nil {
		// Add iteration to context
		ctx["iteration"] = int64(count)
		shouldBreak, err := cel.Eval(breakExpr, ctx)
		if err != nil {
			return false, fmt.Errorf("loop: eval break condition: %w", err)
		}
		if shouldBreak {
			return false, nil // break condition met, stop looping
		}
	}

	ls.Increment(targetTask)
	return true, nil
}
