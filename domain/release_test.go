package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAssetName(t *testing.T) {
	for in, want := range map[string]string{
		"app_linux_amd64.tar.gz": "app_linux_amd64.tar.gz",
		"my file (1).zip":        "my.file.1.zip",
		"../../etc/passwd":       "etc.passwd",
		".hidden":                "hidden",
		"日本.txt":                 "txt",
		"   ":                    "",
		"a/b\\c":                 "a.b.c",
		strings.Repeat("x", 300): strings.Repeat("x", 255),
	} {
		assert.Equal(t, want, AssetName(in), in)
	}
}

func TestTagRuleFor(t *testing.T) {
	rules := []ProtectedTag{{Pattern: "v*"}, {Pattern: "v1.0.0"}, {Pattern: "release/**"}}
	assert.Equal(t, "v1.0.0", TagRuleFor(rules, "v1.0.0").Pattern, "exact names win")
	assert.Equal(t, "v*", TagRuleFor(rules, "v2").Pattern)
	assert.Equal(t, "release/**", TagRuleFor(rules, "release/2025/1").Pattern)
	assert.Nil(t, TagRuleFor(rules, "nightly"))
	assert.Nil(t, TagRuleFor(rules, "x/v1"), "patterns are anchored")
}
