import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { useEffect, useState } from "react";
import { useSearchParams } from "react-router";
import { pageParam, Pager } from "@/components/pager";
import { RepoList } from "@/components/repo-list";
import { Alert } from "@/components/ui/alert";
import { Input } from "@/components/ui/input";
import { api, type RepositorySummary } from "@/lib/api";

export function ExplorePage() {
  const [params, setParams] = useSearchParams();
  const q = params.get("q") ?? "";
  const page = pageParam(params);
  const perPage = 30;
  const [text, setText] = useState(q);

  // Search as you type, a moment after typing stops.
  useEffect(() => {
    if (text === q) return;
    const t = setTimeout(() => setParams(text ? { q: text } : {}, { replace: true }), 250); // a new search starts at page 1
    return () => clearTimeout(t);
  }, [text, q, setParams]);

  const repos = useQuery({
    queryKey: ["explore", q, page],
    queryFn: () => api<{ repositories: RepositorySummary[]; total: number }>("GET", `/explore?limit=${perPage}&offset=${page * perPage}&q=${encodeURIComponent(q)}`),
    placeholderData: keepPreviousData,
  });

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Explore public repositories</h1>
      <div className="relative">
        <Search className="pointer-events-none absolute top-2.5 left-3 h-4 w-4 text-muted-foreground" />
        <Input className="pl-9" placeholder="Search by name or description" aria-label="Search" value={text} onChange={(e) => setText(e.target.value)} />
      </div>
      {repos.isError && <Alert variant="destructive">{repos.error.message}</Alert>}
      {repos.data && (
        <>
          <p className="text-sm text-muted-foreground">
            {repos.data.total} {repos.data.total === 1 ? "repository" : "repositories"}
          </p>
          <RepoList repos={repos.data.repositories} empty={q ? "Nothing matches." : "No public repositories yet."} />
          <Pager
            page={page}
            perPage={perPage}
            count={repos.data.repositories.length}
            total={repos.data.total}
            onPage={(p) => setParams({ ...(q ? { q } : {}), page: String(p + 1) })}
          />
        </>
      )}
    </div>
  );
}
