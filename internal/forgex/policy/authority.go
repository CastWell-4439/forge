package policy

import "strings"

// AuthorityLevel describes how much autonomy a run has for tool execution.
type AuthorityLevel string

const (
	AuthorityL0 AuthorityLevel = "L0" // suggest only
	AuthorityL1 AuthorityLevel = "L1" // generate, human confirms
	AuthorityL2 AuthorityLevel = "L2" // execute low-risk tools
	AuthorityL3 AuthorityLevel = "L3" // execute with approval
	AuthorityL4 AuthorityLevel = "L4" // long-running autonomous
)

// Valid reports whether the authority level is known.
func (a AuthorityLevel) Valid() bool {
	switch NormalizeAuthority(a) {
	case AuthorityL0, AuthorityL1, AuthorityL2, AuthorityL3, AuthorityL4:
		return true
	default:
		return false
	}
}

// NormalizeAuthority uppercases and trims an authority level, defaulting empty to L0.
func NormalizeAuthority(a AuthorityLevel) AuthorityLevel {
	v := strings.ToUpper(strings.TrimSpace(string(a)))
	if v == "" {
		return AuthorityL0
	}
	return AuthorityLevel(v)
}

// CompareAuthority returns -1, 0, or 1 when a is below, equal to, or above b.
func CompareAuthority(a, b AuthorityLevel) int {
	ar := authorityRank(NormalizeAuthority(a))
	br := authorityRank(NormalizeAuthority(b))
	if ar < br {
		return -1
	}
	if ar > br {
		return 1
	}
	return 0
}

func authorityRank(a AuthorityLevel) int {
	switch NormalizeAuthority(a) {
	case AuthorityL0:
		return 0
	case AuthorityL1:
		return 1
	case AuthorityL2:
		return 2
	case AuthorityL3:
		return 3
	case AuthorityL4:
		return 4
	default:
		return -1
	}
}
