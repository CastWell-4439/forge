package harness

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// newRouterFor builds a router with a single registered tool.
func newRouterFor(t *testing.T, def *core.ToolDef, handler core.HandlerFunc) *ToolRouter {
	t.Helper()
	registry := core.NewToolRegistry()
	if err := registry.Register(def, handler); err != nil {
		t.Fatalf("register tool %q: %v", def.Name, err)
	}
	return NewToolRouter(registry)
}

// A panicking tool used to take the whole process down with it.
func TestToolRouterRecoversFromPanic(t *testing.T) {
	router := newRouterFor(t, &core.ToolDef{Name: "boom"}, func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		panic("handler exploded")
	})

	res := router.Call(context.Background(), "boom", nil)

	if res == nil {
		t.Fatal("expected a result, got nil")
	}
	if res.Error == "" {
		t.Fatal("expected the panic to surface as an error result")
	}
	if !strings.Contains(res.Error, "panicked") {
		t.Fatalf("error %q should mention the panic", res.Error)
	}
	if !strings.Contains(res.Error, "handler exploded") {
		t.Fatalf("error %q should carry the panic value", res.Error)
	}
}

// blockingHandler waits for its context, so it can observe any deadline.
func blockingHandler(d time.Duration) core.HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		select {
		case <-time.After(d):
			return map[string]interface{}{"ok": true}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Tool dispatch had no deadline at all, so one hung handler hung the run.
func TestToolRouterAppliesDefaultTimeout(t *testing.T) {
	router := newRouterFor(t, &core.ToolDef{Name: "slow"}, blockingHandler(10*time.Second))
	router.WithToolTimeout(30 * time.Millisecond)

	start := time.Now()
	res := router.Call(context.Background(), "slow", nil)
	elapsed := time.Since(start)

	if res.Error == "" {
		t.Fatal("expected the deadline to abort the call")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("call took %v; the deadline did not apply", elapsed)
	}
}

// A tool may declare its own deadline on the contract.
func TestToolRouterHonoursToolDeclaredTimeout(t *testing.T) {
	router := newRouterFor(t, &core.ToolDef{Name: "slow", Timeout: 30 * time.Millisecond}, blockingHandler(10*time.Second))

	res := router.Call(context.Background(), "slow", nil)

	if res.Error == "" {
		t.Fatal("expected the tool-declared deadline to abort the call")
	}
}

// The tool's declared deadline must win over the router default, otherwise a tool
// that legitimately needs longer than the default would be cut off.
func TestToolDeclaredTimeoutOverridesRouterDefault(t *testing.T) {
	router := newRouterFor(t, &core.ToolDef{Name: "medium", Timeout: 5 * time.Second}, blockingHandler(15*time.Millisecond))
	router.WithToolTimeout(time.Millisecond) // deliberately far too small

	res := router.Call(context.Background(), "medium", nil)

	if res.Error != "" {
		t.Fatalf("tool-declared timeout should win; got error %q", res.Error)
	}
	if res.Output == "" {
		t.Fatal("expected output from the completed call")
	}
}

// WithToolTimeout(0) disables the cap, preserving the pre-existing behaviour for
// callers that manage their own deadlines.
func TestToolRouterTimeoutCanBeDisabled(t *testing.T) {
	router := newRouterFor(t, &core.ToolDef{Name: "medium"}, blockingHandler(15*time.Millisecond))
	router.WithToolTimeout(0)

	res := router.Call(context.Background(), "medium", nil)

	if res.Error != "" {
		t.Fatalf("expected success with the cap disabled; got %q", res.Error)
	}
}
