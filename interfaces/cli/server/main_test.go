package server

import (
	"os"
	"testing"

	"github.com/labbs/ogit/application"
	"golang.org/x/crypto/bcrypt"
)

// Password hashes at the minimum cost: at the default, each test user's
// registration and login spend about a second under the race detector.
// Set before any test runs, so the dummy hash for unknown accounts has the
// same cost as real ones (TestLoginDoesNotRevealAccounts).
func TestMain(m *testing.M) {
	application.PasswordCost = bcrypt.MinCost
	os.Exit(m.Run())
}
