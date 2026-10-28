import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./api";

// Single sign-on (application/sso_app.go): providers on the sign-in page,
// a user's linked providers, and the admins' providers.

export type SSOProvider = { slug: string; name: string; kind: "oidc" | "github" | "ldap" | "saml" };

// SAMLSettings: a SAML identity provider (its metadata URL is the issuer).
export type SAMLSettings = {
  metadata_xml?: string;
  allow_idp_initiated?: boolean;
  username_attribute?: string;
  email_attribute?: string;
  name_attribute?: string;
  subject_attribute?: string;
};

// LDAPSettings: how a directory finds users and their groups.
export type LDAPSettings = {
  user_base: string;
  user_filter?: string;
  username_attribute?: string;
  email_attribute?: string;
  name_attribute?: string;
  subject_attribute?: string;
  member_of_attribute?: string;
  group_base?: string;
  group_filter?: string;
  group_name_attribute?: string;
  start_tls?: boolean;
  root_ca?: string;
  insecure_skip_verify?: boolean;
};

export type Identity = {
  id: string;
  provider: string;
  provider_name: string;
  email: string;
  username: string;
  last_login_at: string | null;
  created_at: string;
};

export type AdminProvider = SSOProvider & {
  issuer: string;
  client_id: string;
  has_secret: boolean;
  scopes: string;
  allowed_domains: string;
  allowed_orgs: string;
  auto_create: boolean;
  require_two_factor: boolean;
  enabled: boolean;
  from_config: boolean;
  callback_url: string;
  groups_claim: string;
  admin_group: string;
  group_mappings: GroupMapping[];
  ldap?: LDAPSettings;
  saml?: SAMLSettings;
  // SAML: this server's metadata (entity id) and assertion consumer URLs
  metadata_url?: string;
  acs_url?: string;
};

// GroupMapping: members of a provider's group get an oGit group's role.
export type GroupMapping = { group: string; target: string; role: "member" | "maintainer" };

export type ProviderFields = Partial<Omit<AdminProvider, "has_secret" | "from_config" | "callback_url">> & { client_secret?: string };

// ssoStart is where the browser goes to sign in (or link, or confirm sudo)
// through a provider; it comes back to redirect.
export function ssoStart(slug: string, redirect: string, purpose?: "link" | "sudo") {
  const q = new URLSearchParams({ redirect });
  if (purpose) q.set("purpose", purpose);
  return `/api/v1/auth/sso/${encodeURIComponent(slug)}/start?${q}`;
}

export function useSSOProviders() {
  return useQuery({
    queryKey: ["sso-providers"],
    queryFn: () => api<{ providers: SSOProvider[] }>("GET", "/auth/providers").then((r) => r.providers),
    staleTime: 60_000,
  });
}

export function useIdentities(enabled = true) {
  return useQuery({
    queryKey: ["identities"],
    queryFn: () => api<{ identities: Identity[] }>("GET", "/user/identities").then((r) => r.identities),
    enabled,
  });
}

export function useUnlink() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (id: string) => api("DELETE", `/user/identities/${id}`), onSuccess: () => qc.invalidateQueries({ queryKey: ["identities"] }) });
}

export function useAdminProviders() {
  return useQuery({
    queryKey: ["admin-sso-providers"],
    queryFn: () => api<{ providers: AdminProvider[] }>("GET", "/admin/sso/providers").then((r) => r.providers),
  });
}

export function useAdminProviderActions() {
  const qc = useQueryClient();
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["admin-sso-providers"] });
    qc.invalidateQueries({ queryKey: ["sso-providers"] });
  };
  return {
    create: useMutation({ mutationFn: (f: ProviderFields & { slug: string }) => api<AdminProvider>("POST", "/admin/sso/providers", f), onSuccess: refresh }),
    update: useMutation({ mutationFn: ({ slug, ...f }: ProviderFields & { slug: string }) => api<AdminProvider>("PATCH", `/admin/sso/providers/${slug}`, f), onSuccess: refresh }),
    remove: useMutation({ mutationFn: (slug: string) => api("DELETE", `/admin/sso/providers/${slug}`), onSuccess: refresh }),
  };
}
