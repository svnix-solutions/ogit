import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { api, repoId } from "./api";

// Actions: workflows, runs and their logs (see application/actions_app.go).

export type RunStatus = "queued" | "waiting" | "running" | "success" | "failure" | "cancelled";

export type Run = {
  id: string;
  number: number;
  workflow_path: string;
  workflow_name: string;
  event: string;
  ref: string;
  sha: string;
  actor: string;
  status: RunStatus;
  cancel_requested: boolean;
  from_fork: boolean;
  pull_number?: number;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
};

export type Job = { name: string; status: string; started_at: string | null; finished_at: string | null };

export type WorkflowInput = { name: string; description: string; required: boolean; default: string; type: string; options: string[] };
export type Workflow = { path: string; name: string; dispatch: boolean; inputs: WorkflowInput[]; error?: string };

export type RunFilter = { workflow?: string; branch?: string; event?: string; status?: string; pull?: number };

export const actionsBase = (fullPath: string) => `/repository/${repoId(fullPath)}/actions`;
export const actionsKey = (fullPath: string) => ["actions", fullPath];

export const runDone = (s: RunStatus) => s === "success" || s === "failure" || s === "cancelled";

// useWorkflows lists the default branch's workflows; uses_actions decides
// whether the Actions tab shows. Errors just hide it.
export function useWorkflows(fullPath: string) {
  return useQuery({
    queryKey: [...actionsKey(fullPath), "workflows"],
    queryFn: () => api<{ uses_actions: boolean; workflows: Workflow[] }>("GET", `${actionsBase(fullPath)}/workflows`),
    retry: false,
  });
}

function query(f: RunFilter, page: number, perPage: number): string {
  const q = new URLSearchParams({ page: String(page + 1), per_page: String(perPage) });
  for (const [k, v] of Object.entries(f)) if (v) q.set(k, String(v));
  return q.toString();
}

// useRuns lists runs, refreshing while some aren't finished.
export function useRuns(fullPath: string, filter: RunFilter, page = 0, perPage = 25) {
  return useQuery({
    queryKey: [...actionsKey(fullPath), "runs", filter, page],
    queryFn: () => api<{ runs: Run[]; total: number }>("GET", `${actionsBase(fullPath)}/runs?${query(filter, page, perPage)}`),
    placeholderData: keepPreviousData,
    refetchInterval: (q) => (q.state.data?.runs.some((r) => !runDone(r.status)) ? 5_000 : false),
  });
}

// useRun reads a run and its jobs, every 2 seconds until it is over.
export function useRun(fullPath: string, number: number) {
  return useQuery({
    queryKey: [...actionsKey(fullPath), "run", number],
    queryFn: () => api<Run & { jobs: Job[] }>("GET", `${actionsBase(fullPath)}/runs/${number}`),
    refetchInterval: (q) => (q.state.data && runDone(q.state.data.status) ? false : 2_000),
  });
}

// useRunLog follows a run's log: what there is, then what follows every 2
// seconds (the API's ?from=), until the run is over.
export function useRunLog(fullPath: string, number: number) {
  const [log, setLog] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [done, setDone] = useState(false);
  const next = useRef(1);
  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    next.current = 1;
    setLog("");
    setDone(false);
    const poll = async () => {
      try {
        const r = await api<{ log: string; next: number; done: boolean }>("GET", `${actionsBase(fullPath)}/runs/${number}/logs?from=${next.current}`);
        if (stopped) return;
        next.current = r.next;
        if (r.log) setLog((l) => l + r.log);
        setError(null);
        if (r.done) {
          setDone(true);
          return;
        }
      } catch (e) {
        if (stopped) return;
        setError(e);
      }
      timer = setTimeout(poll, 2_000);
    };
    poll();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [fullPath, number]);
  return { log, error, done };
}

// badgeURL is a workflow's status badge (an SVG for READMEs).
export function badgeURL(fullPath: string, workflowPath: string): string {
  const file = workflowPath.split("/").pop()!;
  return `/${fullPath}/actions/workflows/${encodeURIComponent(file)}/badge.svg`;
}

export function shortRef(ref: string): string {
  return ref.replace(/^refs\/(heads|tags)\//, "");
}

export const statusLabel: Record<RunStatus, string> = {
  queued: "Queued",
  waiting: "Waiting for approval",
  running: "In progress",
  success: "Success",
  failure: "Failure",
  cancelled: "Cancelled",
};

export function duration(start: string | null, end: string | null, now = Date.now()): string {
  if (!start) return "";
  const ms = (end ? Date.parse(end) : now) - Date.parse(start);
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}
