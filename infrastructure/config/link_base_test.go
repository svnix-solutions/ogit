package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLinkBase(t *testing.T) {
	var none Config
	assert.Equal(t, "http://ogit:8080", none.LinkBase("http://ogit:8080"), "no public URL: the request's address")

	var c Config
	c.Server.PublicURL = "https://git.example.com/"
	assert.Equal(t, "https://git.example.com", c.LinkBase("http://git.example.com"), "the public host keeps the public URL (https behind a proxy)")
	assert.Equal(t, "https://git.example.com", c.LinkBase("http://GIT.example.com"), "host names compare without case")
	assert.Equal(t, "http://ogit:8080", c.LinkBase("http://ogit:8080"), "another address: links a job can reach")
	assert.Equal(t, "https://git.example.com", c.LinkBase(""), "no request address")
}
