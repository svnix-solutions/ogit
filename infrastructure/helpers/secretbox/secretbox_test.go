package secretbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSealOpen(t *testing.T) {
	b, err := New("a key that is at least thirty-two bytes")
	require.NoError(t, err)
	s1, err := b.Seal("JBSWY3DPEHPK3PXP")
	require.NoError(t, err)
	s2, _ := b.Seal("JBSWY3DPEHPK3PXP")
	assert.NotEqual(t, s1, s2, "random nonces")
	assert.NotContains(t, s1, "JBSWY3DPEHPK3PXP")
	got, err := b.Open(s1)
	require.NoError(t, err)
	assert.Equal(t, "JBSWY3DPEHPK3PXP", got)

	other, _ := New("another key that is at least thirty-two bytes")
	_, err = other.Open(s1)
	assert.Error(t, err, "another key can't read it")
	tampered := []byte(s1)
	tampered[len(tampered)-3] ^= 1
	_, err = b.Open(string(tampered))
	assert.Error(t, err)

	none, err := New("")
	require.NoError(t, err)
	_, err = none.Seal("x")
	assert.ErrorIs(t, err, ErrNoKey)
}
