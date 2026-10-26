package totp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// RFC 6238 appendix B (SHA1, 8 digits) with its ASCII key.
func TestRFC6238Vectors(t *testing.T) {
	key := []byte("12345678901234567890")
	for unix, want := range map[int64]string{
		59: "94287082", 1111111109: "07081804", 1111111111: "14050471",
		1234567890: "89005924", 2000000000: "69279037", 20000000000: "65353130",
	} {
		assert.Equal(t, want, code(key, Step(time.Unix(unix, 0)), 8), "t=%d", unix)
	}
}

func TestValidate(t *testing.T) {
	secret, err := NewSecret()
	assert.NoError(t, err)
	now := time.Unix(1_800_000_000, 0)
	c, _ := Code(secret, Step(now))
	step, ok := Validate(secret, c, now)
	assert.True(t, ok)
	assert.Equal(t, Step(now), step)

	prev, _ := Code(secret, Step(now)-1)
	_, ok = Validate(secret, prev, now)
	assert.True(t, ok, "one step of drift")
	old, _ := Code(secret, Step(now)-2)
	_, ok = Validate(secret, old, now)
	assert.False(t, ok, "two steps is too old")
	_, ok = Validate(secret, "12345", now)
	assert.False(t, ok)
	_, ok = Validate(secret, c[:3]+" "+c[3:], now)
	assert.True(t, ok, "spaces are ignored")
}

func TestURI(t *testing.T) {
	assert.Equal(t, "otpauth://totp/oGit:alice@example.com?algorithm=SHA1&digits=6&issuer=oGit&period=30&secret=ABC", URI("oGit", "alice@example.com", "ABC"))
}
