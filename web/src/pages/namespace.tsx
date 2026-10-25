import { useQuery } from "@tanstack/react-query";
import { Plus, Settings, User, Users } from "lucide-react";
import { useParams, useSearchParams } from "react-router";
import { pageParam, Pager } from "@/components/pager";
import { RepoList } from "@/components/repo-list";
import { Alert } from "@/components/ui/alert";
import { ButtonLink } from "@/components/ui/button";
import { api, ApiError, type NamespaceView } from "@/lib/api";
import { useProfile } from "@/lib/auth";
import { useMyGroups } from "@/lib/queries";
import { NotFoundPage } from "./not-found";

export function NamespacePage() {
  const { owner = "" } = useParams();
  const { data: profile } = useProfile();
  const [params, setParams] = useSearchParams();
  const page = pageParam(params);
  const perPage = 30;
  const ns = useQuery({
    queryKey: ["namespace", owner, page],
    queryFn: () => api<NamespaceView>("GET", `/namespace/${encodeURIComponent(owner)}?limit=${perPage}&offset=${page * perPage}`),
    retry: false,
  });
  const groups = useMyGroups(!!profile && ns.data?.type === "group");
  const membership = groups.data?.find((g) => g.name === owner);

  if (ns.error instanceof ApiError && ns.error.status === 404) return <NotFoundPage />;
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
          <h1 className="text-2xl font-semibold">{ns.data.name}</h1>
          <p className="text-sm text-muted-foreground">{ns.data.description || (ns.data.type === "group" ? "Group" : "User")}</p>
        </div>
        <div className="flex gap-2">
          {membership && (
            <ButtonLink variant="outline" size="sm" to={`/groups/${owner}`}>
              <Settings className="h-4 w-4" /> Manage group
            </ButtonLink>
          )}
          {ns.data.can_create && (
            <ButtonLink size="sm" to={`/new?namespace=${encodeURIComponent(owner)}`}>
              <Plus className="h-4 w-4" /> New repository
            </ButtonLink>
          )}
        </div>
      </div>
      <RepoList repos={ns.data.repositories} showNamespace={false} empty="No repositories you can see here." />
      <Pager page={page} perPage={perPage} count={ns.data.repositories.length} total={ns.data.total} onPage={(p) => setParams({ page: String(p + 1) })} />
    </div>
  );
}
