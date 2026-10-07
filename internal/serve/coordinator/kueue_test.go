package coordinator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/coordinator"
)

// newTestCoordinator (discovery_test.go) provides the Coordinator with a
// temp Bolt store; these tests only need an instance to gate setup on.

// Disabled (the default) changes nothing: no k8s client is built, no probe
// runs, the coordinator stays exactly what it was.
func TestSetupKueueDisabledIsZeroChange(t *testing.T) {
	t.Setenv(envKueueEnabled, "")
	coord := newTestCoordinator(t)

	manager, err := setupKueue(coord)
	require.NoError(t, err)
	assert.Nil(t, manager, "no manager is returned when disabled")
	assert.Nil(t, coord.Kueue(), "and none is installed")
}

// Enabled but with no cluster reachable fails startup with the config
// problem spelled out — same contract as a configured etcd that cannot be
// reached, instead of a silent mock queue.
func TestSetupKueueEnabledWithoutClusterFailsLoud(t *testing.T) {
	t.Setenv(envKueueEnabled, "1")
	t.Setenv(envKubeconfig, "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	coord := newTestCoordinator(t)

	manager, err := setupKueue(coord)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kueue")
	assert.Nil(t, manager, "a failed setup returns no manager")
	assert.Nil(t, coord.Kueue(), "a failed setup must not install a half-manager")
}

// An invalid kubeconfig path must also fail before anything is installed —
// the success path needs a live cluster and stays CI-gated.
func TestSetupKueueInvalidKubeconfigFailsBeforeInstall(t *testing.T) {
	t.Setenv(envKueueEnabled, "true")
	t.Setenv(envKubeconfig, t.TempDir()+"/does-not-exist")
	coord := newTestCoordinator(t)

	manager, err := setupKueue(coord)
	require.Error(t, err)
	assert.Nil(t, manager)
	assert.Nil(t, coord.Kueue())
}

// FORGE_KUEUE_POLL_INTERVAL resolves with a safe fallback: a typo must not
// stop result write-back, so it warns and uses the default.
func TestDefaultKueuePollInterval(t *testing.T) {
	t.Setenv(envKueuePollInterval, "")
	assert.Equal(t, coordinator.DefaultKueuePollInterval(), defaultKueuePollInterval())

	t.Setenv(envKueuePollInterval, "2s")
	assert.Equal(t, 2*time.Second, defaultKueuePollInterval())

	for _, bad := range []string{"banana", "0s", "-5s"} {
		t.Setenv(envKueuePollInterval, bad)
		assert.Equal(t, coordinator.DefaultKueuePollInterval(), defaultKueuePollInterval(),
			"%q falls back to the default", bad)
	}
}
