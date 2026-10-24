import { useQuery } from "@tanstack/react-query";
import { Archive, Globe, Lock, Settings } from "lucide-react";
import { Link, useParams } from "react-router";
import { CopyField } from "@/components/copy-field";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { ButtonLink } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { api, ApiError, atLeast, repoId, type Repository } from "@/lib/api";
import { useInstance } from "@/lib/queries";
import { NotFoundPage } from "./not-found";

export function useRepository(owner: string, name: string) {
  return useQuery({
    queryKey: ["repository", `${owner}/${name}`],
    queryFn: () => api<Repository>("GET", `/repository/${repoId(`${owner}/${name}`)}`),
    retry: false,
  });
}

// cloneURLs builds the HTTPS and (if enabled) SSH clone URLs for this server.
export function cloneURLs(fullPath: string, location: { origin: string; hostname: string }, ssh?: { enabled: boolean; port?: number }) {
  const urls = [{ label: "HTTPS", url: `${location.origin}/${fullPath}.git` }];
  if (ssh?.enabled) {
    const port = ssh.port && ssh.port !== 22 ? `:${ssh.port}` : "";
    urls.push({ label: "SSH", url: `ssh://git@${location.hostname}${port}/${fullPath}.git` });
  }
  return urls;
}

export function RepoPage() {
  const { owner = "", repo = "" } = useParams();
  const r = useRepository(owner, repo);
  const instance = useInstance();

  if (r.error instanceof ApiError && r.error.status === 404) return <NotFoundPage />;
  if (r.isError) return <Alert variant="destructive">{r.error.message}</Alert>;
  if (!r.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const repository = r.data;
  const urls = cloneURLs(repository.full_path, window.location, { enabled: !!instance.data?.ssh_enabled, port: instance.data?.ssh_port });

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl">
          <Link to={`/${repository.namespace}`} className="text-primary hover:underline">
            {repository.namespace}
          </Link>
          <span className="text-muted-foreground"> / </span>
          <span className="font-semibold">{repository.name}</span>
        </h1>
        <Badge>
          {repository.is_private ? <Lock className="h-3 w-3" /> : <Globe className="h-3 w-3" />}
          {repository.is_private ? "private" : "public"}
        </Badge>
        {repository.archived && (
          <Badge>
            <Archive className="h-3 w-3" /> archived
          </Badge>
        )}
        {atLeast(repository.role, "maintainer") && (
          <ButtonLink variant="outline" size="sm" className="ml-auto" to={`/${repository.full_path}/settings`}>
            <Settings className="h-4 w-4" /> Settings
          </ButtonLink>
        )}
      </div>
      {repository.description && <p className="text-muted-foreground">{repository.description}</p>}
      {repository.archived && <Alert>This repository is archived: it can be cloned but not pushed to.</Alert>}

      <Card className="space-y-4 p-5">
        <h2 className="font-semibold">Clone</h2>
        {urls.map((u) => (
          <div key={u.label} className="space-y-1">
            <p className="text-xs font-medium text-muted-foreground">{u.label}</p>
            <CopyField value={u.url} label={`${u.label} clone URL`} />
          </div>
        ))}
        <pre className="overflow-x-auto rounded-md bg-muted p-3 text-xs">{`git clone ${urls[0].url}`}</pre>
        <p className="text-sm text-muted-foreground">
          Over HTTPS, git asks for a username and password: use an{" "}
          <Link to="/settings#tokens" className="text-primary hover:underline">
            access token
          </Link>
          's name and value. Over SSH, add your key in{" "}
          <Link to="/settings#ssh-keys" className="text-primary hover:underline">
            settings
          </Link>
          .
        </p>
      </Card>
    </div>
  );
}
