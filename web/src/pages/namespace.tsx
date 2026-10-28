import { useQuery } from "@tanstack/react-query";
import { BookMarked, Folder, FolderPlus, Package, Plus, Settings, User, Users } from "lucide-react";
import { Link, useParams, useSearchParams } from "react-router";
import { pageParam, Pager } from "@/components/pager";
import { NamespacePath } from "@/components/namespace-path";
import { PackageList } from "@/components/package-list";
import { RepoList } from "@/components/repo-list";
import { Alert } from "@/components/ui/alert";
import { ButtonLink } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Select } from "@/components/ui/select";
import { api, ApiError, type NamespaceView } from "@/lib/api";
import { useProfile } from "@/lib/auth";
import { useNamespacePackages } from "@/lib/packages";
import { useMyGroups } from "@/lib/queries";
import { cn } from "@/lib/utils";
import { NotFoundPage } from "./not-found";

// NamespacePage: a user's or group's page; path is given for sub-groups
// (their routes are the repository routes', see RepoLayout).
export function NamespacePage({ path, notFound }: { path?: string; notFound?: React.ReactNode } = {}) {
  const params = useParams();
  const owner = path ?? params.owner ?? params.p0 ?? "";
  const { data: profile } = useProfile();
  const [search, setParams] = useSearchParams();
  const page = pageParam(search);
  const tab = search.get("tab");
  const perPage = 30;
  const ns = useQuery({
    queryKey: ["namespace", owner, page],
    queryFn: () => api<NamespaceView>("GET", `/namespace/${encodeURIComponent(owner)}?limit=${perPage}&offset=${page * perPage}`),
    retry: false,
  });
  const groups = useMyGroups(!!profile && ns.data?.type === "group");
  const membership = groups.data?.find((g) => g.name === owner);

  if (ns.error instanceof ApiError && ns.error.status === 404) return notFound ?? <NotFoundPage />;
  if (ns.isError) return <Alert variant="destructive">{ns.error.message}</Alert>;
  if (!ns.data) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const Icon = ns.data.type === "group" ? Users : User;
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start gap-4">
        <div className="flex h-14 w-14 items-center justify-center rounded-full bg-muted">
          <Icon className="h-7 w-7 text-muted-foreground" />
        </div>
        <div className="min-w-0 flex-1">
          <h1 className="text-2xl font-semibold">{ns.data.name.includes("/") ? <NamespacePath path={ns.data.name} /> : ns.data.name}</h1>
          <p className="text-sm text-muted-foreground">{ns.data.description || (ns.data.type === "group" ? "Group" : "User")}</p>
        </div>
        <div className="flex gap-2">
          {membership && (
            <ButtonLink variant="outline" size="sm" to={`/groups/${ns.data.name}`}>
              <Settings className="h-4 w-4" /> Manage group
            </ButtonLink>
          )}
          {ns.data.can_create && ns.data.type === "group" && (
            <ButtonLink variant="outline" size="sm" to={`/groups/${ns.data.name}#subgroups`}>
              <FolderPlus className="h-4 w-4" /> New sub-group
            </ButtonLink>
          )}
          {ns.data.can_create && (
            <ButtonLink size="sm" to={`/new?namespace=${encodeURIComponent(owner)}`}>
              <Plus className="h-4 w-4" /> New repository
            </ButtonLink>
          )}
        </div>
      </div>
      <nav className="flex gap-1 border-b" aria-label="Sections">
        <button type="button" className={tabClass(tab !== "packages")} onClick={() => setParams({})}>
          <BookMarked className="h-4 w-4" /> Repositories
        </button>
        <button type="button" className={tabClass(tab === "packages")} onClick={() => setParams({ tab: "packages" })}>
          <Package className="h-4 w-4" /> Packages
        </button>
      </nav>
      {tab === "packages" ? (
        <NamespacePackages namespace={owner} type={search.get("type") ?? ""} onType={(t) => setParams(t ? { tab: "packages", type: t } : { tab: "packages" })} />
      ) : (
        <>
          {!!ns.data.subgroups?.length && (
            <Card className="divide-y" aria-label="Sub-groups">
              {ns.data.subgroups.map((g) => (
                <div key={g.name} className="flex items-start gap-3 px-4 py-3">
                  <Folder className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
                  <div className="min-w-0">
                    <Link to={`/${g.name}`} className="font-medium text-primary hover:underline">
                      {g.name.slice(g.name.lastIndexOf("/") + 1)}
                    </Link>
                    {g.description && <p className="text-sm text-muted-foreground">{g.description}</p>}
                  </div>
                </div>
              ))}
            </Card>
          )}
          <RepoList repos={ns.data.repositories} showNamespace={false} empty="No repositories you can see here." />
          <Pager page={page} perPage={perPage} count={ns.data.repositories.length} total={ns.data.total} onPage={(p) => setParams({ page: String(p + 1) })} />
        </>
      )}
    </div>
  );
}

const tabClass = (active: boolean) =>
  cn("flex items-center gap-1.5 border-b-2 px-3 py-2 text-sm", active ? "border-primary font-medium" : "border-transparent text-muted-foreground hover:text-foreground");

// NamespacePackages lists the namespace's packages, by type.
function NamespacePackages({ namespace, type, onType }: { namespace: string; type: string; onType: (t: string) => void }) {
  const list = useNamespacePackages(namespace);
  if (list.isError) return <Alert variant="destructive">{list.error.message}</Alert>;
  if (!list.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const shown = type ? list.data.filter((p) => p.type === type) : list.data;
  return (
    <div className="space-y-3">
      <Select aria-label="Package type" value={type} onChange={(e) => onType(e.target.value)} className="w-48">
        <option value="">All types</option>
        <option value="container">Container</option>
        <option value="npm">npm</option>
        <option value="generic">Generic</option>
      </Select>
      {shown.length === 0 ? (
        <Card className="p-8 text-center text-sm text-muted-foreground">No packages you can see here.</Card>
      ) : (
        <PackageList packages={shown} />
      )}
      <p className="text-xs text-muted-foreground">Go modules are served from repositories' tags: see a repository's Packages tab.</p>
    </div>
  );
}
