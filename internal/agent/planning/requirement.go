// Package planning turns a natural-language requirement into a Forge workflow
// DAG: parse the text into a structured requirement, plan the steps, generate
// and validate the YAML.
//
// It is domain-neutral. The engine knows a requirement has a description, some
// fields it does not interpret, and an acceptance criterion — but not what any
// particular field means. What to extract and how to shape it is a domain's
// business, and a domain supplies that through a DomainProfile. Video production
// was the original domain and is no longer baked in anywhere here.
package planning

// Acceptance is what "done" means: a criterion in prose for the executor to aim
// at, plus checks a reviewer can tick off one by one.
//
// Both halves earn their place. The criterion tells the executor what outcome is
// wanted, which is the part a person would say out loud. The checks exist because
// an outcome that cannot be checked is not an outcome — "finished" is not a
// criterion, and a run that reports success against one has told nobody anything.
type Acceptance struct {
	// Criteria is the outcome in prose, specific enough for an executor to aim
	// at without further questions.
	Criteria string `json:"criteria" yaml:"criteria"`
	// Checks are individually verifiable statements. A run is judged against
	// these, so each one has to be answerable yes or no.
	Checks []string `json:"checks,omitempty" yaml:"checks,omitempty"`
}

// IsEmpty reports whether nothing was declared. It is how the planner recognises
// a requirement that never said what finishing looks like, so it can be told to
// say — an undeclared acceptance is not the same as "anything goes".
func (a Acceptance) IsEmpty() bool {
	return a.Criteria == "" && len(a.Checks) == 0
}

// Requirement is a structured requirement as this engine sees it.
//
// Fields is deliberately opaque: it carries whatever the domain extracted, and
// the engine only forwards it (to the planner prompt, and into the DAG it
// builds). Interpreting those keys here is what coupled the engine to one
// product's vocabulary; not interpreting them is what keeps it general.
type Requirement struct {
	// Description says what is to be achieved, in one or two sentences.
	Description string `json:"description" yaml:"description"`
	// Fields holds domain-specific values, keyed however the domain likes.
	Fields map[string]any `json:"fields,omitempty" yaml:"fields,omitempty"`
	// Acceptance says what "done" means for the whole requirement. Steps may
	// refine it; this is the overall bar.
	Acceptance Acceptance `json:"acceptance" yaml:"acceptance"`
}

// DomainProfile supplies everything domain-specific the engine needs: what to
// extract from the user's text, what to tell the planner, and any pre-built
// shapes worth trying before asking a model.
//
// It is the seam A.11 named. The engine calls these; a domain implements them.
// Nothing about a particular product reaches the engine, and adding a second
// domain means adding an implementation rather than editing this package.
type DomainProfile interface {
	// Name identifies the profile, for logs and diagnostics.
	Name() string

	// ParseSystemPrompt is the instruction used to turn the user's text into a
	// Requirement. It is where a domain describes which Fields it wants, in the
	// domain's own words — the engine never inspects them.
	ParseSystemPrompt() string

	// PlanHints are extra instructions appended to the planning prompt. A
	// domain uses this to say what its steps tend to look like; a domain with
	// nothing to add returns "".
	PlanHints() string

	// Templates are pre-built DAG shapes to try before asking a model. Returning
	// none is valid and means every requirement goes to the planner — which is
	// what a domain with no recurring shape should do, rather than shipping a
	// template that never matches.
	Templates() []DAGTemplate
}

// GenericProfile is the profile with no domain: it asks for the description, the
// acceptance, and leaves Fields to the model's judgement.
//
// It exists because the engine must work for a requirement nobody has written a
// domain for. Shipping it as a real profile rather than as a nil check keeps one
// code path: the planner always has a profile.
type GenericProfile struct{}

// Name implements DomainProfile.
func (GenericProfile) Name() string { return "generic" }

// ParseSystemPrompt implements DomainProfile.
//
// It asks for the pieces the engine actually uses, and says so when they are
// missing rather than inventing them: a fabricated acceptance criterion would
// make a run look judged when nothing was ever specified.
func (GenericProfile) ParseSystemPrompt() string {
	return `你是一个需求分析师。把用户的自然语言描述整理成结构化需求。

输出 JSON，字段如下：
- description: 一句话概括要达成什么
- fields: 一个对象，装描述里给出的具体约定与参数（键名自定，例如 scope、target、deadline、constraints、references；没有就给空对象）
- acceptance: 什么算做完
  - criteria: 一段话描述验收标准，具体到执行者能直接照着做
  - checks: 可逐条核对的检查项，每项都能回答"是/否"

注意：
1. 只提取描述里确实说了的内容。没说的不要编造，也不要用 "<待补充>" 之类的占位符
2. acceptance.criteria 必须可核对。"做完了"、"处理好"这类不算标准
3. 如果描述里没说要什么算完成，就把 acceptance.criteria 留空——不要替用户发明标准
4. 只输出纯 JSON，不要 markdown 代码块，不要解释文字`
}

// PlanHints implements DomainProfile. The generic profile has no domain advice
// beyond what the planner already says.
func (GenericProfile) PlanHints() string { return "" }

// Templates implements DomainProfile. No templates: a shape worth pre-building
// has to come from a domain that knows it recurs, and this profile does not.
func (GenericProfile) Templates() []DAGTemplate { return nil }
