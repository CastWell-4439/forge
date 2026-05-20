package coordinator

import (
	"fmt"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
)

// CELEvaluator evaluates CEL expressions against a context map.
// It caches compiled programs for performance.
type CELEvaluator struct {
	mu       sync.RWMutex
	env      *cel.Env
	programs map[string]cel.Program
}

// NewCELEvaluator creates a CEL evaluator with standard variables available
// in workflow context (task results, workflow metadata).
func NewCELEvaluator() (*CELEvaluator, error) {
	env, err := cel.NewEnv(
		cel.Variable("results", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("vars", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("iteration", cel.IntType),
		cel.Variable("workflow_id", cel.StringType),
	)
	if err != nil {
		return nil, fmt.Errorf("cel: create env: %w", err)
	}
	return &CELEvaluator{
		env:      env,
		programs: make(map[string]cel.Program),
	}, nil
}

// Eval evaluates a CEL expression and returns the result as a bool.
// Returns true if expression is empty (no condition = always execute).
func (e *CELEvaluator) Eval(expr string, ctx map[string]any) (bool, error) {
	if expr == "" {
		return true, nil
	}

	program, err := e.getOrCompile(expr)
	if err != nil {
		return false, err
	}

	// Ensure all expected variables exist in ctx
	evalCtx := map[string]any{
		"results":     map[string]any{},
		"vars":        map[string]any{},
		"iteration":   int64(0),
		"workflow_id": "",
	}
	for k, v := range ctx {
		evalCtx[k] = v
	}

	out, _, err := program.Eval(evalCtx)
	if err != nil {
		return false, fmt.Errorf("cel: eval %q: %w", expr, err)
	}

	// Coerce to bool
	if out.Type() == types.BoolType {
		return out.Value().(bool), nil
	}

	// Truthy: non-null, non-zero, non-empty
	return out.Value() != nil, nil
}

// EvalString evaluates a CEL expression and returns the result as a string.
func (e *CELEvaluator) EvalString(expr string, ctx map[string]any) (string, error) {
	if expr == "" {
		return "", nil
	}

	program, err := e.getOrCompile(expr)
	if err != nil {
		return "", err
	}

	evalCtx := map[string]any{
		"results":     map[string]any{},
		"vars":        map[string]any{},
		"iteration":   int64(0),
		"workflow_id": "",
	}
	for k, v := range ctx {
		evalCtx[k] = v
	}

	out, _, err := program.Eval(evalCtx)
	if err != nil {
		return "", fmt.Errorf("cel: eval %q: %w", expr, err)
	}

	if out.Type() == types.StringType {
		return out.Value().(string), nil
	}
	return fmt.Sprintf("%v", out.Value()), nil
}

// getOrCompile retrieves a cached program or compiles and caches a new one.
func (e *CELEvaluator) getOrCompile(expr string) (cel.Program, error) {
	e.mu.RLock()
	if prog, ok := e.programs[expr]; ok {
		e.mu.RUnlock()
		return prog, nil
	}
	e.mu.RUnlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	// Double-check after acquiring write lock
	if prog, ok := e.programs[expr]; ok {
		return prog, nil
	}

	ast, issues := e.env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("cel: compile %q: %w", expr, issues.Err())
	}

	prog, err := e.env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("cel: program %q: %w", expr, err)
	}

	e.programs[expr] = prog
	return prog, nil
}
