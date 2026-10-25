package domain

import (
	"regexp"
	"strings"
	"time"
)

// BranchProtection protects the branches matching Pattern: force-pushes and
// deletion are refused unless allowed, and the other options add rules.
type BranchProtection struct {
	Id           string `gorm:"primaryKey"`
	RepositoryId string
	// Pattern is a branch name or a glob: * matches within a path segment,
	// ** across segments, ? one character (release/*, feature/**).
	Pattern string

	AllowForcePush bool
	AllowDeletion  bool
	// RequirePullRequest refuses direct pushes: the branch only moves by
	// merging pull requests (creating it by a push is still possible).
	RequirePullRequest bool
	// RequiredApprovals for merging into the branch; nil uses the
	// repository's required_approvals.
	RequiredApprovals *int
	// DismissStaleApprovals: only approvals of the pull request's current
	// head commit count.
	DismissStaleApprovals bool
	// AllowedUserIds and AllowedGroupIds restrict who may push to the branch
	// and merge into it (they still need write access). Both empty: anyone
	// with write access.
	AllowedUserIds  StringList
	AllowedGroupIds StringList
	// OwnersBypass lets repository owners ignore the rule (logged).
	OwnersBypass bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (BranchProtection) TableName() string { return "branch_protection" }

// Restricted reports whether the rule limits who may push and merge.
func (p BranchProtection) Restricted() bool {
	return len(p.AllowedUserIds) > 0 || len(p.AllowedGroupIds) > 0
}

type BranchProtectionPers interface {
	List(repositoryId string) ([]BranchProtection, error)
	Get(repositoryId, id string) (*BranchProtection, error)
	Create(rule *BranchProtection) error
	Update(id string, fields map[string]any) error
	Delete(id string) error
}

var validPattern = regexp.MustCompile(`^[A-Za-z0-9._/*?+@-]{1,200}$`)

// ValidBranchPattern accepts branch names and globs, without "..", empty
// segments or a leading or trailing "/".
func ValidBranchPattern(p string) bool {
	return validPattern.MatchString(p) && !strings.Contains(p, "..") && !strings.Contains(p, "//") &&
		!strings.HasPrefix(p, "/") && !strings.HasSuffix(p, "/") && !strings.Contains(p, "***")
}

// patternRegexp compiles a glob: ** any characters, * any but "/", ? one
// character but "/".
func patternRegexp(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		switch {
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i++
		case p[i] == '*':
			b.WriteString("[^/]*")
		case p[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(p[i : i+1]))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// MatchBranch reports whether branch (without refs/heads/) matches pattern.
func MatchBranch(pattern, branch string) bool {
	if !strings.ContainsAny(pattern, "*?") {
		return pattern == branch
	}
	return patternRegexp(pattern).MatchString(branch)
}

// RuleFor returns the rule protecting branch, or nil. When several match,
// the most specific wins: an exact name, then the pattern with the most
// literal characters (then the shorter, then alphabetical order).
func RuleFor(rules []BranchProtection, branch string) *BranchProtection {
	var best *BranchProtection
	for i := range rules {
		r := &rules[i]
		if !MatchBranch(r.Pattern, branch) {
			continue
		}
		if best == nil || moreSpecific(r.Pattern, best.Pattern) {
			best = r
		}
	}
	return best
}

func moreSpecific(a, b string) bool {
	ea, eb := !strings.ContainsAny(a, "*?"), !strings.ContainsAny(b, "*?")
	if ea != eb {
		return ea
	}
	la, lb := literals(a), literals(b)
	if la != lb {
		return la > lb
	}
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func literals(p string) int {
	return len(p) - strings.Count(p, "*") - strings.Count(p, "?")
}
