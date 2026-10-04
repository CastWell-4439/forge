package coordinator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestCoordinator (discovery_test.go) provides the Coordinator with a
// temp Bolt store; these tests only need an instance to gate setup on.

// Disabled (the default) changes nothing: no k8s client is built, no probe
// runs, the coordinator stays exactly what it was.
func TestSetupKueueDisabledIsZeroChange(t *testing.T) {
	t.Setenv(envKueueEnabled, "")
	coord := newTestCoordinator(t)

	require.NoError(t, setupKueue(coord))
	assert.Nil(t, coord.Kueue(), "no manager must be installed when disabled")
}

// Enabled but with no cluster reachable fails startup with the config
// problem spelled out — same contract as a configured etcd that cannot be
// reached, instead of a silent mock queue.
func TestSetupKueueEnabledWithoutClusterFailsLoud(t *testing.T) {
	t.Setenv(envKueueEnabled, "1")
	t.Setenv(envKubeconfig, "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	coord := newTestCoordinator(t)

	err := setupKueue(coord)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kueue")
	assert.Nil(t, coord.Kueue(), "a failed setup must not install a half-manager")
}

// An invalid kubeconfig path must also fail before anything is installed —
// the success path needs a live cluster and stays CI-gated.
func TestSetupKueueInvalidKubeconfigFailsBeforeInstall(t *testing.T) {
	t.Setenv(envKueueEnabled, "true")
	t.Setenv(envKubeconfig, t.TempDir()+"/does-not-exist")
	coord := newTestCoordinator(t)

	require.Error(t, setupKueue(coord))
	assert.Nil(t, coord.Kueue())
}
