package planning

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/structured"
)

// RequirementParser turns a user's text into a Requirement.
//
// What to extract is the profile's business: the parser sends the profile's
// instruction and decodes the result into the domain-neutral shape. That is why
// this type has no knowledge of any product — the previous version's prompt
// described video production, which meant the engine could only ever serve one.
type RequirementParser struct {
	llmClient core.LLMClient
	profile   DomainProfile
}

// NewRequirementParser creates a parser for a domain. A nil profile means the
// generic one, so a caller with no domain still gets a working parser rather
// than a nil check.
func NewRequirementParser(llm core.LLMClient, profile DomainProfile) *RequirementParser {
	if profile == nil {
		profile = GenericProfile{}
	}
	return &RequirementParser{llmClient: llm, profile: profile}
}

// Parse sends the user's text to the LLM and returns a structured Requirement.
//
// The description is never left empty: if the model returns nothing for it, the
// user's own words stand in. A requirement is what the user asked for, and a
// parser that loses the question has failed even if it produced valid JSON.
func (p *RequirementParser) Parse(ctx context.Context, userText string) (*Requirement, error) {
	messages := []core.Message{
		{Role: "system", Content: p.profile.ParseSystemPrompt()},
		{Role: "user", Content: userText},
	}

	raw, err := p.llmClient.Chat(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("parse requirement: LLM call failed: %w", err)
	}

	// Extract JSON from the response (handles markdown fences, string escapes, etc.).
	jsonStr := structured.ExtractJSONObject(raw)

	var req Requirement
	if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
		return nil, fmt.Errorf("parse requirement: invalid JSON from LLM: %w", err)
	}

	if req.Description == "" {
		req.Description = userText
	}
	if req.Fields == nil {
		req.Fields = map[string]any{}
	}

	return &req, nil
}

// ProfileName reports which domain this parser is working for, for logs. It is
// on the parser rather than the caller because the parser is what holds the
// profile, and a log line that has to reach into a field would be worse.
func (p *RequirementParser) ProfileName() string {
	return p.profile.Name()
}
