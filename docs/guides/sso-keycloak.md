# Sign in with Keycloak

This guide connects oGit to a Keycloak realm with OpenID Connect. Keycloak users sign in with a button on oGit's sign-in page. Their Keycloak groups become oGit group memberships, and members of `ogit-admins` become site admins.

You need:

- oGit with `http.public_url` and `security.encryption_key` set; here, `https://git.example.com`;
- a Keycloak realm; here, `https://sso.example.com/realms/main`.

## 1. Create the client in Keycloak

In the realm, open **Clients → Create client**:

1. **Client type:** OpenID Connect. **Client ID:** `ogit`.
2. **Client authentication:** on. **Standard flow:** on (leave the others off).
3. **Valid redirect URIs:** `https://git.example.com/api/v1/auth/sso/keycloak/callback`.
4. Save, then copy the secret from the **Credentials** tab.

`keycloak` in the redirect URI is the provider's slug in oGit; use the same one in step 3.

## 2. Send the groups

oGit reads groups from the `groups` claim. In the client, open **Client scopes → ogit-dedicated → Add mapper → By configuration → Group Membership**:

- **Name** and **Token Claim Name:** `groups`;
- **Full group path:** off (so groups are `engineering`, not `/engineering`);
- **Add to ID token** and **Add to userinfo:** on.

Create the groups you want to sync, for example `ogit-admins` and `engineering`, and add users to them.

Users need a verified email in Keycloak (**Email verified** on the user, or the realm's email verification). oGit only creates accounts for verified emails.

## 3. Add the provider in oGit

Either on the admin page (**Admin → Sign-in providers → Add a provider**, with the same fields), or in `config.yaml`:

```yaml
sso:
  providers:
    - slug: keycloak
      name: Company SSO
      kind: oidc
      issuer: https://sso.example.com/realms/main
      client_id: ogit
      client_secret: ${OGIT_KEYCLOAK_SECRET}
      allowed_domains: "example.com"
      auto_create: true
      groups_claim: groups
      admin_group: ogit-admins
      group_mappings:
        - {group: engineering, target: eng, role: member}
```

Set `OGIT_KEYCLOAK_SECRET` to the secret from step 1 and restart oGit. The oGit group `eng` must exist; create it first.

## 4. Try it

Sign out, and click **Company SSO** on the sign-in page.

- A user who doesn't exist in oGit gets an account, named after their Keycloak user name.
- A user whose email already has an oGit account confirms with their oGit password once, and the accounts are linked.
- Members of `engineering` become members of `eng`, and members of `ogit-admins` site admins. Removing someone from the Keycloak group removes what sync granted at their next sign-in.

Keycloak's MFA is trusted: oGit doesn't ask for its own 2FA code. To require it anyway, set `require_two_factor: true` on the provider.

## Optional: Keycloak only

Once everyone signs in through Keycloak, turn off passwords, but keep a local admin in case Keycloak is down:

```yaml
security:
  sso_only: true
  sso_only_exempt: [root]
```

See [Single sign-on](/admin/sso) for every option.

## Troubleshooting

| Symptom | Check |
|---|---|
| Keycloak says "Invalid parameter: redirect_uri" | The redirect URI matches exactly, including the slug and `https`. |
| "The provider didn't share a verified email address" | **Email verified** on the Keycloak user. |
| Groups don't sync | The mapper is on the ID token, **Full group path** is off, and the oGit target group exists (oGit logs "a mapped group doesn't exist" otherwise). |
| The button is missing | `http.public_url` is set and the provider is enabled. |
