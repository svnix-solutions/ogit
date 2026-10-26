import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, repoId } from "./api";

// Notifications (application/notification_app.go): one per thread in the
// user's inbox.

export type Notification = {
  id: string;
  unread: boolean;
  reason: string;
  participating: boolean;
  repository: string;
  subject: { type: "issue" | "pull_request" | "release"; number?: number; title: string; url: string };
  summary: string;
  actor?: string;
  updated_at: string;
};

export type InboxFilter = { all: boolean; participating: boolean; repository: string };

export const notificationsKey = ["notifications"];

// useUnreadCount feeds the bell: every minute and when the window regains
// focus.
export function useUnreadCount(enabled: boolean) {
  return useQuery({
    queryKey: [...notificationsKey, "count"],
    queryFn: async () => (await api<{ unread: number }>("GET", "/notifications/count")).unread,
    enabled,
    refetchInterval: 60_000,
    refetchOnWindowFocus: true,
    retry: false,
  });
}

export function inboxQuery(f: InboxFilter, page: number, perPage: number): string {
  const q = new URLSearchParams({ offset: String(page * perPage), limit: String(perPage) });
  if (f.all) q.set("all", "true");
  if (f.participating) q.set("participating", "true");
  if (f.repository) q.set("repository", repoId(f.repository));
  return q.toString();
}

export function useNotifications(f: InboxFilter, page: number, perPage = 50) {
  return useQuery({
    queryKey: [...notificationsKey, "list", f, page],
    queryFn: () => api<{ notifications: Notification[]; total: number; unread: number }>("GET", `/notifications?${inboxQuery(f, page, perPage)}`),
    placeholderData: keepPreviousData,
    refetchOnWindowFocus: true,
  });
}

// Why you got it, in words.
export const reasonLabels: Record<string, string> = {
  mention: "mentioned",
  review_requested: "review requested",
  assign: "assigned",
  author: "author",
  comment: "commented",
  ci_activity: "check failed",
  subscribed: "watching",
};

export type WatchLevel = "participating" | "all" | "releases" | "ignore";

export const watchLevels: { level: WatchLevel; label: string; hint: string }[] = [
  { level: "participating", label: "Participating", hint: "Only threads you take part in or are mentioned in" },
  { level: "all", label: "All activity", hint: "Also new issues, pull requests and releases" },
  { level: "releases", label: "Releases only", hint: "Also new releases" },
  { level: "ignore", label: "Ignore", hint: "Nothing, not even mentions" },
];
