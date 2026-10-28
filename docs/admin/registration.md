# Registration and email

## Who can register

```yaml
registration:
  enabled: true
  domain_whitelist: []        # e.g. ["example.com"]: only these email domains
  password_min_length: 12
  password_complexity: true   # upper and lower case, a digit and a symbol
```

With `enabled: false`, nobody registers, and admins create accounts (`POST /api/v1/admin/users`, see [Site administration](./administration)) or [single sign-on](./sso) creates them. With `security.sso_only`, registration is off too.

Sign-in never reveals whether an account exists: an unknown email and a wrong password get the same answer, in the same time. An account's state (disabled, unverified) is only reported after a correct password.

## Outgoing mail

```yaml
smtp:
  host: smtp.example.com
  port: 587
  username: ogit            # optional
  password: …
  from: "oGit <git@example.com>"
  tls: starttls             # starttls, tls (implicit, usually port 465) or none
```

With SMTP set, oGit sends verification links, notifications (by each user's preference), and security alerts: two-factor authentication turned on, off or reset, and SSH keys added.

## Email verification

```yaml
registration:
  require_email_verification: true
  verification_ttl_hours: 24
  verification_url_template: "{public_url}/api/v1/auth/verify?token={token}"
```

New users receive a link and can't sign in until they open it. The server refuses to start unless `smtp.host`, `smtp.from` and `http.public_url` are set.

- Accounts that existed before the setting was turned on, or were created while it was off, count as verified.
- `POST /api/v1/auth/verify/resend {"email": "…"}` sends a new link. It sends at most one per minute, older links stop working, and it gives the same answer whether or not the address is registered.
- If the email can't be sent at registration, the account is still created and the user can ask for a new link.
- `verification_url_template` changes the link, e.g. to point at another front end that calls `POST /api/v1/auth/verify {"token": "…"}`.
- Admins can mark an account verified: `PATCH /api/v1/admin/users/<username> {"email_verified": true}`.
