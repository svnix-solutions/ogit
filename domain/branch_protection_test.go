package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatchBranch(t *testing.T) {
	for _, c := range []struct {
		pattern, branch string
		want            bool
	}{
		{"main", "main", true},
		{"main", "main2", false},
		{"release/*", "release/v1", true},
		{"release/*", "release/v1/hotfix", false},
		{"release/**", "release/v1/hotfix", true},
		{"release/*", "release", false},
		{"v?", "v1", true},
		{"v?", "v10", false},
		{"*", "main", true},
		{"*", "feature/x", false},
		{"**", "feature/x", true},
		{"feat.x", "featyx", false}, // . is literal
		{"a+b", "a+b", true},
	} {
		assert.Equal(t, c.want, MatchBranch(c.pattern, c.branch), "%s vs %s", c.pattern, c.branch)
	}
}

func TestValidBranchPattern(t *testing.T) {
	for _, p := range []string{"main", "release/*", "feature/**", "v?.x", "a-b_c@1"} {
		assert.True(t, ValidBranchPattern(p), p)
	}
	for _, p := range []string{"", "/main", "main/", "a//b", "a..b", "a b", "***", "x\\y", "a~b"} {
		assert.False(t, ValidBranchPattern(p), p)
	}
}

func TestRuleForPicksTheMostSpecific(t *testing.T) {
	rules := []BranchProtection{{Pattern: "**"}, {Pattern: "release/*"}, {Pattern: "release/v1"}, {Pattern: "*"}}
	assert.Equal(t, "release/v1", RuleFor(rules, "release/v1").Pattern, "exact names first")
	assert.Equal(t, "release/*", RuleFor(rules, "release/v2").Pattern, "then the most literal characters")
	assert.Equal(t, "*", RuleFor(rules, "main").Pattern)
	assert.Equal(t, "**", RuleFor(rules, "feature/a/b").Pattern)
	assert.Nil(t, RuleFor(rules[1:3], "main"))
}
