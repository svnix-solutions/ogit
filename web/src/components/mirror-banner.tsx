import { useQueryClient } from "@tanstack/react-query";
import { CopyIcon, RefreshCw } from "lucide-react";
import { useEffect, useRef } from "react";
import { Link } from "react-router";
import { atLeast, type Repository } from "@/lib/api";
import { relativeTime } from "@/lib/browse";
import { useMirror, useMirrorActions } from "@/lib/mirrors";
import { Alert } from "./ui/alert";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

// MirrorBanner tells about a repository's mirror or running import: its
// remote and status to maintainers, "mirror" to everyone else.
export function MirrorBanner({ repo }: { repo: Repository }) {
  const maintainer = atLeast(repo.role, "maintainer");
  const m = useMirror(repo.full_path, maintainer);
  const { sync } = useMirrorActions(repo.full_path);
  const qc = useQueryClient();
  const wasBusy = useRef(false);

  // When a sync ends (an import that finished has no mirror any more),
  // the repository's content changed: show it.
  const busyNow = !!m.data && (m.data.status === "pending" || m.data.status === "syncing");
  useEffect(() => {
    if (busyNow) wasBusy.current = true;
    else if (wasBusy.current && (m.data || m.isError)) {
      wasBusy.current = false;
      qc.invalidateQueries();
    }
  }, [busyNow, m.data, m.isError, qc]);

  if (!m.data) {
    return repo.is_mirror ? (
      <Badge title="It follows its remote; pushes are refused">
        <CopyIcon className="h-3 w-3" /> mirror
      </Badge>
    ) : null;
  }
  const d = m.data;
  const busy = d.status === "pending" || d.status === "syncing";
  if (!d.mirror) {
    return (
      <Alert variant={d.status === "failed" ? "destructive" : undefined}>
        {busy ? (
          <span>Importing from {d.url}…</span>
        ) : (
          <span>
            The import from {d.url} failed: {d.last_error}.{" "}
            <Link to={`/${repo.full_path}/settings#mirror`} className="underline">
              Settings
            </Link>
          </span>
        )}{" "}
        {!busy && (
          <Button size="sm" variant="outline" onClick={() => sync.mutate()} disabled={sync.isPending}>
            Retry
          </Button>
        )}
      </Alert>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
      <Badge>
        <CopyIcon className="h-3 w-3" /> mirror
      </Badge>
      <span>
        of <span className="font-mono">{d.url}</span>
        {d.last_success_at && <> · synced {relativeTime(d.last_success_at)}</>}
        {busy && " · syncing…"}
      </span>
      {d.status === "failed" && <span className="text-destructive">last sync failed: {d.last_error}</span>}
      <Button size="sm" variant="ghost" onClick={() => sync.mutate()} disabled={busy || sync.isPending} aria-label="Sync the mirror">
        <RefreshCw className="h-4 w-4" />
      </Button>
    </div>
  );
}
