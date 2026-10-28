import { Archive, Globe, Lock } from "lucide-react";
import { Link } from "react-router";
import type { RepositorySummary } from "@/lib/api";
import { Badge } from "./ui/badge";
import { Card } from "./ui/card";

export function RepoList({ repos, empty, showNamespace = true }: { repos: RepositorySummary[]; empty: string; showNamespace?: boolean }) {
  if (repos.length === 0) {
    return <Card className="p-8 text-center text-sm text-muted-foreground">{empty}</Card>;
  }
  return (
    <Card className="divide-y">
      {repos.map((r) => (
        <div key={r.id} className="flex items-start gap-3 px-4 py-3">
          {r.is_private ? (
            <Lock aria-label="private" className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
          ) : (
            <Globe aria-label="public" className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
          )}
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <Link to={`/${r.full_path}`} className="font-medium text-primary hover:underline">
                {showNamespace && <span className="font-normal">{r.namespace} / </span>}
                {r.name}
              </Link>
              {r.archived && (
                <Badge>
                  <Archive className="h-3 w-3" /> archived
                </Badge>
              )}
            </div>
            {r.description && <p className="truncate text-sm text-muted-foreground">{r.description}</p>}
          </div>
        </div>
      ))}
    </Card>
  );
}
