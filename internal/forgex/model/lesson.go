package model

import "time"

// Lesson captures a durable learning extracted from a run or bad case.
type Lesson struct {
	ID          string            `json:"id" yaml:"id"`
	Title       string            `json:"title" yaml:"title"`
	SourceRunID string            `json:"source_run_id,omitempty" yaml:"source_run_id,omitempty"`
	Category    string            `json:"category" yaml:"category"`
	Content     string            `json:"content" yaml:"content"`
	Metadata    map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at" yaml:"created_at"`
}
