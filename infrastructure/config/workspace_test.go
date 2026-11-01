package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckWorkspaceDomain(t *testing.T) {
	assert.NoError(t, CheckWorkspaceDomain("", "https://git.example.com"), "off")
	assert.NoError(t, CheckWorkspaceDomain("workspaces.example.com", "https://git.example.com"))
	assert.NoError(t, CheckWorkspaceDomain("workspaces.example.com", ""))
	for _, domain := range []string{"https://w.example.com", "w.example.com:8443", "w.example.com/x", "*.example.com", ".example.com", "example.com."} {
		assert.Error(t, CheckWorkspaceDomain(domain, ""), domain)
	}
	assert.Error(t, CheckWorkspaceDomain("GIT.example.com", "https://git.example.com:8443"), "the server's own host")
}
