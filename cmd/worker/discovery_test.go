package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/discovery"
)

// Without FORGE_ETCD_ENDPOINTS the worker keeps its original behaviour:
// direct registration only, nothing to discover, nothing to close.
func TestEtcdDiscoveryFromEnvUnsetReturnsNil(t *testing.T) {
	t.Setenv(envEtcdEndpoints, "")

	d, err := etcdDiscoveryFromEnv("worker-1")
	require.NoError(t, err)
	assert.Nil(t, d, "unset endpoints must keep direct-registration mode")
}

// A comma-separated list parses into endpoints; the client is lazy, so no
// connection is attempted at construction.
func TestEtcdDiscoveryFromEnvParsesEndpointList(t *testing.T) {
	t.Setenv(envEtcdEndpoints, "http://127.0.0.1:1, http://127.0.0.1:2")

	d, err := etcdDiscoveryFromEnv("worker-1")
	require.NoError(t, err)
	require.NotNil(t, d)

	etcd, ok := d.(*discovery.EtcdDiscovery)
	require.True(t, ok, "expected an EtcdDiscovery, got %T", d)
	require.NoError(t, etcd.Close())
}

// Set-but-empty is a configuration mistake and must say so: silently
// falling back to direct registration would make the worker invisible to a
// distributed coordinator.
func TestEtcdDiscoveryFromEnvRejectsEmptyValue(t *testing.T) {
	t.Setenv(envEtcdEndpoints, "  ,  ")

	_, err := etcdDiscoveryFromEnv("worker-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), envEtcdEndpoints)
}
