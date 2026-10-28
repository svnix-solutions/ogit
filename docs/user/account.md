# Your account

Everything here is under **Settings** (`/settings`).

## Signing in

Sign in with your email and password, a passkey, or a sign-in provider if your server has one (the buttons on the sign-in page). Your administrator decides whether you can register yourself and which email domains are allowed.

To change your password, use **Settings → Account**. It needs your current password, and it signs out your other sessions.

## Two-factor authentication

Under **Settings → Two-factor authentication**:

- **An authenticator app** (TOTP): scan the QR code, enter a first code.
- **Security keys** (WebAuthn): a hardware key, Touch ID, Windows Hello, a phone. A key added **as a passkey** also signs you in without your password ("Sign in with a passkey"), checking a PIN or fingerprint.
- **Recovery codes:** 10 single-use codes, shown once when you set up your first app or key. Keep them somewhere safe: they get you in if you lose your devices. You can make new ones (the old ones stop working).

Removing your last app and key turns two-factor authentication off. You're emailed (if the server sends mail) when it is turned on or off.

If you lose your devices and your recovery codes, ask a site admin to reset it.

### Confirming it's you

Sensitive actions ask you to confirm with a code, a key, or your password, if you haven't in the last 10 minutes. These include new access tokens and SSH keys, turning two-factor authentication off, and new recovery codes. The web UI asks when needed.

### When it's required

Your server, or a group you're in, may require two-factor authentication:

- **Site-wide:** until you set it up, you can sign in but only reach the setup page, and your access tokens and SSH keys don't work.
- **Group:** you keep your membership, but get no access through that group until you set it up.

Signing in through a provider whose own MFA your server trusts counts as two-factor.

## Sign-in providers

Under **Settings → Sign-in providers**, link your account to your server's providers (Keycloak, GitHub, Okta, LDAP…) and unlink them. You can't unlink your last way to sign in. An account created through a provider has no password; set one in **Settings → Account** if you want one.

## Access tokens

Git over HTTPS, the package registries and CI integrations use **personal access tokens**, not your password. Create them in **Settings → Access tokens**. A token is shown once: copy it then.

| Scope | Allows |
|---|---|
| `read:repository` | clone and fetch what you can read |
| `write:repository` | push where you're developer or higher |
| `read:packages` / `write:packages` | pull and publish [packages](./packages) |
| `write:status` | post commit statuses (CI) |

A token acts as you, limited to its scopes. It can have an expiry date, and you can delete it at any time. For git, the user name is the token's **name** and the password is the token itself.

```bash
curl -X POST https://git.example.com/api/v1/user/token -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' \
  -d '{"name": "laptop", "scopes": ["read:repository", "write:repository"]}'
```

## SSH keys

Add your public key in **Settings → SSH keys** to use [git over SSH](./git#over-ssh). oGit accepts ed25519, ECDSA, RSA with at least 3072 bits, and security-key (`sk-`) variants. A key can only belong to one account. You're emailed when a key is added (if the server sends mail).

## Notification emails

**Settings → Notification emails** chooses which [notifications](./notifications) are also emailed: `participating` (the default: threads you take part in), `all` (also what you watch), or `none`.
