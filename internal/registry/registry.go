package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Registry manages workflow definitions loaded from YAML files.
// Thread-safe for concurrent access via RWMutex.
type Registry struct {
	mu        sync.RWMutex
	workflows map[string]*CompiledWorkflow
	dir       string
}

// NewRegistry creates a new empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		workflows: make(map[string]*CompiledWorkflow),
	}
}

// Load reads all .yaml/.yml files from dir and compiles them into the registry.
// Existing entries are replaced. Invalid files are collected as errors but do not
// prevent valid files from loading.
func (r *Registry) Load(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("registry: read dir %q: %w", dir, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.dir = dir
	var errs []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isYAMLFile(name) {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		cw, err := Compile(data)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		r.workflows[cw.Name] = cw
	}

	if len(errs) > 0 {
		return fmt.Errorf("registry: %d file(s) failed to load:\n  %s", len(errs), strings.Join(errs, "\n  "))
	}
	return nil
}

// Get retrieves a compiled workflow by name. Returns an error if not found.
func (r *Registry) Get(name string) (*CompiledWorkflow, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	wf, ok := r.workflows[name]
	if !ok {
		return nil, fmt.Errorf("registry: workflow %q not found", name)
	}
	return wf, nil
}

// List returns the names of all loaded workflows.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.workflows))
	for name := range r.workflows {
		names = append(names, name)
	}
	return names
}

// Count returns the number of loaded workflows.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.workflows)
}

// Reload re-reads a single file and updates/adds the workflow.
// Used by the file watcher on change events.
func (r *Registry) Reload(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("registry: reload %q: %w", path, err)
	}
	cw, err := Compile(data)
	if err != nil {
		return fmt.Errorf("registry: reload %q: %w", path, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.workflows[cw.Name] = cw
	return nil
}

// Remove deletes a workflow entry by file name (derives workflow name from cache).
// Used by the file watcher on delete events.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.workflows, name)
}

// Dir returns the directory the registry was loaded from.
func (r *Registry) Dir() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.dir
}

func isYAMLFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yaml" || ext == ".yml"
}
