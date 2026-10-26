package gitbrowse

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParsePointer(t *testing.T) {
	oid := strings.Repeat("ab", 32)
	p, ok := ParsePointer([]byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\nsize 12345\n"))
	assert.True(t, ok)
	assert.Equal(t, &LFSPointer{Oid: oid, Size: 12345}, p)
	_, ok = ParsePointer([]byte("version https://git-lfs.github.com/spec/v1\next-0-foo sha256:" + oid + "\noid sha256:" + oid + "\nsize 1\n"))
	assert.True(t, ok, "extensions")

	for name, text := range map[string]string{
		"no size":        "version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\n",
		"other version":  "version https://example.com/v2\noid sha256:" + oid + "\nsize 1\n",
		"short oid":      "version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 1\n",
		"no newline":     "version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\nsize 1",
		"text after":     "version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\nsize 1\nhello world, this is text\n",
		"plain text":     "hello\n",
		"too large":      "version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\nsize 1\n" + strings.Repeat("x-y z\n", 200),
		"negative size":  "version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\nsize -1\n",
		"uppercase hash": "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.ToUpper(oid) + "\nsize 1\n",
	} {
		_, ok := ParsePointer([]byte(text))
		assert.False(t, ok, name)
	}
}
