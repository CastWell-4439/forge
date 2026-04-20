package wasm

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"sync"
	"time"
)

// PluginVersion represents a specific version of a Wasm plugin.
type PluginVersion struct {
	Version   string
	Hash      string // SHA-256 of the Wasm bytes
	Size      int
	CreatedAt time.Time
	WasmBytes []byte
}

// Plugin represents a registered Wasm plugin with version history.
type Plugin struct {
	Name        string
	Description string
	Versions    []PluginVersion // sorted by CreatedAt desc
	Active      string          // active version string
}

// Registry manages Wasm plugin registration and version control.
type Registry struct {
	plugins map[string]*Plugin
	mu      sync.RWMutex
}

// NewRegistry creates a new Wasm plugin registry.
func NewRegistry() *Registry {
	return &Registry{
		plugins: make(map[string]*Plugin),
	}
}

// Register adds a new plugin version. If the plugin doesn't exist, it creates it.
func (r *Registry) Register(name, version, description string, wasmBytes []byte) error {
	if err := ValidateModule(wasmBytes); err != nil {
		return fmt.Errorf("register %s@%s: %w", name, version, err)
	}

	hash := sha256.Sum256(wasmBytes)
	hashStr := fmt.Sprintf("%x", hash)

	pv := PluginVersion{
		Version:   version,
		Hash:      hashStr,
		Size:      len(wasmBytes),
		CreatedAt: time.Now(),
		WasmBytes: wasmBytes,
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	plugin, ok := r.plugins[name]
	if !ok {
		plugin = &Plugin{
			Name:        name,
			Description: description,
		}
		r.plugins[name] = plugin
	}

	// Check for duplicate version.
	for _, v := range plugin.Versions {
		if v.Version == version {
			return fmt.Errorf("register %s@%s: version already exists", name, version)
		}
	}

	plugin.Versions = append(plugin.Versions, pv)
	// Sort versions by CreatedAt descending (newest first).
	sort.Slice(plugin.Versions, func(i, j int) bool {
		return plugin.Versions[i].CreatedAt.After(plugin.Versions[j].CreatedAt)
	})

	// Auto-activate the latest version.
	plugin.Active = version

	if description != "" {
		plugin.Description = description
	}

	return nil
}

// Get retrieves the active version of a plugin.
func (r *Registry) Get(name string) ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	plugin, ok := r.plugins[name]
	if !ok {
		return nil, fmt.Errorf("plugin %q not found", name)
	}

	for _, v := range plugin.Versions {
		if v.Version == plugin.Active {
			return v.WasmBytes, nil
		}
	}

	return nil, fmt.Errorf("plugin %q: active version %q not found", name, plugin.Active)
}

// GetVersion retrieves a specific version of a plugin.
func (r *Registry) GetVersion(name, version string) ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	plugin, ok := r.plugins[name]
	if !ok {
		return nil, fmt.Errorf("plugin %q not found", name)
	}

	for _, v := range plugin.Versions {
		if v.Version == version {
			return v.WasmBytes, nil
		}
	}

	return nil, fmt.Errorf("plugin %s@%s not found", name, version)
}

// SetActive sets the active version for a plugin.
func (r *Registry) SetActive(name, version string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	plugin, ok := r.plugins[name]
	if !ok {
		return fmt.Errorf("plugin %q not found", name)
	}

	for _, v := range plugin.Versions {
		if v.Version == version {
			plugin.Active = version
			return nil
		}
	}

	return fmt.Errorf("plugin %s@%s not found", name, version)
}

// List returns all registered plugins.
func (r *Registry) List() []*Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// Remove removes a plugin entirely.
func (r *Registry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.plugins[name]; !ok {
		return fmt.Errorf("plugin %q not found", name)
	}
	delete(r.plugins, name)
	return nil
}

// Count returns the number of registered plugins.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.plugins)
}
