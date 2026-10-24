package application

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"

	"github.com/labbs/ogit/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func authorizedLine(t *testing.T, pub any, comment string) string {
	t.Helper()
	k, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) + " " + comment
}

func TestParseSSHKey(t *testing.T) {
	edPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	rsa2048, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	rsa3072, err := rsa.GenerateKey(rand.Reader, 3072)
	require.NoError(t, err)

	t.Run("accepted", func(t *testing.T) {
		for name, tc := range map[string]struct {
			pub  any
			want domain.SSHType
		}{
			"ed25519":  {edPub, domain.SSHTypeED25519},
			"ecdsa":    {&ec.PublicKey, domain.SSHTypeECDSA},
			"rsa_3072": {&rsa3072.PublicKey, domain.SSHTypeRSA},
		} {
			t.Run(name, func(t *testing.T) {
				parsed, err := ParseSSHKey("  " + authorizedLine(t, tc.pub, "me@laptop") + "\n")
				require.NoError(t, err)
				assert.Equal(t, tc.want, parsed.Type)
				assert.Equal(t, "me@laptop", parsed.Comment)
				assert.True(t, strings.HasPrefix(parsed.Fingerprint, "SHA256:"))
			})
		}
	})

	t.Run("rejected", func(t *testing.T) {
		for name, text := range map[string]string{
			"rsa_2048": authorizedLine(t, &rsa2048.PublicKey, "old"),
			"garbage":  "ssh-ed25519 not-base64",
			"empty":    "",
			"two_keys": authorizedLine(t, edPub, "a") + "\n" + authorizedLine(t, &ec.PublicKey, "b"),
			"private":  "-----BEGIN OPENSSH PRIVATE KEY-----",
		} {
			t.Run(name, func(t *testing.T) {
				_, err := ParseSSHKey(text)
				assert.ErrorIs(t, err, ErrInvalidSSHKey)
			})
		}
	})
}
