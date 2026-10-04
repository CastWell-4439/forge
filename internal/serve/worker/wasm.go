package worker

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/castwell/forge/internal/wasm"
	"github.com/castwell/forge/internal/worker"
	wasmworker "github.com/castwell/forge/internal/workers/wasm"
)

// Environment variables that configure the Wasm plugin worker.
//
//	FORGE_WASM_PLUGINS_DIR  directory scanned recursively for *.wasm (default plugins)
//	FORGE_WASM_MEMORY_MB    per-plugin memory limit in MB (default 64)
const (
	envWasmPluginsDir = "FORGE_WASM_PLUGINS_DIR"
	envWasmMemoryMB   = "FORGE_WASM_MEMORY_MB"
	defaultWasmDir    = "plugins"
	defaultWasmMemory = 64
)

// registerWasm registers the wasm workflow worker and loads every *.wasm
// under the plugins directory into the registry (module validation +
// SHA-256 identity happen inside Register).
//
// A missing directory is normal (deployments without plugins) and only
// logs: the worker still registers, and its error messages name the empty
// plugin list. A broken .wasm file is skipped with a warning — a stray
// invalid file must not take the whole worker binary down.
func registerWasm(r *worker.Registry) {
	dir := envOrDefault(envWasmPluginsDir, defaultWasmDir)
	registry, loaded, problems := loadWasmPlugins(dir)
	for _, problem := range problems {
		log.Printf("WARN: wasm plugin skipped: %v", problem)
	}
	if loaded == 0 {
		log.Printf("INFO: no wasm plugins loaded (dir=%s); the wasm worker reports an empty plugin list", dir)
	} else {
		log.Printf("INFO: wasm plugins loaded: %d from %s", loaded, dir)
	}
	r.Register("wasm", adaptWorkflowWorker("wasm", wasmworker.NewWorker(registry, wasmMemoryLimit())))
}

// loadWasmPlugins walks dir for *.wasm files and registers each under its
// file name. The version is the content hash prefix, so two different files
// with the same name become two versions instead of a collision, and the
// same content appearing twice (e.g. via symlinks) is skipped as a
// duplicate.
func loadWasmPlugins(dir string) (registry *wasm.Registry, loaded int, problems []error) {
	registry = wasm.NewRegistry()

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".wasm") {
			return nil
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Errorf("read %s: %w", path, err))
			return nil
		}
		name := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		version := fmt.Sprintf("%x", sha256.Sum256(bytes))[:12]
		if err := registry.Register(name, version, "", bytes); err != nil {
			// The same content under the same name (same file reached twice)
			// is already loaded; anything else is a real problem.
			if strings.Contains(err.Error(), "version already exists") {
				return nil
			}
			problems = append(problems, fmt.Errorf("%s: %w", path, err))
			return nil
		}
		loaded++
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return registry, 0, nil
		}
		problems = append(problems, fmt.Errorf("scan %s: %w", dir, err))
	}
	return registry, loaded, problems
}

// wasmMemoryLimit reads the per-plugin memory cap with a sane default.
func wasmMemoryLimit() uint32 {
	raw := strings.TrimSpace(os.Getenv(envWasmMemoryMB))
	if raw == "" {
		return defaultWasmMemory
	}
	mb, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || mb == 0 {
		log.Printf("WARN: invalid %s %q, using %d", envWasmMemoryMB, raw, defaultWasmMemory)
		return defaultWasmMemory
	}
	return uint32(mb)
}
