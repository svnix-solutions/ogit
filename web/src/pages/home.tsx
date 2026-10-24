import { useQuery } from "@tanstack/react-query";
import { Archive, Globe, Lock } from "lucide-react";
import { Alert } from "@/components/ui/alert";
import { Card } from "@/components/ui/card";
import { api, type RepositorySummary } from "@/lib/api";
import { useProfile } from "@/lib/auth";

export function HomePage() {
  const { data: profile } = useProfile();
  const repos = useQuery({
    queryKey: ["repositories", "mine"],
    queryFn: () => api<{ repositories: RepositorySummary[] }>("GET", "/repository"),
  });

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold">Welcome, {profile?.username}</h1>
      <section>
        <h2 className="mb-3 text-sm font-medium text-muted-foreground">Your repositories</h2>
        {repos.isPending && <p className="text-sm text-muted-foreground">Loading…</p>}
        {repos.isError && <Alert variant="destructive">Couldn't load repositories: {repos.error.message}</Alert>}
        {repos.data && repos.data.repositories.length === 0 && (
          <Card className="p-8 text-center text-sm text-muted-foreground">No repositories yet.</Card>
        )}
        {repos.data && repos.data.repositories.length > 0 && (
          <Card className="divide-y">
            {repos.data.repositories.map((r) => (
              <div key={r.id} className="flex items-start gap-3 px-4 py-3">
                {r.is_private ? <Lock className="mt-0.5 h-4 w-4 text-muted-foreground" /> : <Globe className="mt-0.5 h-4 w-4 text-muted-foreground" />}
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 font-medium">
                    {r.name}
                    {r.archived && (
                      <span className="inline-flex items-center gap-1 rounded-full border px-2 text-xs text-muted-foreground">
                        <Archive className="h-3 w-3" /> archived
                      </span>
                    )}
                  </div>
                  {r.description && <p className="truncate text-sm text-muted-foreground">{r.description}</p>}
                </div>
                <code className="hidden text-xs text-muted-foreground sm:block">/{r.slug}.git</code>
              </div>
            ))}
          </Card>
        )}
      </section>
    </div>
  );
}
