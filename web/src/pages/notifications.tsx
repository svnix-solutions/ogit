import { useMutation, useQueryClient } from "@tanstack/react-query";
import { BellOff, CircleDot, GitPullRequest, Mail, MailOpen, Tag } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { pageParam, Pager } from "@/components/pager";
import { ErrorText } from "@/components/section";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { api } from "@/lib/api";
import { relativeTime } from "@/lib/browse";
import { notificationsKey, reasonLabels, useNotifications, type InboxFilter, type Notification } from "@/lib/notifications";
import { cn } from "@/lib/utils";

const perPage = 50;

const icons = { issue: CircleDot, pull_request: GitPullRequest, release: Tag };

// NotificationsPage is the inbox: one entry per thread, grouped by
// repository, newest first.
export function NotificationsPage() {
  const [params, setParams] = useSearchParams();
  const page = pageParam(params);
  const filter: InboxFilter = { all: params.get("all") === "true", participating: params.get("participating") === "true", repository: params.get("repository") ?? "" };
  const list = useNotifications(filter, page, perPage);
  const qc = useQueryClient();
  const refresh = () => qc.invalidateQueries({ queryKey: notificationsKey });
  const update = (change: Record<string, string | null>) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(change)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    next.delete("page");
    setParams(next);
  };
  const readAll = useMutation({ mutationFn: () => api("POST", "/notifications/read", filter.repository ? { repository: filter.repository } : {}), onSuccess: refresh });

  const groups = new Map<string, Notification[]>();
  for (const n of list.data?.notifications ?? []) groups.set(n.repository, [...(groups.get(n.repository) ?? []), n]);

  const tab = (active: boolean) => cn("rounded-md px-3 py-1.5 text-sm", active ? "bg-muted font-medium" : "text-muted-foreground hover:text-foreground");
  return (
    <div className="mx-auto max-w-4xl space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <h1 className="mr-2 text-xl font-semibold">Notifications</h1>
        <button className={tab(!filter.all)} onClick={() => update({ all: null })}>
          Unread{list.data ? ` (${list.data.unread})` : ""}
        </button>
        <button className={tab(filter.all)} onClick={() => update({ all: "true" })}>
          All
        </button>
        <label className="ml-2 flex items-center gap-1.5 text-sm">
          <input type="checkbox" checked={filter.participating} onChange={(e) => update({ participating: e.target.checked ? "true" : null })} />
          Participating only
        </label>
        {filter.repository && (
          <Badge>
            {filter.repository}
            <button className="ml-1" aria-label="Show all repositories" onClick={() => update({ repository: null })}>
              ×
            </button>
          </Badge>
        )}
        <Button size="sm" variant="outline" className="ml-auto" disabled={readAll.isPending || !list.data?.unread} onClick={() => readAll.mutate()}>
          Mark all as read
        </Button>
      </div>
      <ErrorText error={list.error ?? readAll.error} />
      {list.data?.notifications.length === 0 && (
        <Card className="p-8 text-center text-sm text-muted-foreground">{filter.all ? "No notifications." : "All caught up."}</Card>
      )}
      {[...groups.entries()].map(([repo, items]) => (
        <Card key={repo} className="overflow-hidden">
          <div className="flex items-center gap-2 border-b bg-muted/40 px-4 py-2 text-sm">
            <button className="font-medium hover:underline" onClick={() => update({ repository: repo })}>
              {repo}
            </button>
          </div>
          <ul className="divide-y">
            {items.map((n) => (
              <Row key={n.id} n={n} onChange={refresh} />
            ))}
          </ul>
        </Card>
      ))}
      {list.data && <Pager page={page} perPage={perPage} count={list.data.notifications.length} total={list.data.total} onPage={(p) => update({ page: String(p + 1) })} />}
    </div>
  );
}

function Row({ n, onChange }: { n: Notification; onChange: () => void }) {
  const navigate = useNavigate();
  const [muted, setMuted] = useState(false);
  const read = useMutation({ mutationFn: (unread: boolean) => api("PATCH", `/notifications/${n.id}`, { unread }), onSuccess: onChange });
  const mute = useMutation({
    mutationFn: (on: boolean) => api(on ? "POST" : "DELETE", `/notifications/${n.id}/mute`),
    onSuccess: (_, on) => (setMuted(on), onChange()),
  });
  const Icon = icons[n.subject.type] ?? CircleDot;
  const open = () => {
    if (n.unread) read.mutate(false);
    navigate(n.subject.url);
  };
  return (
    <li className={cn("flex flex-wrap items-start gap-3 px-4 py-3", !n.unread && "opacity-70")}>
      <Icon className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1">
        <Link
          to={n.subject.url}
          onClick={(e) => {
            e.preventDefault();
            open();
          }}
          className={cn("hover:underline", n.unread && "font-semibold")}
        >
          {n.subject.title}
          {n.subject.number ? <span className="font-normal text-muted-foreground"> #{n.subject.number}</span> : null}
        </Link>
        <p className="text-xs text-muted-foreground">{n.summary}</p>
      </div>
      <Badge>{reasonLabels[n.reason] ?? n.reason}</Badge>
      <span className="w-24 text-right text-xs text-muted-foreground">{relativeTime(n.updated_at)}</span>
      <span className="flex gap-1">
        <Button size="icon" variant="ghost" aria-label={n.unread ? "Mark as read" : "Mark as unread"} title={n.unread ? "Mark as read" : "Mark as unread"} onClick={() => read.mutate(!n.unread)}>
          {n.unread ? <MailOpen className="h-4 w-4" /> : <Mail className="h-4 w-4" />}
        </Button>
        <Button
          size="icon"
          variant="ghost"
          aria-label={muted ? "Subscribe again" : "Unsubscribe"}
          title={muted ? "Subscribe again" : "Unsubscribe from this thread"}
          onClick={() => mute.mutate(!muted)}
        >
          <BellOff className={cn("h-4 w-4", muted && "text-primary")} />
        </Button>
      </span>
    </li>
  );
}
