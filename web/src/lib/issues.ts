import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, repoId, type Member } from "./api";
import type { Label, MilestoneRef } from "./pulls";

// Issues, milestones and assignees (see application/issue_app.go).

export type IssueSummary = {
  number: number;
  title: string;
  state: "open" | "closed";
  author: string;
  created_at: string;
  updated_at: string;
  closed_at?: string;
  labels: Label[];
  assignees: string[];
  milestone?: MilestoneRef;
};

export type Issue = IssueSummary & {
  body: string;
  closed_by?: string;
  closed_by_pull?: number;
  can_edit: boolean;
  can_comment: boolean;
  can_label: boolean;
  can_delete: boolean;
};

export type IssueComment = { id: string; author: string; body: string; created_at: string; updated_at: string; can_edit: boolean; can_delete: boolean };

export type Milestone = {
  id: string;
  title: string;
  description: string;
  due_on: string | null;
  state: "open" | "closed";
  closed_at?: string;
  open: number;
  closed: number;
};

export const issuesBase = (fullPath: string) => `/repository/${repoId(fullPath)}/issues`;

export type IssueFilters = { state: string; label?: string; milestone?: string; assignee?: string; mine?: string; q?: string };

export function issuesQuery(f: IssueFilters, page: number, perPage: number): string {
  const p = new URLSearchParams({ state: f.state, limit: String(perPage), offset: String(page * perPage) });
  for (const k of ["label", "milestone", "assignee", "mine", "q"] as const) if (f[k]) p.set(k, f[k]!);
  return p.toString();
}

export function useIssues(fullPath: string, f: IssueFilters, page = 0, perPage = 30) {
  return useQuery({
    queryKey: ["issues", fullPath, f, page],
    queryFn: () => api<{ issues: IssueSummary[]; counts: Record<string, number> }>("GET", `${issuesBase(fullPath)}?${issuesQuery(f, page, perPage)}`),
    placeholderData: keepPreviousData,
  });
}

export function useIssue(fullPath: string, n: number) {
  return useQuery({ queryKey: ["issue", fullPath, n], queryFn: () => api<Issue>("GET", `${issuesBase(fullPath)}/${n}`), retry: false });
}

export function useIssueComments(fullPath: string, n: number) {
  return useQuery({
    queryKey: ["issue-comments", fullPath, n],
    queryFn: async () => (await api<{ comments: IssueComment[] }>("GET", `${issuesBase(fullPath)}/${n}/comments`)).comments,
  });
}

export function useMilestones(fullPath: string, state = "open") {
  return useQuery({
    queryKey: ["milestones", fullPath, state],
    queryFn: async () => (await api<{ milestones: Milestone[] }>("GET", `/repository/${repoId(fullPath)}/milestones?state=${state}`)).milestones,
  });
}

// useAssignable lists the repository's direct user members with write
// access, as assignee suggestions (others can still be typed in).
export function useAssignable(fullPath: string, enabled: boolean) {
  return useQuery({
    queryKey: ["members", fullPath],
    queryFn: async () => (await api<{ members: Member[] | null }>("GET", `/repository/${repoId(fullPath)}/members`)).members ?? [],
    enabled,
    select: (members) =>
      members.filter((m) => m.type === "user" && m.username && ["developer", "maintainer", "owner"].includes(m.role)).map((m) => m.username!),
  });
}

// progress is a milestone's share of closed items, 0 to 100.
export function progress(m: Pick<Milestone, "open" | "closed">): number {
  const total = m.open + m.closed;
  return total === 0 ? 0 : Math.round((m.closed / total) * 100);
}
