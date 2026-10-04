package database

import (
	"fmt"
	"strings"
)

// GuardReadOnlySQL validates a query against the read-only contract and
// returns it ready to run (a MaxRows LIMIT is appended when missing).
//
// It is shared by the database workflow worker and the agent's data.query
// tool: one implementation so the two consumers cannot drift — a security
// rule that exists in two copies is a security rule that will eventually
// exist in one.
func GuardReadOnlySQL(sql string) (string, error) {
	// Security: only SELECT allowed.
	normalized := strings.TrimSpace(strings.ToUpper(sql))
	if !strings.HasPrefix(normalized, "SELECT") {
		return "", fmt.Errorf("only SELECT queries allowed, got: %s", firstWord(sql))
	}

	// Dangerous patterns, matched as whole words: a substring check used to
	// reject ordinary columns like updated_at / created_at ("UPDATE" inside
	// "UPDATED_AT"), which made the read-only path refuse exactly the
	// queries it exists to run. Matching words keeps the protection (a real
	// UPDATE/DROP still trips) while staying conservative inside string
	// literals — "'drop table'" in a comment or value still blocks, which
	// errs toward refusing a legitimate query rather than passing a clever
	// one.
	if m := forbiddenKeyword.FindString(normalized); m != "" {
		return "", fmt.Errorf("query contains forbidden keyword %q", m)
	}

	// Enforce LIMIT.
	if !strings.Contains(normalized, "LIMIT") {
		sql = sql + fmt.Sprintf(" LIMIT %d", MaxRows)
	}
	return sql, nil
}
