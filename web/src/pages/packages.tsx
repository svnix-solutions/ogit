import { Box, FileArchive, Globe, Lock, Package as PackageIcon, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { ConfirmDelete } from "@/components/confirm-delete";
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
import {
  fileURL,
  installCommand,
  packagePath,
  useDeletePackage,
  useDeleteVersion,
  usePackage,
  useRepoPackages,
  useUpdatePackage,
  type Package,
  type PackageVersion,
} from "@/lib/packages";
import { NotFoundPage } from "./not-found";

const typeLabels: Record<string, string> = { container: "Container", generic: "Generic", npm: "npm", go: "Go" };

export function PackageTypeIcon({ type, className }: { type: string; className?: string }) {
  return type === "container" ? <Box aria-label="container" className={className} /> : <PackageIcon aria-label={type} className={className} />;
}

// PackageList lists packages with their type and visibility.
export function PackageList({ packages, showNamespace }: { packages: Package[]; showNamespace?: boolean }) {
  return (
    <Card className="divide-y">
      {packages.map((p) => (
        <div key={p.id} className="flex items-start gap-3 px-4 py-3">
          <PackageTypeIcon type={p.type} className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <Link to={packagePath(p)} className="font-medium text-primary hover:underline">
                {showNamespace && <span className="font-normal">{p.namespace} / </span>}
                {p.name}
              </Link>
              <Badge>{typeLabels[p.type] ?? p.type}</Badge>
              {!p.public && (
                <Badge>
                  <Lock className="h-3 w-3" /> private
                </Badge>
              )}
            </div>
            <p className="text-xs text-muted-foreground">
              {p.repository && <>from {p.repository} · </>}updated {relativeTime(p.updated_at)}
            </p>
          </div>
        </div>
      ))}
    </Card>
  );
}

// RepoPackagesPage: /:owner/:repo/packages, the packages linked to it.
export function RepoPackagesPage() {
  const repo = useRepo();
  const list = useRepoPackages(repo.full_path);
  if (list.isError) return <Alert variant="destructive">{list.error.message}</Alert>;
  if (!list.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (list.data.length === 0) return <Card className="p-8 text-center text-sm text-muted-foreground">No packages are linked to this repository.</Card>;
  return <PackageList packages={list.data} />;
}

// PackagePage: /packages/:owner/:type/<name>.
export function PackagePage() {
  const { owner = "", type = "", "*": name = "" } = useParams();
  const q = usePackage(owner, type, name);
  if (q.error instanceof ApiError && q.error.status === 404) return <NotFoundPage />;
  if (q.isError) return <Alert variant="destructive">{q.error.message}</Alert>;
  if (!q.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const { package: p, versions } = q.data;
  const host = window.location.host;
  const latest = versions[0];
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <PackageTypeIcon type={p.type} className="h-6 w-6 text-muted-foreground" />
        <h1 className="text-xl">
          <Link to={`/${p.namespace}`} className="text-primary hover:underline">
            {p.namespace}
          </Link>
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

      <Section title="Install" description={p.public ? undefined : "Sign in first: docker login " + host + " with your username and a token with read:packages."}>
        <CopyField value={installCommand(p, host, latest)} label="Install command" />
        {p.can_write && (
          <p className="text-xs text-muted-foreground">
            Publish with a token that has write:packages
            {p.type === "container" ? (
              <>
                : <code className="rounded bg-muted px-1">docker push {host}/{p.namespace}/{p.name}:tag</code>
              </>
            ) : (
              <>
                : <code className="rounded bg-muted px-1">curl -u &lt;user&gt;:&lt;token&gt; --upload-file &lt;file&gt; {location.origin}/api/packages/{p.namespace}/generic/{p.name}/&lt;version&gt;/&lt;file&gt;</code>
              </>
            )}
            .
          </p>
        )}
      </Section>

      <Versions pkg={p} versions={versions} />
      {p.can_admin && <PackageSettings pkg={p} />}
    </div>
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
