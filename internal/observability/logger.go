// Package observability — Logger abstraction with log/slog and standard log backends.
package observability

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
)

// Logger is the unified logging interface for Forge.
// Both slog and standard log can implement it.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	Debug(msg string, args ...any)
	With(args ...any) Logger

	// InfoContext / WarnContext / ErrorContext carry request-scoped values.
	InfoContext(ctx context.Context, msg string, args ...any)
	WarnContext(ctx context.Context, msg string, args ...any)
	ErrorContext(ctx context.Context, msg string, args ...any)
}

// --- slog backend ---

// SlogLogger wraps log/slog.Logger as a Forge Logger.
type SlogLogger struct {
	inner *slog.Logger
}

// SlogOption configures SlogLogger creation.
type SlogOption func(*slogConfig)

type slogConfig struct {
	level   slog.Level
	format  string // "json" or "text"
	writer  io.Writer
	replace func(groups []string, a slog.Attr) slog.Attr
}

// WithLevel sets the minimum log level.
func WithLevel(level slog.Level) SlogOption {
	return func(c *slogConfig) { c.level = level }
}

// WithFormat sets the output format ("json" or "text").
func WithFormat(format string) SlogOption {
	return func(c *slogConfig) { c.format = format }
}

// WithWriter sets the output writer (default: os.Stdout).
func WithWriter(w io.Writer) SlogOption {
	return func(c *slogConfig) { c.writer = w }
}

// WithReplaceAttr sets a custom attribute replacer.
func WithReplaceAttr(fn func(groups []string, a slog.Attr) slog.Attr) SlogOption {
	return func(c *slogConfig) { c.replace = fn }
}

// NewSlogLogger creates a Logger backed by log/slog.
func NewSlogLogger(opts ...SlogOption) Logger {
	cfg := &slogConfig{
		level:  slog.LevelInfo,
		format: "json",
		writer: os.Stdout,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	handlerOpts := &slog.HandlerOptions{
		Level:       cfg.level,
		ReplaceAttr: cfg.replace,
	}

	var handler slog.Handler
	switch cfg.format {
	case "text":
		handler = slog.NewTextHandler(cfg.writer, handlerOpts)
	default:
		handler = slog.NewJSONHandler(cfg.writer, handlerOpts)
	}

	return &SlogLogger{inner: slog.New(handler)}
}

func (l *SlogLogger) Info(msg string, args ...any)  { l.inner.Info(msg, args...) }
func (l *SlogLogger) Warn(msg string, args ...any)  { l.inner.Warn(msg, args...) }
func (l *SlogLogger) Error(msg string, args ...any) { l.inner.Error(msg, args...) }
func (l *SlogLogger) Debug(msg string, args ...any) { l.inner.Debug(msg, args...) }

func (l *SlogLogger) InfoContext(ctx context.Context, msg string, args ...any) {
	l.inner.InfoContext(ctx, msg, args...)
}
func (l *SlogLogger) WarnContext(ctx context.Context, msg string, args ...any) {
	l.inner.WarnContext(ctx, msg, args...)
}
func (l *SlogLogger) ErrorContext(ctx context.Context, msg string, args ...any) {
	l.inner.ErrorContext(ctx, msg, args...)
}

func (l *SlogLogger) With(args ...any) Logger {
	return &SlogLogger{inner: l.inner.With(args...)}
}

// --- Standard log backend ---

// StdLogger wraps the standard log package as a Forge Logger.
// It provides a fallback for environments where slog is not desired.
type StdLogger struct {
	inner  *log.Logger
	prefix string
}

// NewStdLogger creates a Logger backed by the standard log package.
func NewStdLogger() Logger {
	return &StdLogger{
		inner: log.New(os.Stderr, "", log.LstdFlags),
	}
}

func (l *StdLogger) Info(msg string, args ...any) {
	l.inner.Printf("INFO: %s %v", msg, formatArgs(args))
}
func (l *StdLogger) Warn(msg string, args ...any) {
	l.inner.Printf("WARN: %s %v", msg, formatArgs(args))
}
func (l *StdLogger) Error(msg string, args ...any) {
	l.inner.Printf("ERROR: %s %v", msg, formatArgs(args))
}
func (l *StdLogger) Debug(msg string, args ...any) {
	l.inner.Printf("DEBUG: %s %v", msg, formatArgs(args))
}

func (l *StdLogger) InfoContext(_ context.Context, msg string, args ...any) {
	l.Info(msg, args...)
}
func (l *StdLogger) WarnContext(_ context.Context, msg string, args ...any) {
	l.Warn(msg, args...)
}
func (l *StdLogger) ErrorContext(_ context.Context, msg string, args ...any) {
	l.Error(msg, args...)
}

func (l *StdLogger) With(args ...any) Logger {
	prefix := l.prefix + formatArgs(args) + " "
	return &StdLogger{
		inner:  l.inner,
		prefix: prefix,
	}
}

// formatArgs converts slog-style key-value pairs to a simple string for StdLogger.
func formatArgs(args []any) string {
	if len(args) == 0 {
		return ""
	}
	result := ""
	for i := 0; i < len(args)-1; i += 2 {
		if result != "" {
			result += " "
		}
		result += fmt.Sprintf("%v=%v", args[i], args[i+1])
	}
	// Handle odd number of args (trailing key without value).
	if len(args)%2 != 0 {
		if result != "" {
			result += " "
		}
		result += fmt.Sprintf("%v=?", args[len(args)-1])
	}
	return result
}

// Nop returns a Logger that discards all output. Useful for tests.
func Nop() Logger {
	return &nopLogger{}
}

type nopLogger struct{}

func (l *nopLogger) Info(_ string, _ ...any)                            {}
func (l *nopLogger) Warn(_ string, _ ...any)                            {}
func (l *nopLogger) Error(_ string, _ ...any)                           {}
func (l *nopLogger) Debug(_ string, _ ...any)                           {}
func (l *nopLogger) InfoContext(_ context.Context, _ string, _ ...any)  {}
func (l *nopLogger) WarnContext(_ context.Context, _ string, _ ...any)  {}
func (l *nopLogger) ErrorContext(_ context.Context, _ string, _ ...any) {}
func (l *nopLogger) With(_ ...any) Logger                               { return l }
