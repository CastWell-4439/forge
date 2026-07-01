// Package judge defines an optional, pluggable evaluation-judge interface.
//
// The package intentionally contains only local no-op/mock implementations. It
// does not call external LLMs and is not part of the default deterministic eval
// gate.
package judge
