import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, repoId } from "./api";

// Packages (see application/packages.go): container images, npm packages
// and generic files, owned by a user or group; Go modules are served from
// repositories' tags (application/go_proxy.go).

export type PackageType = "container" | "generic" | "npm" | "go";

export type Package = {
  id: string;
  namespace: string;
  type: PackageType;
  name: string;
  visibility: "repository" | "public" | "private";
  public: boolean;
  repository: string | null;
  can_write: boolean;
  can_admin: boolean;
  created_at: string;
  updated_at: string;
};

export type PackageFile = { name: string; size: number; sha256: string; created_at: string };

export type PackageVersion = {
  version: string;
  size: number;
  metadata: Record<string, unknown>;
  publisher: string | null;
  files: PackageFile[];
  tags: string[];
  created_at: string;
};

// The UI's path of a package; image names may have slashes.
export const packagePath = (p: { namespace: string; type: string; name: string }) => `/packages/${p.namespace}/${p.type}/${p.name}`;
const apiPath = (namespace: string, type: string, name: string) =>
  `/packages/${encodeURIComponent(namespace)}/${encodeURIComponent(type)}/${encodeURIComponent(name)}`;

// npmScope is a package's npm scope: the top of its namespace (sub-groups
// publish in their top-level group's scope).
export const npmScope = (p: { namespace: string }) => p.namespace.split("/")[0];

// fileURL downloads a generic file or an npm tarball (the session cookie
// authenticates it).
export const fileURL = (p: Package, version: string, file: string) =>
  p.type === "npm"
    ? `/api/packages/npm/@${encodeURIComponent(npmScope(p))}/${encodeURIComponent(p.name)}/-/${encodeURIComponent(file)}`
    : `/api/packages/${p.namespace.split("/").map(encodeURIComponent).join("/")}/generic/${encodeURIComponent(p.name)}/${encodeURIComponent(version)}/${encodeURIComponent(file)}`;

export function useNamespacePackages(namespace: string) {
  return useQuery({
    queryKey: ["packages", "namespace", namespace],
    queryFn: async () => (await api<{ packages: Package[] }>("GET", `/packages/${encodeURIComponent(namespace)}`)).packages,
    retry: false,
  });
}

export function useRepoPackages(fullPath: string) {
  return useQuery({
    queryKey: ["packages", "repository", fullPath],
    queryFn: async () => (await api<{ packages: Package[] }>("GET", `/repository/${repoId(fullPath)}/packages`)).packages,
    retry: false,
  });
}

export function usePackage(namespace: string, type: string, name: string) {
  return useQuery({
    queryKey: ["packages", "detail", namespace, type, name],
    queryFn: () => api<{ package: Package; versions: PackageVersion[]; readme: string }>("GET", apiPath(namespace, type, name)),
    retry: false,
  });
}

export function useUpdatePackage(p: Package) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (change: { visibility?: string; repository?: string }) => api<Package>("PATCH", apiPath(p.namespace, p.type, p.name), change),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["packages"] }),
  });
}

export function useDeleteVersion(p: Package) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (version: string) => api("DELETE", `${apiPath(p.namespace, p.type, p.name)}/versions/${encodeURIComponent(version)}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["packages"] }),
  });
}

export function useDeletePackage(p: Package) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api("DELETE", `${apiPath(p.namespace, p.type, p.name)}?confirm=${encodeURIComponent(p.name)}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["packages"] }),
  });
}

// The version npm installs by default (dist-tag latest), else the newest.
export const latestVersion = (versions: PackageVersion[]) => versions.find((v) => v.tags.includes("latest")) ?? versions[0];

// npmManifest is a version's package.json as published.
export const npmManifest = (v?: PackageVersion) => (v?.metadata.manifest ?? {}) as Record<string, unknown>;

// npmrc maps the package's scope to this registry.
export const npmrc = (p: Package, origin: string) => [`@${npmScope(p)}:registry=${origin}/api/packages/npm/`, `${origin.replace(/^https?:/, "")}/api/packages/npm/:_authToken=<token>`];

// Install commands for a package, as shown on its page.
export function installCommand(p: Package, host: string, version?: PackageVersion): string {
  if (p.type === "npm") return `npm install @${npmScope(p)}/${p.name}${version ? "@" + version.version : ""}`;
  if (p.type === "container") {
    const ref = version?.tags[0] ? `:${version.tags[0]}` : version ? `@${version.version}` : "";
    return `docker pull ${host}/${p.namespace}/${p.name}${ref}`;
  }
  const v = version?.version ?? "<version>";
  const f = version?.files[0]?.name ?? "<file>";
  return `curl -fLO -u <user>:<token> ${location.protocol}//${host}/api/packages/${p.namespace}/generic/${p.name}/${v}/${f}`;
}

export type GoModule = { path: string; versions: { version: string; time: string }[]; requires: { path: string; version: string; indirect: boolean }[] };

// useGoModule: the module a repository serves through the Go proxy (404
// when it has none: data stays undefined). summary only checks that there
// is one (no versions or requirements; cheap, for the tab).
export function useGoModule(fullPath: string, summary = false) {
  return useQuery({
    queryKey: ["packages", "go", fullPath, summary],
    queryFn: () => api<GoModule>("GET", `/repository/${repoId(fullPath)}/go-module${summary ? "?summary=true" : ""}`),
    retry: false,
  });
}
