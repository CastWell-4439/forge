// Package lessons derives durable Lesson records from a completed run snapshot.
//
// A lesson captures a reusable learning from a halting outcome: the run hit a
// classified error that drove a stop/escalate/pause decision. Derivation is
// deliberately conservative — a clean run that continued or succeeded yields no
// lessons, so a happy-path run never emits a misleading lesson or bad case.
//
// The package performs no I/O; callers persist the derived lessons via the
// storage layer (lessons.jsonl in the run directory) and may surface them in
// the run report.
package lessons
