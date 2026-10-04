package worker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/worker"
)

const echoFixture = "../../../internal/wasm/testdata/echo.wasm"

func copyEchoPlugin(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(echoFixture)
	require.NoError(t, err)
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

// Directory discovery: *.wasm files land in the registry under their file
// names, content-hashed as versions.
func TestLoadWasmPluginsFromDir(t *testing.T) {
	dir := t.TempDir()
	copyEchoPlugin(t, dir, "echo.wasm")

	registry, loaded, problems := loadWasmPlugins(dir)
	require.Empty(t, problems)
	require.Equal(t, 1, loaded)

	plugins := registry.List()
	require.Len(t, plugins, 1)
	assert.Equal(t, "echo", plugins[0].Name)

	module, err := registry.Get("echo")
	require.NoError(t, err)
	assert.NotEmpty(t, module, "the registered bytes are retrievable for execution")
}

// A missing directory is normal (deployments without plugins): zero loaded,
// no noise — the worker still registers and reports an empty list.
func TestLoadWasmPluginsMissingDirIsQuiet(t *testing.T) {
	registry, loaded, problems := loadWasmPlugins(filepath.Join(t.TempDir(), "nope"))
	require.Empty(t, problems)
	assert.Equal(t, 0, loaded)
	assert.NotNil(t, registry)
}

// A stray invalid .wasm is skipped with a recorded problem instead of
// taking the worker binary down.
func TestLoadWasmPluginsSkipsInvalidModule(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.wasm"), []byte("not a wasm module"), 0o644))

	registry, loaded, problems := loadWasmPlugins(dir)
	assert.Equal(t, 0, loaded)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "broken.wasm")
	assert.NotNil(t, registry)
}

// The same file name with identical content reached twice (two directories)
// collides on name+version — that is a duplicate to tolerate, not a problem.
// Different file names are different plugins and both register.
func TestLoadWasmPluginsToleratesDuplicateContent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	copyEchoPlugin(t, dir, "echo.wasm")
	copyEchoPlugin(t, dir, filepath.Join("sub", "echo.wasm"))
	copyEchoPlugin(t, dir, "echo_copy.wasm") // different name → separate plugin

	registry, loaded, problems := loadWasmPlugins(dir)
	assert.Equal(t, 2, loaded, "same name+version registers once; a different file name is its own plugin")
	require.Empty(t, problems)
	assert.Len(t, registry.List(), 2)
}

// The ninth workflow worker is on the dispatch chain.
func TestRegisterWasmRegistersTheHandler(t *testing.T) {
	dir := t.TempDir()
	copyEchoPlugin(t, dir, "echo.wasm")
	t.Setenv(envWasmPluginsDir, dir)

	r := worker.NewRegistry()
	registerWasm(r)
	require.NotNil(t, r.Get("wasm"), "the wasm worker must be registered")
}

// The memory cap reads env with a sane default and rejects nonsense.
func TestWasmMemoryLimit(t *testing.T) {
	t.Setenv(envWasmMemoryMB, "")
	assert.Equal(t, uint32(64), wasmMemoryLimit())

	t.Setenv(envWasmMemoryMB, "128")
	assert.Equal(t, uint32(128), wasmMemoryLimit())

	t.Setenv(envWasmMemoryMB, "banana")
	assert.Equal(t, uint32(64), wasmMemoryLimit(), "invalid values fall back to the default")
}
