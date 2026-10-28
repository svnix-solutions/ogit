# Two-factor authentication

Users turn on two-factor authentication in their settings, with an authenticator app (TOTP), security keys or passkeys (WebAuthn), and get 10 single-use recovery codes. This page is about what the server needs and what admins can require.

## Server settings

```yaml
http:
  public_url: https://git.example.com   # its host is the WebAuthn relying party
security:
  encryption_key: …                     # at least 32 bytes
```

- **`security.encryption_key`** encrypts authenticator app secrets. Without it, users can't add an app. Keep it: a new key turns app codes off for everyone (recovery codes still work).
- **Security keys and passkeys** need `http.public_url`: its host is the relying party, so keys only work on that address. Changing the host makes existing keys unusable.

## Sudo mode

Sensitive actions need a session confirmed in the last 10 minutes: turning 2FA off, new recovery codes, new access tokens, SSH and deploy keys, repository tokens, linking and unlinking sign-in providers, changing sign-in providers (admins) and resetting someone's 2FA. Otherwise they answer 403 `SUDO_REQUIRED`, and the web UI asks the user to confirm (a code, a key, or the password without 2FA).

Access tokens and SSH keys keep working for git and CI without codes.

## Requiring it

- **Site-wide:** `security.require_2fa: true`, or on the admin page (`PATCH /api/v1/admin/settings {"require_two_factor": true}`).
  - Users without 2FA can still sign in, but only reach its setup until they add an app or a key. The API answers 403 `TWO_FACTOR_REQUIRED`, and their access tokens and SSH keys are refused.
  - Repository tokens and deploy keys keep working.
  - Sessions from a [single sign-on](./sso) provider whose MFA is trusted count as two-factor.
- **Per group:** the group's owner sets `require_two_factor` (`PATCH /api/v1/group/<name>`). Members without 2FA get no access to the group's repositories through the group, and can't create repositories in it, until they set it up. They stay members; owners and maintainers see who lacks 2FA. Sub-groups inherit the requirement.
- **Site admins** always need 2FA for admin actions (`ADMIN_TWO_FACTOR_REQUIRED`). `ogit admin` on the server is not affected.

## Lost devices

When a user has lost both their devices and their recovery codes, an admin turns their 2FA off:

```bash
./ogit admin reset-2fa -c config.yaml --username someone
# or: POST /api/v1/admin/users/<username>/reset-2fa (with sudo)
```

The user is emailed (with SMTP set) and can set it up again.
