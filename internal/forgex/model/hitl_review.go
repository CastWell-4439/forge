package model

import "time"

// HITLReviewStatus is the local human-review lifecycle state.
type HITLReviewStatus string

const (
	HITLReviewPending   HITLReviewStatus = "pending"
	HITLReviewApproved  HITLReviewStatus = "approved"
	HITLReviewRejected  HITLReviewStatus = "rejected"
	HITLReviewContinued HITLReviewStatus = "continued"
)

// HITLReview records one local human-in-the-loop decision artifact.
type HITLReview struct {
	ID         string           `json:"id" yaml:"id"`
	RunID      string           `json:"run_id" yaml:"run_id"`
	GateID     string           `json:"gate_id,omitempty" yaml:"gate_id,omitempty"`
	Status     HITLReviewStatus `json:"status" yaml:"status"`
	Decision   string           `json:"decision,omitempty" yaml:"decision,omitempty"`
	Reviewer   string           `json:"reviewer,omitempty" yaml:"reviewer,omitempty"`
	Reason     string           `json:"reason,omitempty" yaml:"reason,omitempty"`
	CreatedAt  time.Time        `json:"created_at" yaml:"created_at"`
	ResolvedAt time.Time        `json:"resolved_at,omitempty" yaml:"resolved_at,omitempty"`
}
