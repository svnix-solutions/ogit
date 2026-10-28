# Single sign-on

oGit signs users in through four kinds of providers:

| Kind | For | Users sign in |
|---|---|---|
| `oidc` | OpenID Connect: Keycloak, Authentik, Entra ID, Okta, Google, GitLab… | with a button on the sign-in page |
| `github` | GitHub (OAuth2), optionally limited to organizations; GitHub Enterprise with `issuer` | with a button |
| `saml` | SAML 2.0 identity providers | with a button |
| `ldap` | LDAP and Active Directory | with the normal sign-in form |

## Adding a provider

Every provider needs `http.public_url`, and secrets need `security.encryption_key`. Providers are either:

- added by site admins on the admin page (`POST /api/v1/admin/sso/providers`, with sudo), or
- declared under `sso.providers` in the configuration file. These are read-only on the admin page.

```yaml
sso:
  providers:
    - slug: keycloak                 # in URLs: a-z, 0-9, -
      name: Company SSO              # on the sign-in button
      kind: oidc
      issuer: https://sso.example.com/realms/main
      client_id: ogit
      client_secret: ${OGIT_OIDC_SECRET}   # read from the environment
      allowed_domains: "example.com"       # optional: only these verified emails
      auto_create: true
      groups_claim: groups
      admin_group: ogit-admins
      group_mappings:
        - {group: engineering, target: eng, role: member}
```

Register this redirect URI at the provider (OIDC and GitHub):

```txt
{http.public_url}/api/v1/auth/sso/<slug>/callback
```

The [Keycloak guide](/guides/sso-keycloak) goes through a complete setup.

## Accounts

- **New users** get an account the first time they sign in (`auto_create`, on by default) when the provider verified their email, and it is in `allowed_domains` if set. Their user name comes from the provider, made unique, and they have no password.
- **Existing users:** when the verified email matches an account here, the user confirms with their password (and 2FA code) once, which links the provider to the account.
- In their settings, users link more providers and unlink them (never their last way to sign in). Users without a password can set one.

## Two-factor authentication

The provider's MFA is trusted for OIDC, GitHub and SAML: oGit doesn't ask for its own 2FA code, and such sessions count as two-factor for [`security.require_2fa`](./two-factor#requiring-it). Users without a password, who sign in only through such providers, count as having 2FA everywhere: tokens, groups and admin actions.

With `require_two_factor: true` on a provider, users who have oGit 2FA are asked for it after the provider.

A directory (LDAP) password is one factor only: users who have oGit 2FA are asked for it.

## Group sync

Per provider:

- **`groups_claim`** (default `groups`): where the user's groups are in the provider's claims. A dotted path works, e.g. `realm_access.roles`. For GitHub, the groups are the user's organizations listed in `allowed_orgs`. For SAML, it's the groups attribute.
- **`admin_group`:** its members are site admins.
- **`group_mappings`:** `[{group: <provider group>, target: <oGit group>, role: member|maintainer}]`.

They apply at each sign-in through the provider:

- Memberships are added, raised and removed as the user's groups change, but only the ones sync added. Memberships set by hand are never touched.
- Site admin is taken back only from admins that sync made, and never from the last active admin.

## LDAP and Active Directory

```yaml
    - slug: corp-ad
      name: Corp directory
      kind: ldap
      issuer: ldaps://ad.example.com          # ldap:// or ldaps://
      client_id: cn=ogit,ou=services,dc=example,dc=com   # optional service account
      client_secret: ${OGIT_LDAP_PASSWORD}
      admin_group: ogit-admins
      ldap:
        user_base: ou=people,dc=example,dc=com
        user_filter: ""            # default (&(objectClass=person)(|(uid={input})(mail={input})))
        username_attribute: uid    # AD: sAMAccountName
        subject_attribute: entryUUID   # AD: objectGUID
        member_of_attribute: ""    # AD: memberOf; otherwise group_base and group_filter
        group_base: ou=groups,dc=example,dc=com
        start_tls: false
        root_ca: ""                # PEM
```

- Users sign in with the normal form, with their directory user name or email. Every enabled directory is tried first, then local accounts, so a local admin still gets in when the directory is down.
- Account creation, linking by email and group sync work as for the other kinds.
- The directory password also confirms sudo. Git over HTTPS still uses access tokens.

## SAML 2.0

```yaml
    - slug: okta
      name: Okta
      kind: saml
      issuer: https://corp.okta.com/app/xyz/sso/saml/metadata   # or saml.metadata_xml
      client_secret: ${OGIT_SAML_KEY}   # the SP's private key (PEM)
      groups_claim: groups
      saml:
        certificate: |                  # the SP's certificate (PEM), with that key
          -----BEGIN CERTIFICATE-----
          …
        allow_idp_initiated: false
```

- The identity provider's metadata comes from `issuer` (fetched daily, the last good copy kept) or `saml.metadata_xml`.
- Providers added on the admin page get their key pair made for them. In the configuration file, generate one: an RSA key in PKCS#1 form (`BEGIN RSA PRIVATE KEY`) and a certificate for it:

  ```bash
  openssl genrsa -traditional -out sp.key 2048
  openssl req -x509 -key sp.key -out sp.crt -days 3650 -subj "/CN=ogit"
  ```
- Give the identity provider this server's metadata: `{http.public_url}/api/v1/auth/sso/<slug>/metadata` (also the entity ID; the assertion consumer is `…/acs`).
- Requests are signed; responses must be signed and answer the request; encrypted assertions are decrypted. User name, email, name and groups come from the usual attributes, or the ones named in `saml` (`username_attribute`, `email_attribute`, …).
- Sign-ins started at the identity provider's dashboard are refused unless `allow_idp_initiated`, and each such response is accepted once.

## Sign in only through providers

```yaml
security:
  sso_only: true
  sso_only_exempt: [root]   # keep a local admin here
```

Or turn it on from the admin page (only with an enabled provider, and only if the admin turning it on keeps a way in).

- Password and passkey sign-in, password sudo and registration are off, except for the user names in `sso_only_exempt`. That list is only read from the configuration, so a broken provider can't lock you out.
- Access tokens and SSH keys keep working, and existing sessions last until they expire.

Sign-ins, links and provider changes are written to the [audit log](./monitoring#audit-log).
