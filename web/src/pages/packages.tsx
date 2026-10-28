import { FileArchive, Globe, Lock, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { ConfirmDelete } from "@/components/confirm-delete";
import { NamespacePath } from "@/components/namespace-path";
import { PackageList, PackageTypeIcon, typeLabels } from "@/components/package-list";
import { CopyField } from "@/components/copy-field";
import { useRepo } from "@/components/repo-layout";
import { ErrorText, Section } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select } from "@/components/ui/select";
import { ApiError } from "@/lib/api";
import { formatSize, relativeTime } from "@/lib/browse";
import { renderMarkdown } from "@/lib/markdown";
import {
  fileURL,
  installCommand,
  latestVersion,
  npmManifest,
  npmrc,
  useDeletePackage,
  useDeleteVersion,
  useGoModule,
  usePackage,
  useRepoPackages,
  useUpdatePackage,
  type GoModule,
  type Package,
  type PackageVersion,
} from "@/lib/packages";
import { NotFoundPage } from "./not-found";

// RepoPackagesPage: /:owner/:repo/packages, its Go module and the packages
// linked to it.
export function RepoPackagesPage() {
  const repo = useRepo();
  const list = useRepoPackages(repo.full_path);
  const goModule = useGoModule(repo.full_path);
  if (list.isError) return <Alert variant="destructive">{list.error.message}</Alert>;
  if (!list.data || goModule.isPending) return <p className="text-sm text-muted-foreground">Loading…</p>;
  return (
    <div className="space-y-4">
      {goModule.data && <GoModuleCard module={goModule.data} isPrivate={repo.is_private} />}
      {list.data.length > 0 ? (
        <PackageList packages={list.data} />
      ) : (
        !goModule.data && <Card className="p-8 text-center text-sm text-muted-foreground">No packages are linked to this repository.</Card>
      )}
    </div>
  );
}

// GoModuleCard: how to use the repository's Go module, its versions and
// requirements.
function GoModuleCard({ module: m, isPrivate }: { module: GoModule; isPrivate: boolean }) {
  const host = m.path.split("/")[0];
  const latest = m.versions.find((v) => !v.version.includes("-")) ?? m.versions[0];
  return (
    <Section title="Go module" description={<code className="font-mono">{m.path}</code>}>
      <div className="space-y-1">
        <p className="text-xs text-muted-foreground">
          Once: <code className="rounded bg-muted px-1">go env -w GOPRIVATE={host}</code>
          {isPrivate && (
            <>
              {" "}
              and, in <code className="rounded bg-muted px-1">~/.netrc</code>,{" "}
              <code className="rounded bg-muted px-1">machine {host} login &lt;user&gt; password &lt;token&gt;</code> (a token that reads the repository)
            </>
          )}
          . Or <code className="rounded bg-muted px-1">GOPROXY={location.origin}/api/packages/go</code>.
        </p>
        <CopyField value={`go get ${m.path}${latest ? "@" + latest.version : "@latest"}`} label="Go get command" />
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <div>
          <h3 className="mb-1 text-sm font-medium">Versions ({m.versions.length})</h3>
          {m.versions.length === 0 ? (
            <p className="text-sm text-muted-foreground">No semver tags yet: go get takes branches and commits as pseudo-versions.</p>
          ) : (
            <ul className="space-y-0.5 text-sm">
              {m.versions.slice(0, 20).map((v) => (
                <li key={v.version} className="flex gap-2">
                  <span className="font-mono">{v.version}</span>
                  <span className="text-xs text-muted-foreground">{relativeTime(v.time)}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
        <Dependencies title="Requires" deps={m.requires.filter((r) => !r.indirect).map((r) => [r.path, r.version])} />
      </div>
    </Section>
  );
}

function Dependencies({ title, deps }: { title: string; deps: [string, string][] }) {
  return (
    <div>
      <h3 className="mb-1 text-sm font-medium">
        {title} ({deps.length})
      </h3>
      {deps.length === 0 ? (
        <p className="text-sm text-muted-foreground">None.</p>
      ) : (
        <ul className="space-y-0.5 text-sm">
          {deps.map(([name, version]) => (
            <li key={name} className="flex flex-wrap gap-2">
              <span className="font-mono">{name}</span>
              <span className="font-mono text-xs text-muted-foreground">{version}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

const packageTypes = ["container", "npm", "generic", "go"];

// splitPackagePath reads <owner>/<type>/<name>, where the owner may be a
// sub-group's path and image names have slashes: the first type word after
// the owner's first segment splits it.
export function splitPackagePath(path: string) {
  const parts = path.split("/");
  const i = parts.findIndex((p, i) => i > 0 && packageTypes.includes(p));
  if (i < 0) return { owner: "", type: "", name: "" };
  return { owner: parts.slice(0, i).join("/"), type: parts[i], name: parts.slice(i + 1).join("/") };
}

// PackagePage: /packages/<owner>/<type>/<name>.
export function PackagePage() {
  const { owner, type, name } = splitPackagePath(useParams()["*"] ?? "");
  const q = usePackage(owner, type, name);
  if (q.error instanceof ApiError && q.error.status === 404) return <NotFoundPage />;
  if (q.isError) return <Alert variant="destructive">{q.error.message}</Alert>;
  if (!q.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const { package: p, versions, readme } = q.data;
  const latest = latestVersion(versions);
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <PackageTypeIcon type={p.type} className="h-6 w-6 text-muted-foreground" />
        <h1 className="text-xl">
          <NamespacePath path={p.namespace} />
          <span className="text-muted-foreground"> / </span>
          <span className="font-semibold">{p.name}</span>
        </h1>
        <Badge>{typeLabels[p.type] ?? p.type}</Badge>
        <Badge>
          {p.public ? <Globe className="h-3 w-3" /> : <Lock className="h-3 w-3" />}
          {p.public ? "public" : "private"}
        </Badge>
        {p.repository && (
          <span className="text-sm text-muted-foreground">
            from{" "}
            <Link to={`/${p.repository}`} className="text-primary hover:underline">
              {p.repository}
            </Link>
          </span>
        )}
      </div>

      <Install pkg={p} latest={latest} />
      {p.type === "npm" && latest && <NpmDetails pkg={p} version={latest} readme={readme} />}
      <Versions pkg={p} versions={versions} />
      {p.can_admin && <PackageSettings pkg={p} />}
    </div>
  );
}

// Install shows how to get (and, for writers, publish) the package.
function Install({ pkg: p, latest }: { pkg: Package; latest?: PackageVersion }) {
  const host = window.location.host;
  const origin = window.location.origin;
  let setup: React.ReactNode = null;
  let publish: React.ReactNode = null;
  switch (p.type) {
    case "container":
      setup = !p.public && (
        <p className="text-xs text-muted-foreground">
          Sign in first: <code className="rounded bg-muted px-1">docker login {host}</code> with your username and a token with read:packages.
        </p>
      );
      publish = <code className="rounded bg-muted px-1">docker push {host}/{p.namespace}/{p.name}:tag</code>;
      break;
    case "npm":
      setup = (
        <div className="space-y-1">
          <p className="text-xs text-muted-foreground">
            In <code className="rounded bg-muted px-1">.npmrc</code> (the token needs read:packages{p.can_write ? ", or write:packages to publish" : ""}):
          </p>
          {npmrc(p, origin).map((line) => (
            <CopyField key={line} value={line} label="npmrc line" />
          ))}
        </div>
      );
      publish = <code className="rounded bg-muted px-1">npm publish</code>;
      break;
    default:
      publish = (
        <code className="rounded bg-muted px-1">
          curl -u &lt;user&gt;:&lt;token&gt; --upload-file &lt;file&gt; {origin}/api/packages/{p.namespace}/generic/{p.name}/&lt;version&gt;/&lt;file&gt;
        </code>
      );
  }
  return (
    <Section title="Install">
      {setup}
      <CopyField value={installCommand(p, host, latest)} label="Install command" />
      {p.can_write && <p className="text-xs text-muted-foreground">Publish with a token that has write:packages: {publish}.</p>}
    </Section>
  );
}

// NpmDetails: the latest version's README and dependencies.
function NpmDetails({ pkg, version, readme }: { pkg: Package; version: PackageVersion; readme: string }) {
  const manifest = npmManifest(version);
  const html = useMemo(
    () => (readme ? renderMarkdown(readme, { fullPath: pkg.repository ?? `${pkg.namespace}/${pkg.name}`, ref: "HEAD", dir: "", links: !!pkg.repository }) : ""),
    [readme, pkg.repository, pkg.namespace, pkg.name],
  );
  const deps = (key: string) => Object.entries((manifest[key] ?? {}) as Record<string, string>);
  return (
    <>
      {html && (
        <Section title="README">
          <div className="markdown text-sm" dangerouslySetInnerHTML={{ __html: html }} />
        </Section>
      )}
      <Section title={`Dependencies of ${version.version}`}>
        <div className="grid gap-4 sm:grid-cols-2">
          <Dependencies title="Dependencies" deps={deps("dependencies")} />
          <Dependencies title="Peer dependencies" deps={deps("peerDependencies")} />
        </div>
      </Section>
    </>
  );
}

function Versions({ pkg, versions }: { pkg: Package; versions: PackageVersion[] }) {
  const del = useDeleteVersion(pkg);
  if (versions.length === 0) return <Card className="p-8 text-center text-sm text-muted-foreground">No versions.</Card>;
  return (
    <Section title={`Versions (${versions.length})`}>
      <ErrorText error={del.error} />
      <div className="divide-y">
        {versions.map((v) => (
          <div key={v.version} className="space-y-1 py-3 first:pt-0 last:pb-0">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-mono text-sm font-medium">{pkg.type === "container" ? v.version.slice(0, 19) : v.version}</span>
              {v.tags.map((t) => (
                <Badge key={t}>{t}</Badge>
              ))}
              {typeof npmManifest(v).deprecated === "string" && (
                <Badge className="border-amber-500 text-amber-700 dark:text-amber-400" title={npmManifest(v).deprecated as string}>
                  deprecated
                </Badge>
              )}
              <span className="text-xs text-muted-foreground">
                {formatSize(v.size)} · {v.publisher ? `by ${v.publisher} · ` : ""}
                {relativeTime(v.created_at)}
              </span>
              {pkg.can_admin && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="ml-auto"
                  aria-label={`Delete version ${v.version}`}
                  disabled={del.isPending}
                  onClick={() => confirm(`Delete version ${v.version}?`) && del.mutate(v.version)}
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              )}
            </div>
            {v.files.length > 0 && (
              <ul className="space-y-0.5">
                {v.files.map((f) => (
                  <li key={f.name} className="flex flex-wrap items-center gap-2 text-sm">
                    <FileArchive className="h-4 w-4 text-muted-foreground" />
                    <a href={fileURL(pkg, v.version, f.name)} className="text-primary hover:underline" download>
                      {f.name}
                    </a>
                    <span className="text-xs text-muted-foreground">{formatSize(f.size)}</span>
                    <span className="hidden font-mono text-xs text-muted-foreground sm:inline" title="SHA-256">
                      {f.sha256.slice(0, 12)}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        ))}
      </div>
    </Section>
  );
}

function PackageSettings({ pkg }: { pkg: Package }) {
  const update = useUpdatePackage(pkg);
  const remove = useDeletePackage(pkg);
  const navigate = useNavigate();
  const [repository, setRepository] = useState(pkg.repository ?? "");
  return (
    <>
      <Section title="Settings">
        <div className="space-y-1">
          <Label htmlFor="package-visibility">Visibility</Label>
          <Select
            id="package-visibility"
            value={pkg.visibility}
            disabled={update.isPending}
            onChange={(e) => update.mutate({ visibility: e.target.value })}
          >
            {pkg.repository && <option value="repository">Same as {pkg.repository}</option>}
            <option value="private">Private</option>
            <option value="public">Public</option>
          </Select>
        </div>
        <form
          className="space-y-1"
          onSubmit={(e) => {
            e.preventDefault();
            update.mutate({ repository: repository.trim() });
          }}
        >
          <Label htmlFor="package-repository">Linked repository</Label>
          <p className="text-xs text-muted-foreground">
            A repository of {pkg.namespace}: its writers publish, its readers read, and its Actions jobs use it. Empty to unlink.
          </p>
          <div className="flex gap-2">
            <Input id="package-repository" value={repository} placeholder={`${pkg.namespace}/repository`} onChange={(e) => setRepository(e.target.value)} />
            <Button type="submit" variant="outline" disabled={update.isPending}>
              Save
            </Button>
          </div>
        </form>
        <ErrorText error={update.error} />
      </Section>
      <Section title="Delete this package" danger description="Every version goes. This can't be undone.">
        <ConfirmDelete word={pkg.name} action="Delete package" pending={remove.isPending} onConfirm={() => remove.mutate(undefined, { onSuccess: () => navigate(`/${pkg.namespace}`) })} />
        <ErrorText error={remove.error} />
      </Section>
    </>
  );
}
