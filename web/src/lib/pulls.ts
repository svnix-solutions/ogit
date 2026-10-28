import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, repoId } from "./api";
import type { Comparison } from "./browse";

export type Label = { id: string; name: string; color: string; description: string };
export type PullSummary = {
  number: number;
  title: string;
  state: "open" | "closed" | "merged";
  draft: boolean;
  author: string;
  source_branch: string;
  target_branch: string;
  created_at: string;
  updated_at: string;
  merged_at?: string;
  closed_at?: string;
  labels: Label[];
  head_commit?: string;
  source_repository?: string; // the fork ("deleted fork" when gone)
};
export type MilestoneRef = { id: string; title: string; state: "open" | "closed" };
export type Pull = PullSummary & {
  body: string;
  base_commit: string;
  head_commit: string;
  merge_commit?: string;
  merge_method?: string;
  merged_by?: string;
  mergeable?: boolean;
  conflicts: string[];
  branches_exist: boolean;
  can_edit: boolean;
  can_merge: boolean;
  can_review: boolean;
  approved: string[];
  changes_requested: string[];
  required_approvals: number;
  protected_by?: string; // the rule protecting the target branch
  required_checks: { context: string; state: "" | "pending" | "success" | "failure" | "error" }[];
  merge_blocked?: { type: string; message: string }; // why the viewer can't merge
  assignees: string[];
  requested_reviewers?: string[];
  milestone?: MilestoneRef;
  allow_maintainer_edits: boolean;
};
export type PullComment = {
  id: string;
  author: string;
  review_id?: string;
  body: string;
  path?: string;
  line?: number;
  side?: "old" | "new";
  commit_id: string;
  created_at: string;
  updated_at: string;
  can_edit: boolean;
  can_delete: boolean;
};
export type PullReview = { id: string; author: string; state: "approved" | "changes_requested" | "commented"; body: string; commit_id: string; created_at: string };

export const pullsBase = (fullPath: string) => `/repository/${repoId(fullPath)}/pulls`;

export function usePulls(fullPath: string, state: string, label: string, page = 0, perPage = 30) {
  return useQuery({
    queryKey: ["pulls", fullPath, state, label, page],
    queryFn: () =>
      api<{ pull_requests: PullSummary[]; counts: Record<string, number> }>(
        "GET",
        `${pullsBase(fullPath)}?state=${state}&limit=${perPage}&offset=${page * perPage}${label ? `&label=${encodeURIComponent(label)}` : ""}`,
      ),
    placeholderData: keepPreviousData,
  });
}

export function usePull(fullPath: string, n: number) {
  return useQuery({ queryKey: ["pull", fullPath, n], queryFn: () => api<Pull>("GET", `${pullsBase(fullPath)}/${n}`), retry: false });
}

export function usePullActivity(fullPath: string, n: number) {
  return useQuery({
    queryKey: ["pull-activity", fullPath, n],
    queryFn: () => api<{ reviews: PullReview[]; comments: PullComment[] }>("GET", `${pullsBase(fullPath)}/${n}/activity`),
  });
}

export function usePullCompare(fullPath: string, n: number) {
  return useQuery({ queryKey: ["pull-compare", fullPath, n], queryFn: () => api<Comparison>("GET", `${pullsBase(fullPath)}/${n}/compare`) });
}

export function useLabels(fullPath: string) {
  return useQuery({
    queryKey: ["labels", fullPath],
    queryFn: async () => (await api<{ labels: Label[] }>("GET", `/repository/${repoId(fullPath)}/labels`)).labels,
  });
}

// textColor picks black or white text for a label's background.
export function textColor(hex: string): string {
  const n = parseInt(hex.slice(1), 16);
  const [r, g, b] = [(n >> 16) & 255, (n >> 8) & 255, n & 255];
  return 0.299 * r + 0.587 * g + 0.114 * b > 150 ? "#000" : "#fff";
}
