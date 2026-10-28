import { useState } from "react";
import { CopyField } from "@/components/copy-field";
import { ErrorText, Section } from "@/components/section";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select } from "@/components/ui/select";
import { useAdminProviderActions, useAdminProviders, type AdminProvider, type GroupMapping, type LDAPSettings, type ProviderFields, type SAMLSettings } from "@/lib/sso";
import { Textarea } from "@/components/ui/textarea";

const issuerLabel = { github: "GitHub URL (Enterprise only)", ldap: "Directory URL", saml: "IdP metadata URL", oidc: "Issuer URL" };
const issuerPlaceholder = {
  github: "https://github.com",
  ldap: "ldaps://ad.example.com",
  saml: "https://idp.example.com/metadata (or paste it below)",
  oidc: "https://sso.example.com/realms/main",
};

// SignInProvidersAdmin: the site's sign-in providers. Those from the
// configuration file are read-only here.
export function SignInProvidersAdmin() {
  const providers = useAdminProviders();
  const { create, update, remove } = useAdminProviderActions();
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState("");
  return (
    <section id="sign-in-providers">
      <Section
        title="Sign-in providers"
        description="OpenID Connect providers (Keycloak, Entra ID, Okta, Google…) and GitHub. New users signing in through one get an account; existing users with the same verified email confirm with their password first."
      >
        {providers.data?.map((p) => (
          <div key={p.slug} className="space-y-2 rounded-md border p-3" data-testid="sso-provider">
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <strong>{p.name}</strong>
              <code className="text-xs text-muted-foreground">{p.slug}</code>
              <Badge>{{ github: "GitHub", ldap: "LDAP", saml: "SAML", oidc: "OpenID Connect" }[p.kind]}</Badge>
              {!p.enabled && <Badge>disabled</Badge>}
              {p.from_config && <Badge>configuration file</Badge>}
            </div>
            {p.kind === "saml" && p.metadata_url && (
              <div className="space-y-1">
                <Label>For the identity provider: this server's metadata (entity ID) and assertion consumer URL</Label>
                <CopyField value={p.metadata_url} label={`SP metadata URL of ${p.name}`} />
                {p.acs_url && <CopyField value={p.acs_url} label={`ACS URL of ${p.name}`} />}
              </div>
            )}
            {p.callback_url && p.kind !== "ldap" && (
              <div className="space-y-1">
                <Label>Callback URL (register it at the provider)</Label>
                <CopyField value={p.callback_url} label={`Callback URL of ${p.name}`} />
              </div>
            )}
            {!p.from_config && (
              <div className="flex gap-2">
                <Button size="sm" variant="ghost" onClick={() => setEditing(editing === p.slug ? "" : p.slug)}>
                  {editing === p.slug ? "Close" : "Edit"}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => confirm(`Remove ${p.name}? Linked accounts can't sign in with it any more.`) && remove.mutate(p.slug)}>
                  Remove
                </Button>
              </div>
            )}
            {editing === p.slug && (
              <ProviderForm
                provider={p}
                submitLabel="Save"
                pending={update.isPending}
                error={update.error}
                onCancel={() => setEditing("")}
                onSubmit={(f) => update.mutate({ ...f, slug: p.slug }, { onSuccess: () => setEditing("") })}
              />
            )}
          </div>
        ))}
        <ErrorText error={remove.error ?? providers.error} />
        {adding ? (
          <ProviderForm
            submitLabel="Add provider"
            pending={create.isPending}
            error={create.error}
            onCancel={() => setAdding(false)}
            onSubmit={(f) => create.mutate(f as ProviderFields & { slug: string }, { onSuccess: () => setAdding(false) })}
          />
        ) : (
          <Button variant="outline" onClick={() => setAdding(true)}>
            Add a provider
          </Button>
        )}
      </Section>
    </section>
  );
}

function ProviderForm(props: {
  provider?: AdminProvider;
  submitLabel: string;
  pending: boolean;
  error: Error | null;
  onSubmit: (f: ProviderFields & { slug?: string }) => void;
  onCancel: () => void;
}) {
  const p = props.provider;
  const id = p?.slug ?? "new";
  const [slug, setSlug] = useState(p?.slug ?? "");
  const [name, setName] = useState(p?.name ?? "");
  const [kind, setKind] = useState<"oidc" | "github" | "ldap" | "saml">(p?.kind ?? "oidc");
  const [samlCfg, setSAML] = useState<SAMLSettings>(p?.saml ?? {});
  const setS = (v: Partial<SAMLSettings>) => setSAML({ ...samlCfg, ...v });
  const [ldap, setLDAP] = useState<LDAPSettings>(p?.ldap ?? { user_base: "" });
  const setL = (v: Partial<LDAPSettings>) => setLDAP({ ...ldap, ...v });
  const [issuer, setIssuer] = useState(p?.issuer ?? "");
  const [clientId, setClientId] = useState(p?.client_id ?? "");
  const [secret, setSecret] = useState("");
  const [scopes, setScopes] = useState(p?.scopes ?? "");
  const [domains, setDomains] = useState(p?.allowed_domains ?? "");
  const [orgs, setOrgs] = useState(p?.allowed_orgs ?? "");
  const [autoCreate, setAutoCreate] = useState(p?.auto_create ?? true);
  const [require2FA, setRequire2FA] = useState(p?.require_two_factor ?? false);
  const [enabled, setEnabled] = useState(p?.enabled ?? true);
  const [groupsClaim, setGroupsClaim] = useState(p?.groups_claim ?? "");
  const [adminGroup, setAdminGroup] = useState(p?.admin_group ?? "");
  const [mappings, setMappings] = useState<GroupMapping[]>(p?.group_mappings ?? []);
  const setMapping = (i: number, m: Partial<GroupMapping>) => setMappings(mappings.map((x, j) => (j === i ? { ...x, ...m } : x)));
  const field = (label: string, key: string, value: string, set: (v: string) => void, extra: Record<string, unknown> = {}) => (
    <div className="space-y-1.5">
      <Label htmlFor={`sso-${key}-${id}`}>{label}</Label>
      <Input id={`sso-${key}-${id}`} value={value} onChange={(e) => set(e.target.value)} {...extra} />
    </div>
  );
  return (
    <form
      className="space-y-3"
      onSubmit={(e) => {
        e.preventDefault();
        props.onSubmit({
          ...(p ? {} : { slug }),
          name, kind, issuer, client_id: clientId, scopes, allowed_domains: domains, allowed_orgs: kind === "github" ? orgs : "",
          auto_create: autoCreate, require_two_factor: require2FA, enabled,
          groups_claim: groupsClaim, admin_group: adminGroup, group_mappings: mappings,
          ...(secret ? { client_secret: secret } : {}),
          ...(kind === "ldap" ? { ldap } : {}),
          ...(kind === "saml" ? { saml: samlCfg } : {}),
        });
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        {!p && field("Slug (in URLs)", "slug", slug, setSlug, { required: true, placeholder: "keycloak", pattern: "[a-z0-9][a-z0-9-]*" })}
        {field("Name (on the sign-in button)", "name", name, setName, { required: true, placeholder: "Company SSO" })}
        <div className="space-y-1.5">
          <Label htmlFor={`sso-kind-${id}`}>Kind</Label>
          <Select id={`sso-kind-${id}`} className="w-full" value={kind} onChange={(e) => setKind(e.target.value as "oidc" | "github" | "ldap" | "saml")}>
            <option value="oidc">OpenID Connect</option>
            <option value="github">GitHub</option>
            <option value="ldap">LDAP / Active Directory</option>
            <option value="saml">SAML 2.0</option>
          </Select>
        </div>
        {field(issuerLabel[kind], "issuer", issuer, setIssuer, {
          required: kind !== "github" && !(kind === "saml" && samlCfg.metadata_xml),
          placeholder: issuerPlaceholder[kind],
        })}
        {kind !== "saml" && field(kind === "ldap" ? "Service account DN (optional)" : "Client ID", "client-id", clientId, setClientId, {
          required: kind !== "ldap",
          autoComplete: "off",
          placeholder: kind === "ldap" ? "cn=ogit,ou=services,dc=example,dc=com" : "",
        })}
        {kind !== "saml" && field(kind === "ldap" ? "Service account password" : "Client secret", "secret", secret, setSecret, {
          type: "password",
          autoComplete: "new-password",
          required: !p && kind !== "ldap",
          placeholder: p?.has_secret ? "(unchanged)" : "",
        })}
        {kind !== "ldap" && kind !== "saml" && field("Extra scopes (optional)", "scopes", scopes, setScopes, { placeholder: kind === "github" ? "" : "groups" })}
        {field("Allowed email domains (optional)", "domains", domains, setDomains, { placeholder: "example.com, example.org" })}
        {kind === "github" && field("Allowed organizations (optional)", "orgs", orgs, setOrgs, { placeholder: "my-org" })}
      </div>
      {kind === "saml" && (
        <fieldset className="space-y-2 rounded-md border p-3">
          <legend className="px-1 text-sm font-medium">SAML</legend>
          <p className="text-xs text-muted-foreground">
            oGit makes its signing key when you save; give the identity provider this server's metadata URL (shown once saved). Responses must be signed; encrypted
            assertions work.
          </p>
          <div className="space-y-1.5">
            <Label htmlFor={`sso-saml-xml-${id}`}>IdP metadata XML (instead of the URL)</Label>
            <Textarea id={`sso-saml-xml-${id}`} rows={3} className="font-mono text-xs" value={samlCfg.metadata_xml ?? ""} onChange={(e) => setS({ metadata_xml: e.target.value })} />
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            {field("User name attribute", "saml-username", samlCfg.username_attribute ?? "", (v) => setS({ username_attribute: v }), { placeholder: "username, uid…" })}
            {field("Email attribute", "saml-email", samlCfg.email_attribute ?? "", (v) => setS({ email_attribute: v }), { placeholder: "email, mail…" })}
            {field("Stable id attribute", "saml-subject", samlCfg.subject_attribute ?? "", (v) => setS({ subject_attribute: v }), { placeholder: "empty: the NameID" })}
          </div>
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" className="mt-1" checked={!!samlCfg.allow_idp_initiated} onChange={(e) => setS({ allow_idp_initiated: e.target.checked })} />
            <span>
              Allow sign-ins started at the identity provider
              <span className="block text-xs text-muted-foreground">From its dashboard. They can't be tied to the browser; each response is accepted once.</span>
            </span>
          </label>
        </fieldset>
      )}
      {kind === "ldap" && (
        <fieldset className="space-y-2 rounded-md border p-3">
          <legend className="px-1 text-sm font-medium">Directory</legend>
          <p className="text-xs text-muted-foreground">
            Users sign in with the normal form (user name or email), checked against the directory first. oGit's two-factor authentication still applies.
          </p>
          <div className="grid gap-3 sm:grid-cols-2">
            {field("User base DN", "user-base", ldap.user_base, (v) => setL({ user_base: v }), { required: true, placeholder: "ou=people,dc=example,dc=com" })}
            {field("User filter (optional)", "user-filter", ldap.user_filter ?? "", (v) => setL({ user_filter: v }), {
              placeholder: "(&(objectClass=person)(|(uid={input})(mail={input})))",
            })}
            {field("User name attribute", "username-attr", ldap.username_attribute ?? "", (v) => setL({ username_attribute: v }), { placeholder: "uid (AD: sAMAccountName)" })}
            {field("Email attribute", "email-attr", ldap.email_attribute ?? "", (v) => setL({ email_attribute: v }), { placeholder: "mail" })}
            {field("Stable id attribute", "subject-attr", ldap.subject_attribute ?? "", (v) => setL({ subject_attribute: v }), {
              placeholder: "entryUUID (AD: objectGUID); empty: the DN",
            })}
            {field("Groups from attribute", "member-of", ldap.member_of_attribute ?? "", (v) => setL({ member_of_attribute: v }), { placeholder: "memberOf (AD)" })}
            {field("Or group base DN", "group-base", ldap.group_base ?? "", (v) => setL({ group_base: v }), { placeholder: "ou=groups,dc=example,dc=com" })}
          </div>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={!!ldap.start_tls} onChange={(e) => setL({ start_tls: e.target.checked })} /> StartTLS (ldap:// URLs)
          </label>
          <div className="space-y-1.5">
            <Label htmlFor={`sso-root-ca-${id}`}>CA certificate (optional, PEM)</Label>
            <Textarea id={`sso-root-ca-${id}`} rows={3} className="font-mono text-xs" value={ldap.root_ca ?? ""} onChange={(e) => setL({ root_ca: e.target.value })} />
          </div>
        </fieldset>
      )}
      <fieldset className="space-y-2 rounded-md border p-3">
        <legend className="px-1 text-sm font-medium">Group sync</legend>
        <p className="text-xs text-muted-foreground">
          At each sign-in. Sync only removes the memberships (and admin role) it added; ones set by hand stay.
          {kind === "github" && " GitHub's groups are the user's organizations (listed only with allowed organizations)."}
        </p>
        <div className="grid gap-3 sm:grid-cols-2">
          {kind === "oidc" && field("Groups claim", "groups-claim", groupsClaim, setGroupsClaim, { placeholder: "groups (or realm_access.roles)" })}
          {kind === "saml" && field("Groups attribute", "groups-claim", groupsClaim, setGroupsClaim, { placeholder: "groups, memberOf…" })}
          {field("Admin group", "admin-group", adminGroup, setAdminGroup, { placeholder: "Members are site admins" })}
        </div>
        {mappings.map((m, i) => (
          <div key={i} className="flex flex-wrap items-end gap-2" data-testid="group-mapping">
            <Input aria-label="Provider group" className="w-40" placeholder="Provider group" value={m.group} onChange={(e) => setMapping(i, { group: e.target.value })} required />
            <span className="pb-2 text-sm">→</span>
            <Input aria-label="oGit group" className="w-40" placeholder="oGit group" value={m.target} onChange={(e) => setMapping(i, { target: e.target.value })} required />
            <Select aria-label="Role" value={m.role} onChange={(e) => setMapping(i, { role: e.target.value as GroupMapping["role"] })}>
              <option value="member">member</option>
              <option value="maintainer">maintainer</option>
            </Select>
            <Button type="button" variant="ghost" size="sm" onClick={() => setMappings(mappings.filter((_, j) => j !== i))}>
              Remove
            </Button>
          </div>
        ))}
        <Button type="button" variant="outline" size="sm" onClick={() => setMappings([...mappings, { group: "", target: "", role: "member" }])}>
          Map a group
        </Button>
      </fieldset>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={autoCreate} onChange={(e) => setAutoCreate(e.target.checked)} /> Create accounts for new users
      </label>
      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" className="mt-1" checked={require2FA || kind === "ldap"} disabled={kind === "ldap"} onChange={(e) => setRequire2FA(e.target.checked)} />
        <span>
          Ask for oGit's two-factor authentication too
          <span className="block text-xs text-muted-foreground">Otherwise the provider's own MFA is trusted.</span>
        </span>
      </label>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} /> Enabled
      </label>
      <div className="flex gap-2">
        <Button type="submit" disabled={props.pending}>
          {props.submitLabel}
        </Button>
        <Button type="button" variant="ghost" onClick={props.onCancel}>
          Cancel
        </Button>
      </div>
      <ErrorText error={props.error} />
    </form>
  );
}
