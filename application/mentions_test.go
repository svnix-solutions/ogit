package application

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMentions(t *testing.T) {
	assert.Equal(t, []string{"bob", "team.lead"}, Mentions("Thanks @bob, cc @team.lead. Also @bob again."))
	assert.Nil(t, Mentions("mail me at bob@example.com or see a/@path"), "emails and paths are not mentions")
	assert.Equal(t, []string{"alice"}, Mentions("(@alice)"))
}
