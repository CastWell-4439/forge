package review

import "fmt"

const reviewSystemPrompt = `You are a senior code reviewer. Your task is to carefully review the given code or plan and provide structured feedback.

Guidelines:
- Be specific: reference file paths and line numbers where possible
- Categorize issues: 🔴 Critical, 🟡 Warning, ⬜ Suggestion
- Explain WHY something is problematic, not just WHAT
- Suggest concrete fixes
- Acknowledge good patterns too

Output format: Markdown with categorized findings and a summary verdict (APPROVE / REQUEST_CHANGES / NEEDS_DISCUSSION).`

func buildReviewPlanPrompt(plan, ragContext string) string {
	p := fmt.Sprintf("Review the following implementation plan:\n\n%s", plan)
	if ragContext != "" {
		p += fmt.Sprintf("\n\n---\nProject conventions and past review notes:\n%s", ragContext)
	}
	p += `

Check for:
1. Completeness — are all requirements covered?
2. Feasibility — are there technical risks or unknowns?
3. Dependencies — are they correctly identified?
4. Testing strategy — is it adequate?
5. Security implications — any concerns?`
	return p
}

func buildReviewCodePrompt(diff, planContext, ragContext string) string {
	p := fmt.Sprintf("Review the following code diff:\n\n```diff\n%s\n```", diff)
	if planContext != "" {
		p += fmt.Sprintf("\n\nOriginal plan context:\n%s", planContext)
	}
	if ragContext != "" {
		p += fmt.Sprintf("\n\n---\nProject coding standards:\n%s", ragContext)
	}
	p += `

Check for:
1. Correctness — does it do what it's supposed to?
2. Error handling — are errors properly caught and propagated?
3. Edge cases — are boundary conditions handled?
4. Naming — are names clear and consistent?
5. Performance — any obvious inefficiencies?
6. Tests — are changes adequately tested?`
	return p
}

func buildSecurityReviewPrompt(diff, ragContext string) string {
	p := fmt.Sprintf("Perform a security review of the following code diff:\n\n```diff\n%s\n```", diff)
	if ragContext != "" {
		p += fmt.Sprintf("\n\n---\nSecurity guidelines:\n%s", ragContext)
	}
	p += `

Check for:
1. Injection vulnerabilities (SQL, command, XSS)
2. Authentication/authorization bypasses
3. Sensitive data exposure (secrets, PII in logs)
4. Input validation gaps
5. Insecure deserialization
6. Missing rate limiting or resource bounds
7. Cryptographic misuse`
	return p
}
