package actrunner

import (
	"testing"

	"github.com/labbs/ogit/application"
	"github.com/stretchr/testify/assert"
)

func TestJobServerURL(t *testing.T) {
	task := &application.Task{ServerURL: "https://git.example.com/"}
	assert.Equal(t, "https://git.example.com", jobServerURL(task, Options{}), "the server's address by default")
	assert.Equal(t, "http://ogit:8080", jobServerURL(task, Options{JobURL: "http://ogit:8080/"}), "--job-url wins")
}
