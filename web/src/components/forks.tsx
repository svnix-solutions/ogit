import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, GitFork, RefreshCw } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { ErrorText } from "@/components/section";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select } from "@/components/ui/select";
import { api, atLeast, type Repository, type RepositorySummary } from "@/lib/api";
import { useProfile } from "@/lib/auth";
import { useMyGroups } from "@/lib/queries";
import { watchLevels, type WatchLevel } from "@/lib/notifications";

// Forking: the Fork button, "forked from", and syncing a fork.

export function useForks(repoId: string) {
  return useQuery({
    queryKey: ["forks", repoId],
    queryFn: async () => (await api<{ forks: RepositorySummary[] }>("GET", `/repository/${repoId}/forks`)).forks,
  });
}

export function ForkedFrom({ repo }: { repo: Repository }) {
  if (!repo.is_fork) return null;
  return (
    <p className="flex items-center gap-1 text-sm text-muted-foreground">
      <GitFork className="h-3.5 w-3.5" /> forked from{" "}
      {repo.fork_of ? (
        <Link to={`/${repo.fork_of.full_path}`} className="text-primary hover:underline">
          {repo.fork_of.full_path}
        </Link>
      ) : (
        "a repository you can't see"
      )}
    </p>
  );
}

// WatchSelect sets the caller's watch level on a repository.
export function WatchSelect({ repo }: { repo: Repository }) {
  const profile = useProfile();
  const qc = useQueryClient();
  const key = ["watch", repo.id];
  const watch = useQuery({
    queryKey: key,
    queryFn: async () => (await api<{ level: WatchLevel }>("GET", `/repository/${repo.id}/watch`)).level,
    enabled: !!profile.data,
  });
  const set = useMutation({
    mutationFn: (level: WatchLevel) => api<{ level: WatchLevel }>("PUT", `/repository/${repo.id}/watch`, { level }),
    onSuccess: (r) => qc.setQueryData(key, r.level),
  });
  if (!profile.data || !watch.data) return null;
  return (
    <Select
      aria-label="Watch"
      className="h-8 text-xs"
      value={watch.data}
      title={watchLevels.find((w) => w.level === watch.data)?.hint}
      disabled={set.isPending}
      onChange={(e) => set.mutate(e.target.value as WatchLevel)}
    >
      {watchLevels.map((w) => (
        <option key={w.level} value={w.level} title={w.hint}>
          {w.level === "participating" ? "Watch: participating" : w.level === "ignore" ? "Ignoring" : `Watching: ${w.label.toLowerCase()}`}
        </option>
      ))}
    </Select>
  );
}

export function ForkButton({ repo }: { repo: Repository }) {
  const profile = useProfile();
  const groups = useMyGroups(!!profile.data);
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [namespace, setNamespace] = useState("");
  const [name, setName] = useState(repo.name);
  const fork = useMutation({
    mutationFn: () => api<Repository>("POST", `/repository/${repo.id}/forks`, { namespace, name }),
    onSuccess: (f) => navigate(`/${f.full_path}`),
  });
  if (!profile.data) return null;
  const targets = (groups.data ?? []).filter((g) => g.role === "owner" || g.role === "maintainer");
  return (
    <div className="relative">
      <Button size="sm" variant="outline" onClick={() => setOpen(!open)} aria-expanded={open}>
        <GitFork className="h-4 w-4" /> Fork <ChevronDown className="h-3 w-3" />
      </Button>
      {open && (
        <Card className="absolute right-0 z-10 mt-2 w-[min(22rem,90vw)] space-y-3 p-4 shadow-lg">
          <div className="space-y-1">
            <Label htmlFor="fork-owner">Owner</Label>
            <Select id="fork-owner" value={namespace} onChange={(e) => setNamespace(e.target.value)} className="w-full">
              <option value="">{profile.data.username}</option>
              {targets.map((g) => (
                <option key={g.id} value={g.name}>
                  {g.name}
                </option>
              ))}
            </Select>
          </div>
          <div className="space-y-1">
            <Label htmlFor="fork-name">Name</Label>
            <Input id="fork-name" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <p className="text-xs text-muted-foreground">
            A copy of every branch and tag.{repo.is_private && " The fork is private, like the original."}
          </p>
          <ErrorText error={fork.error} />
          <Button onClick={() => fork.mutate()} disabled={!name.trim() || fork.isPending}>
            {fork.isPending ? "Forking…" : "Create fork"}
          </Button>
          <Link to={`/${repo.full_path}/forks`} className="block text-xs text-primary hover:underline" onClick={() => setOpen(false)}>
            See this repository's forks
          </Link>
        </Card>
      )}
    </div>
  );
}

export function SyncButton({ repo }: { repo: Repository }) {
  const qc = useQueryClient();
  const [message, setMessage] = useState("");
  const sync = useMutation({
    mutationFn: () => api<{ updated: boolean; branch: string }>("POST", `/repository/${repo.id}/sync`, {}),
    onSuccess: (r) => {
      setMessage(r.updated ? `${r.branch} is now up to date with ${repo.fork_of?.full_path}.` : `${r.branch} was already up to date.`);
      qc.invalidateQueries();
    },
    onError: () => setMessage(""),
  });
  if (!repo.fork_of || !atLeast(repo.role, "developer") || repo.archived) return null;
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <Button size="sm" variant="outline" onClick={() => sync.mutate()} disabled={sync.isPending}>
        <RefreshCw className="h-4 w-4" /> Sync fork
      </Button>
      {message && <span className="text-muted-foreground">{message}</span>}
      <ErrorText error={sync.error} />
    </div>
  );
}

export function ForksPage({ repo }: { repo: Repository }) {
  const forks = useForks(repo.id);
  return (
    <div className="space-y-3">
      <h2 className="text-lg font-semibold">Forks</h2>
      <ErrorText error={forks.error} />
      {forks.data?.length === 0 && <Card className="p-8 text-center text-sm text-muted-foreground">No forks you can see.</Card>}
      {forks.data && forks.data.length > 0 && (
        <Card className="divide-y">
          {forks.data.map((f) => (
            <div key={f.id} className="flex items-center gap-2 px-4 py-2 text-sm">
              <GitFork className="h-4 w-4 text-muted-foreground" />
              <Link to={`/${f.full_path}`} className="font-medium text-primary hover:underline">
                {f.full_path}
              </Link>
              {f.is_private && <span className="text-xs text-muted-foreground">private</span>}
            </div>
          ))}
        </Card>
      )}
    </div>
  );
}
