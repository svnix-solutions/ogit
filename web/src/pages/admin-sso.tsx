import { useState } from "react";
import { CopyField } from "@/components/copy-field";
import { ErrorText, Section } from "@/components/section";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select } from "@/components/ui/select";
import { useAdminProviderActions, useAdminProviders, type AdminProvider, type ProviderFields } from "@/lib/sso";

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
              <Badge>{p.kind === "github" ? "GitHub" : "OpenID Connect"}</Badge>
              {!p.enabled && <Badge>disabled</Badge>}
              {p.from_config && <Badge>configuration file</Badge>}
            </div>
            {p.callback_url && (
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
  const [kind, setKind] = useState<"oidc" | "github">(p?.kind ?? "oidc");
  const [issuer, setIssuer] = useState(p?.issuer ?? "");
  const [clientId, setClientId] = useState(p?.client_id ?? "");
  const [secret, setSecret] = useState("");
  const [scopes, setScopes] = useState(p?.scopes ?? "");
  const [domains, setDomains] = useState(p?.allowed_domains ?? "");
  const [orgs, setOrgs] = useState(p?.allowed_orgs ?? "");
  const [autoCreate, setAutoCreate] = useState(p?.auto_create ?? true);
  const [require2FA, setRequire2FA] = useState(p?.require_two_factor ?? false);
  const [enabled, setEnabled] = useState(p?.enabled ?? true);
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
          ...(secret ? { client_secret: secret } : {}),
        });
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        {!p && field("Slug (in URLs)", "slug", slug, setSlug, { required: true, placeholder: "keycloak", pattern: "[a-z0-9][a-z0-9-]*" })}
        {field("Name (on the sign-in button)", "name", name, setName, { required: true, placeholder: "Company SSO" })}
        <div className="space-y-1.5">
          <Label htmlFor={`sso-kind-${id}`}>Kind</Label>
          <Select id={`sso-kind-${id}`} className="w-full" value={kind} onChange={(e) => setKind(e.target.value as "oidc" | "github")}>
            <option value="oidc">OpenID Connect</option>
            <option value="github">GitHub</option>
          </Select>
        </div>
        {field(kind === "github" ? "GitHub URL (Enterprise only)" : "Issuer URL", "issuer", issuer, setIssuer, {
          required: kind === "oidc",
          placeholder: kind === "github" ? "https://github.com" : "https://sso.example.com/realms/main",
        })}
        {field("Client ID", "client-id", clientId, setClientId, { required: true, autoComplete: "off" })}
        {field("Client secret", "secret", secret, setSecret, {
          type: "password",
          autoComplete: "new-password",
          required: !p,
          placeholder: p?.has_secret ? "(unchanged)" : "",
        })}
        {field("Extra scopes (optional)", "scopes", scopes, setScopes, { placeholder: kind === "github" ? "" : "groups" })}
        {field("Allowed email domains (optional)", "domains", domains, setDomains, { placeholder: "example.com, example.org" })}
        {kind === "github" && field("Allowed organizations (optional)", "orgs", orgs, setOrgs, { placeholder: "my-org" })}
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={autoCreate} onChange={(e) => setAutoCreate(e.target.checked)} /> Create accounts for new users
      </label>
      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" className="mt-1" checked={require2FA} onChange={(e) => setRequire2FA(e.target.checked)} />
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
