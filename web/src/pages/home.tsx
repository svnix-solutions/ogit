import { useQuery } from "@tanstack/react-query";
import { Plus, Users } from "lucide-react";
import { Link } from "react-router";
import { RepoList } from "@/components/repo-list";
import { Alert } from "@/components/ui/alert";
import { ButtonLink } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { api, type RepositorySummary } from "@/lib/api";
import { useProfile } from "@/lib/auth";
import { useMyGroups } from "@/lib/queries";

export function HomePage() {
  const { data: profile } = useProfile();
  const repos = useQuery({
    queryKey: ["repositories", "mine"],
    queryFn: async () => (await api<{ repositories: RepositorySummary[] | null }>("GET", "/repository")).repositories ?? [],
  });
  const groups = useMyGroups();

  return (
    <div className="grid gap-8 md:grid-cols-[1fr_16rem]">
      <section className="min-w-0 space-y-3">
        <div className="flex items-center justify-between gap-2">
          <h1 className="text-xl font-semibold">Repositories</h1>
          <ButtonLink size="sm" to="/new">
            <Plus className="h-4 w-4" /> New
          </ButtonLink>
        </div>
        {repos.isPending && <p className="text-sm text-muted-foreground">Loading…</p>}
        {repos.isError && <Alert variant="destructive">Couldn't load repositories: {repos.error.message}</Alert>}
        {repos.data && <RepoList repos={repos.data} empty="No repositories yet. Create one, or ask to be added to one." />}
      </section>
      <aside className="space-y-3">
        <h2 className="text-sm font-medium text-muted-foreground">Signed in as {profile?.username}</h2>
        <Card className="p-4">
          <div className="mb-2 flex items-center justify-between">
            <h3 className="flex items-center gap-1.5 text-sm font-medium">
              <Users className="h-4 w-4" /> Your groups
            </h3>
            <Link to="/groups" className="text-xs text-primary hover:underline">
              Manage
            </Link>
          </div>
          {groups.data && groups.data.length === 0 && <p className="text-sm text-muted-foreground">None yet.</p>}
          <ul className="space-y-1 text-sm">
            {groups.data?.map((g) => (
              <li key={g.id}>
                <Link to={`/${g.name}`} className="hover:underline">
                  {g.name}
                </Link>
              </li>
            ))}
          </ul>
        </Card>
      </aside>
    </div>
  );
}
