package application

import (
	"bytes"
	"strings"
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

func TestValidateTokenDoesNotLogToken(t *testing.T) {
	var logs bytes.Buffer
	var cfg config.Config
	cfg.Session.SecretKey = strings.Repeat("s", 32)
	app := NewSessionApp(cfg, zerolog.New(&logs), nil, nil)

	const token = "eyJhbGciOiJIUzI1NiJ9.eyJzZXNzaW9uX2lkIjoieCJ9.not-a-valid-signature"
	_, err := app.ValidateToken(token)

	assert.Error(t, err)
	assert.NotEmpty(t, logs.String(), "the failure is still logged")
	assert.NotContains(t, logs.String(), token)
	assert.NotContains(t, logs.String(), "not-a-valid-signature")
}
