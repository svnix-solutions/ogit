package netguard

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPermitted(t *testing.T) {
	g, err := New([]string{"10.1.0.0/16", "192.168.5.5", "ci.internal"}, "test")
	require.NoError(t, err)
	for ip, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "::1": false, "10.0.0.1": false, "172.16.3.4": false, "192.168.1.1": false,
		"169.254.169.254": false, "fe80::1": false, "fc00::1": false, "100.64.1.1": false, "0.0.0.0": false,
		"::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false,
		"10.1.2.3": true, "192.168.5.5": true, // allowed
	} {
		assert.Equal(t, want, g.Permitted("example.com", net.ParseIP(ip)), ip)
	}
	assert.True(t, g.Permitted("CI.internal", net.ParseIP("10.9.9.9")), "an allowed host name")
	assert.False(t, g.Permitted("example.com", nil))
	_, err = New([]string{"10.0.0.0/99"}, "x.allowed_hosts")
	assert.ErrorContains(t, err, "x.allowed_hosts")
}

func TestResolveIP(t *testing.T) {
	g, err := New(nil, "test")
	require.NoError(t, err)
	g.Resolve = func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "inside.example":
			return []net.IP{net.ParseIP("10.0.0.5")}, nil
		case "mixed.example":
			return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("93.184.216.34")}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host}
	}
	_, err = g.ResolveIP(context.Background(), "inside.example")
	assert.ErrorIs(t, err, ErrBlockedAddress)
	ip, err := g.ResolveIP(context.Background(), "mixed.example")
	require.NoError(t, err)
	assert.Equal(t, "93.184.216.34", ip.String(), "a permitted address of the name")
	_, err = g.ResolveIP(context.Background(), "127.0.0.1")
	assert.ErrorIs(t, err, ErrBlockedAddress)
}
