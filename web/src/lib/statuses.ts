import { useQuery } from "@tanstack/react-query";
import { api } from "./api";

// Commit statuses reported by CI (see application/commit_status.go).

export type StatusState = "pending" | "success" | "failure" | "error";

export type CommitStatus = {
  id: string;
  state: StatusState;
  context: string;
  description: string;
  target_url: string;
  creator?: string;
  created_at: string;
};

export type Combined = { sha: string; state: StatusState; total_count: number; statuses: CommitStatus[] };

// useCombined reads a commit's latest status per context.
export function useCombined(repoId: string, ref: string | undefined) {
  return useQuery({
    queryKey: ["status", repoId, ref],
    queryFn: () => api<Combined>("GET", `/repository/${repoId}/status?ref=${encodeURIComponent(ref!)}`),
    enabled: !!ref,
    // CI updates statuses on its own; look again while some are pending.
    refetchInterval: (q) => (q.state.data?.statuses.some((s) => s.state === "pending") ? 10_000 : false),
  });
}

// useStatuses reads the combined state of many commits at once (icons in
// lists); commits without statuses are absent.
export function useStatuses(repoId: string, shas: string[]) {
  const unique = [...new Set(shas.filter(Boolean))].slice(0, 100).sort();
  return useQuery({
    queryKey: ["statuses", repoId, unique.join(",")],
    queryFn: async () => (await api<{ commits: Record<string, Combined> }>("GET", `/repository/${repoId}/statuses?shas=${unique.join(",")}`)).commits,
    enabled: unique.length > 0,
  });
}

// useRecentContexts suggests contexts for required checks.
export function useRecentContexts(repoId: string) {
  return useQuery({
    queryKey: ["status-contexts", repoId],
    queryFn: async () => (await api<{ contexts: string[] }>("GET", `/repository/${repoId}/status-contexts`)).contexts,
  });
}

// safeLink returns url if it is http(s) (the server enforces it too), so a
// status can never put a javascript: link on the page.
export function safeLink(url: string): string | undefined {
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:" ? u.href : undefined;
  } catch {
    return undefined;
  }
}

export const stateLabel: Record<StatusState, string> = { pending: "pending", success: "passed", failure: "failed", error: "errored" };
