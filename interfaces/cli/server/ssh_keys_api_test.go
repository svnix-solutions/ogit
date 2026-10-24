package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/mail/mailtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSHKeyAPI(t *testing.T) {
	srv := startTestServer(t, func(c *config.Config) { c.SMTP.Host = "smtp.invalid" }) // "configured": the recorder sends
	rec := &mailtest.Recorder{}
	srv.deps.SSHKeyApp.Mailer = rec
	alice, bob := srv.newUser(t, "alice"), srv.newUser(t, "bob")
	key := newSSHKey(t, "alice@laptop")

	t.Run("add_list_delete", func(t *testing.T) {
		status, body := srv.post(t, "/api/v1/user/ssh-keys", alice.session, map[string]any{"key": key.authorized})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "alice@laptop", body["name"], "name defaults to the comment")
		assert.Equal(t, "ed25519", body["type"])
		assert.NotContains(t, body, "read_only", "user keys have no read_only")
		fingerprint := body["fingerprint"].(string)

		msgs := rec.Messages("alice@example.com")
		require.Len(t, msgs, 1, "the user is notified")
		assert.Contains(t, msgs[0].Body, fingerprint)

		status, body = srv.get(t, "/api/v1/user/ssh-keys", alice.session)
		require.Equal(t, http.StatusOK, status)
		require.Len(t, body["keys"], 1)

		status, body = srv.get(t, "/api/v1/user/ssh-keys", bob.session)
		require.Equal(t, http.StatusOK, status)
		assert.Empty(t, body["keys"], "keys are per user")
	})

	t.Run("validation", func(t *testing.T) {
		status, body := srv.post(t, "/api/v1/user/ssh-keys", alice.session, map[string]any{"key": "ssh-dss AAAA"})
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "INVALID_SSH_KEY", body["type"])

		status, body = srv.post(t, "/api/v1/user/ssh-keys", bob.session, map[string]any{"key": key.authorized})
		assert.Equal(t, http.StatusConflict, status, "a key is used in one place only")
		assert.Equal(t, "SSH_KEY_IN_USE", body["type"])

		other := newSSHKey(t, "x")
		status, body = srv.post(t, "/api/v1/user/ssh-keys", alice.session, map[string]any{"key": other.authorized, "name": "alice@laptop"})
		assert.Equal(t, http.StatusConflict, status, "names are unique per user")
		assert.Equal(t, "SSH_KEY_NAME_TAKEN", body["type"])
		status, _ = srv.post(t, "/api/v1/user/ssh-keys", bob.session, map[string]any{"key": other.authorized, "name": "alice@laptop"})
		assert.Equal(t, http.StatusOK, status, "but not across users")

		bare := strings.Join(strings.Fields(newSSHKey(t, "c").authorized)[:2], " ")
		status, _ = srv.post(t, "/api/v1/user/ssh-keys", alice.session, map[string]any{"key": bare})
		assert.Equal(t, http.StatusBadRequest, status, "a name is needed when the key has no comment")
	})

	t.Run("cannot_delete_others_keys", func(t *testing.T) {
		_, body := srv.get(t, "/api/v1/user/ssh-keys", alice.session)
		id := body["keys"].([]any)[0].(map[string]any)["id"].(string)
		status, _ := srv.request(t, http.MethodDelete, "/api/v1/user/ssh-keys/"+id, bob.session, nil)
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("no_notification_without_smtp", func(t *testing.T) {
		plain := startTestServer(t)
		quiet := &mailtest.Recorder{}
		plain.deps.SSHKeyApp.Mailer = quiet
		u := plain.newUser(t, "carol")
		status, _ := plain.post(t, "/api/v1/user/ssh-keys", u.session, map[string]any{"key": newSSHKey(t, "c").authorized})
		require.Equal(t, http.StatusOK, status)
		assert.Empty(t, quiet.Messages("carol@example.com"))
	})
}

func TestDeployKeyAPI(t *testing.T) {
	srv := startTestServer(t)
	owner, maint, dev, outsider := srv.newUser(t, "owner"), srv.newUser(t, "maint"), srv.newUser(t, "dev"), srv.newUser(t, "outsider")
	srv.createRepo(t, owner.session, "App", true)
	for name, role := range map[string]string{"maint": "maintainer", "dev": "developer"} {
		status, _ := srv.request(t, http.MethodPost, repoAPI("owner/app")+"/members", owner.session, map[string]any{"username": name, "role": role})
		require.Equal(t, http.StatusOK, status)
	}
	url := repoAPI("owner/app") + "/deploy-keys"

	status, body := srv.post(t, url, maint.session, map[string]any{"key": newSSHKey(t, "ci").authorized, "read_only": false})
	require.Equal(t, http.StatusOK, status, "maintainers add deploy keys: %v", body)
	assert.Equal(t, false, body["read_only"])
	id := body["id"].(string)

	status, _ = srv.post(t, url, dev.session, map[string]any{"key": newSSHKey(t, "x").authorized})
	assert.Equal(t, http.StatusForbidden, status, "developers cannot")
	status, _ = srv.get(t, url, dev.session)
	assert.Equal(t, http.StatusForbidden, status)
	status, _ = srv.get(t, url, outsider.session)
	assert.Equal(t, http.StatusNotFound, status, "non-members do not learn the repo exists")

	status, body = srv.get(t, url, owner.session)
	require.Equal(t, http.StatusOK, status)
	assert.Len(t, body["keys"], 1)

	status, _ = srv.request(t, http.MethodDelete, url+"/"+id, dev.session, nil)
	assert.Equal(t, http.StatusForbidden, status)
	status, _ = srv.request(t, http.MethodDelete, url+"/"+id, owner.session, nil)
	assert.Equal(t, http.StatusOK, status)
}

func TestAdminSSHKeys(t *testing.T) {
	srv := startTestServer(t)
	admin := srv.bootstrapAdmin(t)
	alice := srv.newUser(t, "alice")
	status, body := srv.post(t, "/api/v1/user/ssh-keys", alice.session, map[string]any{"key": newSSHKey(t, "alice@laptop").authorized})
	require.Equal(t, http.StatusOK, status)
	id := body["id"].(string)

	status, _ = srv.get(t, "/api/v1/admin/users/alice/ssh-keys", alice.session)
	assert.Equal(t, http.StatusForbidden, status, "admins only")

	status, body = srv.get(t, "/api/v1/admin/users/alice/ssh-keys", admin.session)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["keys"], 1)

	status, _ = srv.request(t, http.MethodDelete, "/api/v1/admin/users/alice/ssh-keys/"+id, admin.session, nil)
	require.Equal(t, http.StatusOK, status)
	_, body = srv.get(t, "/api/v1/user/ssh-keys", alice.session)
	assert.Empty(t, body["keys"], "revoked")
}
