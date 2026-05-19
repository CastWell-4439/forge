package registry

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher monitors a directory for YAML file changes and hot-reloads the registry.
type Watcher struct {
	registry *Registry
	watcher  *fsnotify.Watcher
	dir      string
	debounce time.Duration
	logger   *slog.Logger
}

// WatcherOption configures the Watcher.
type WatcherOption func(*Watcher)

// WithDebounce sets the debounce interval for file change events.
func WithDebounce(d time.Duration) WatcherOption {
	return func(w *Watcher) { w.debounce = d }
}

// WithLogger sets the logger for the watcher.
func WithLogger(l *slog.Logger) WatcherOption {
	return func(w *Watcher) { w.logger = l }
}

// NewWatcher creates a file watcher for the given directory and registry.
func NewWatcher(reg *Registry, dir string, opts ...WatcherOption) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		registry: reg,
		watcher:  fw,
		dir:      dir,
		debounce: 500 * time.Millisecond,
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w, nil
}

// Watch starts watching for file changes. Blocks until ctx is cancelled.
func (w *Watcher) Watch(ctx context.Context) error {
	if err := w.watcher.Add(w.dir); err != nil {
		return err
	}
	defer w.watcher.Close()

	// Debounce: track pending reloads
	pending := make(map[string]time.Time)
	ticker := time.NewTicker(w.debounce)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case event, ok := <-w.watcher.Events:
			if !ok {
				return nil
			}
			name := filepath.Base(event.Name)
			if !isYAMLFile(name) {
				continue
			}
			switch {
			case event.Has(fsnotify.Create) || event.Has(fsnotify.Write):
				pending[event.Name] = time.Now()
			case event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename):
				// On delete/rename, remove from registry
				// We need to figure out which workflow was in that file.
				// For simplicity: reload all on delete.
				pending[event.Name] = time.Time{} // sentinel: zero time means delete
			}

		case <-ticker.C:
			for path, ts := range pending {
				if ts.IsZero() {
					// Delete event — full reload is simplest for correctness
					w.logger.Info("workflow file removed, reloading registry", "path", path)
					if err := w.registry.Load(w.dir); err != nil {
						w.logger.Error("registry reload failed", "error", err)
					}
					delete(pending, path)
					continue
				}
				if time.Since(ts) >= w.debounce {
					w.logger.Info("workflow file changed, reloading", "path", path)
					if err := w.registry.Reload(path); err != nil {
						w.logger.Error("workflow reload failed", "path", path, "error", err)
					}
					delete(pending, path)
				}
			}

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return nil
			}
			w.logger.Error("fsnotify error", "error", err)
		}
	}
}
