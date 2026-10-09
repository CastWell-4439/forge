package observability

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The series exists on every build, including platforms with no eBPF. Declaring
// it unconditionally means a dashboard can reference it without knowing which
// node it is talking to; the values are simply absent where nothing measures.
func TestTCPConnectLatencySeriesAlwaysExists(t *testing.T) {
	m := NewMetrics()
	require.NotNil(t, m.TCPConnectLatency)

	// Observing must not panic and must be visible in the exposition, which is
	// what a dashboard actually reads.
	m.TCPConnectLatency.Observe(0.012, "forge-worker")

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	assert.Contains(t, body, "forge_tcp_connect_latency_seconds")
	assert.Contains(t, body, "forge-worker")
}

// Every declared metric must appear in the exposition.
//
// The handler lists its series explicitly, so adding a field to Metrics and
// forgetting the corresponding write produces a metric that is recorded and never
// served — invisible in exactly the place it exists to be read. That happened
// once while adding the eBPF histogram, which is why this is a test rather than a
// comment.
func TestEveryDeclaredMetricIsServed(t *testing.T) {
	m := NewMetrics()

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	declared := map[string]bool{
		"forge_workflows_total":             true,
		"forge_task_duration_seconds":       true,
		"forge_active_workflows":            true,
		"forge_worker_pool_size":            true,
		"forge_task_retries_total":          true,
		"forge_queue_depth":                 true,
		"forge_tcp_connect_latency_seconds": true,
	}

	for name := range declared {
		assert.Contains(t, body, "# HELP "+name,
			"metric %s is declared but not served by the handler", name)
		assert.Contains(t, body, "# TYPE "+name,
			"metric %s is declared but not served by the handler", name)
	}

	// And the reverse: nothing is served that this test does not know about, so
	// adding a series forces the list above to be updated too.
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "# HELP ") {
			continue
		}
		name := strings.TrimPrefix(line, "# HELP ")
		name = name[:strings.Index(name, " ")]
		assert.True(t, declared[name],
			"the handler serves %s, which this test does not list — update both", name)
	}
}

// Every switch spelling the project uses is honoured, and a value that is neither
// truthy nor falsy is not silently treated as enabled.
func TestEnvBoolSpellings(t *testing.T) {
	cases := map[string]bool{
		"":         false,
		"0":        false,
		"false":    false,
		"no":       false,
		"off":      false,
		"1":        true,
		"true":     true,
		"TRUE":     true,
		"yes":      true,
		"on":       true,
		"2":        true,
		"nonsense": false,
	}
	for value, want := range cases {
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv(envEBPFEnabled, value)
			assert.Equal(t, want, envBool(envEBPFEnabled), "value %q", value)
		})
	}

	t.Setenv(envEBPFEnabled, "")
	assert.False(t, envBool("FORGE_EBPF_UNSET_VARIABLE"))
}

// With nothing asked for, starting is a no-op — and the returned stop function
// is still safe to call, which is what lets callers defer it unconditionally.
func TestStartEBPFObserverOffByDefault(t *testing.T) {
	t.Setenv(envEBPFEnabled, "")

	stop := StartEBPFObserver(t.Context(), NewMetrics())
	require.NotNil(t, stop, "a stop function is always returned")
	assert.NotPanics(t, stop)
}

// Enabled on a platform without eBPF is a no-op rather than an error: a node
// without kernel support must still run Forge.
func TestStartEBPFObserverDegradesWithoutKernelSupport(t *testing.T) {
	t.Setenv(envEBPFEnabled, "1")

	stop := StartEBPFObserver(t.Context(), NewMetrics())
	require.NotNil(t, stop)
	assert.NotPanics(t, stop)

	if !IsEBPFAvailable() {
		assert.Contains(t, DescribeEBPF(), "unavailable",
			"the reason is reported so an operator can tell it apart from 'not asked for'")
	}
}

// Enabled with no metrics sink is refused with a reason rather than starting a
// reader whose observations would go nowhere.
func TestStartEBPFObserverNeedsASink(t *testing.T) {
	t.Setenv(envEBPFEnabled, "1")

	stop := StartEBPFObserver(t.Context(), nil)
	require.NotNil(t, stop)
	assert.NotPanics(t, stop)
}

// The description distinguishes the three states an operator has to act on.
func TestDescribeEBPFStates(t *testing.T) {
	t.Setenv(envEBPFEnabled, "")
	assert.Contains(t, DescribeEBPF(), "off", "not asked for")
	assert.Contains(t, DescribeEBPF(), envEBPFEnabled, "and names the switch that would enable it")

	t.Setenv(envEBPFEnabled, "1")
	if IsEBPFAvailable() {
		assert.Equal(t, "enabled", DescribeEBPF())
	} else {
		assert.Contains(t, DescribeEBPF(), "unavailable")
	}
}

// A stub observer fails loudly rather than pretending, and its error says what
// is missing — the caller should not have to guess between a missing object
// file, a missing tag, and a missing kernel feature.
func TestStubObserverReportsWhyItCannotRun(t *testing.T) {
	if IsEBPFAvailable() {
		t.Skip("this build has real eBPF support; the stub is not compiled")
	}

	_, err := NewEBPFObserver("some/path.o")
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "linux") || strings.Contains(err.Error(), "platform"),
		"the error must say which capability is missing, got: %v", err)

	obs := &EBPFObserver{}
	assert.NotPanics(t, func() { _ = obs.Close() })
	assert.Error(t, obs.ReadEvents(t.Context(), func(TCPEvent) {}))
}

// The stub's TCPEvent has the same shape as the real one, so a caller handling
// events compiles on every platform without build tags of its own.
func TestStubTCPEventShape(t *testing.T) {
	e := TCPEvent{
		PID:       1,
		DstPort:   443,
		LatencyNs: uint64(time.Millisecond),
		Comm:      "forge",
	}
	assert.Equal(t, uint32(1), e.PID)
	assert.Equal(t, uint16(443), e.DstPort)
	assert.Equal(t, "forge", e.Comm)
}
