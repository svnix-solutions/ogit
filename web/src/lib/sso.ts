import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./api";

// Single sign-on (application/sso_app.go): providers on the sign-in page,
// a user's linked providers, and the admins' providers.

export type SSOProvider = { slug: string; name: string; kind: "oidc" | "github" };

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
};

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
