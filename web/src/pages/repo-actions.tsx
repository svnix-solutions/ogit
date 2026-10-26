import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Ban, CheckCircle2, CircleDashed, Clock, Loader2, Play, RotateCw, ShieldAlert, XCircle } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { CopyField } from "@/components/copy-field";
import { pageParam, Pager } from "@/components/pager";
import { useRepo } from "@/components/repo-layout";
import { ErrorText } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select } from "@/components/ui/select";
import {
  actionsBase,
  actionsKey,
  badgeURL,
  duration,
  runDone,
  shortRef,
  statusLabel,
  useRun,
  useRunLog,
  useRuns,
  useWorkflows,
  type Run,
  type RunStatus,
  type Workflow,
} from "@/lib/actions";
import { api, ApiError, atLeast } from "@/lib/api";
import { relativeTime, shortHash } from "@/lib/browse";
import { cn } from "@/lib/utils";
import { NotFoundPage } from "./not-found";

const perPage = 25;

// RunIcon shows a run's or a job's status.
export function RunIcon({ status, className }: { status: string; className?: string }) {
  const label = statusLabel[status as RunStatus] ?? status;
  const cls = cn("h-4 w-4 shrink-0", className);
  switch (status) {
    case "success":
      return <CheckCircle2 role="img" aria-label={label} className={cn(cls, "text-green-600")} />;
    case "failure":
      return <XCircle role="img" aria-label={label} className={cn(cls, "text-red-600")} />;
    case "cancelled":
      return <Ban role="img" aria-label={label} className={cn(cls, "text-muted-foreground")} />;
    case "skipped":
      return <CircleDashed role="img" aria-label="Skipped" className={cn(cls, "text-muted-foreground")} />;
    case "running":
      return <Loader2 role="img" aria-label={label} className={cn(cls, "animate-spin text-amber-500")} />;
    case "waiting":
      return <ShieldAlert role="img" aria-label={label} className={cn(cls, "text-amber-500")} />;
    default:
      return <Clock role="img" aria-label={label} className={cn(cls, "text-amber-500")} />;
  }
}

function runTitle(r: Run): string {
  return r.workflow_name || r.workflow_path;
}

function eventLabel(r: Run): string {
  if (r.event === "pull_request" && r.pull_number) return `pull request #${r.pull_number}`;
  return r.event.replace(/_/g, " ");
}

const events = ["push", "pull_request", "workflow_dispatch", "schedule", "issues", "issue_comment", "release", "create", "delete"];
const statuses: RunStatus[] = ["queued", "waiting", "running", "success", "failure", "cancelled"];

export function ActionsPage() {
  const repo = useRepo();
  const [params, setParams] = useSearchParams();
  const page = pageParam(params);
  const filter = { workflow: params.get("workflow") ?? "", branch: params.get("branch") ?? "", event: params.get("event") ?? "", status: params.get("status") ?? "" };
  const workflows = useWorkflows(repo.full_path);
  const runs = useRuns(repo.full_path, filter, page, perPage);
  const update = (key: string, value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    next.delete("page");
    setParams(next);
  };
  const selected = workflows.data?.workflows.find((w) => w.path === filter.workflow);
  const canWrite = atLeast(repo.role, "developer");

  return (
    <div className="grid gap-6 md:grid-cols-[14rem_1fr]">
      <nav aria-label="Workflows" className="space-y-1 text-sm">
        <button className={cn("block w-full rounded-md px-2 py-1.5 text-left", !filter.workflow ? "bg-muted font-medium" : "hover:bg-muted")} onClick={() => update("workflow", "")}>
          All workflows
        </button>
        {workflows.data?.workflows.map((w) => (
          <button
            key={w.path}
            title={w.path}
            className={cn("block w-full truncate rounded-md px-2 py-1.5 text-left", filter.workflow === w.path ? "bg-muted font-medium" : "hover:bg-muted")}
            onClick={() => update("workflow", w.path)}
          >
            {w.name}
            {w.error && <span className="ml-1 text-xs text-red-600">(invalid)</span>}
          </button>
        ))}
      </nav>
      <div className="min-w-0 space-y-4">
        <div className="flex flex-wrap items-center gap-2">
          <h2 className="mr-auto text-lg font-semibold">{selected ? selected.name : "All workflows"}</h2>
          <Input aria-label="Branch" placeholder="Branch" className="h-8 w-36" defaultValue={filter.branch} onKeyDown={(e) => e.key === "Enter" && update("branch", e.currentTarget.value.trim())} />
          <Select aria-label="Event" className="h-8 w-40" value={filter.event} onChange={(e) => update("event", e.target.value)}>
            <option value="">All events</option>
            {events.map((e) => (
              <option key={e} value={e}>
                {e}
              </option>
            ))}
          </Select>
          <Select aria-label="Status" className="h-8 w-40" value={filter.status} onChange={(e) => update("status", e.target.value)}>
            <option value="">All statuses</option>
            {statuses.map((s) => (
              <option key={s} value={s}>
                {statusLabel[s]}
              </option>
            ))}
          </Select>
        </div>
        {selected?.error && <Alert variant="destructive">{selected.path}: {selected.error}.</Alert>}
        {selected && !selected.error && <WorkflowTools workflow={selected} canWrite={canWrite} />}
        <ErrorText error={runs.error} />
        {runs.data?.runs.length === 0 && (
          <Card className="p-8 text-center text-sm text-muted-foreground">
            {workflows.data?.workflows.length ? "No runs yet." : "Add a workflow in .ogit/workflows or .github/workflows to run it on your runners."}
          </Card>
        )}
        {runs.data && runs.data.runs.length > 0 && (
          <Card className="overflow-hidden">
            <ul className="divide-y">
              {runs.data.runs.map((r) => (
                <li key={r.id} className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm">
                  <RunIcon status={r.status} />
                  <div className="min-w-0 flex-1">
                    <Link to={`/${repo.full_path}/actions/runs/${r.number}`} className="font-medium hover:underline">
                      {runTitle(r)} <span className="font-normal text-muted-foreground">#{r.number}</span>
                    </Link>
                    <p className="text-xs text-muted-foreground">
                      {eventLabel(r)} · {shortRef(r.ref)} · {shortHash(r.sha)}
                      {r.actor && ` · ${r.actor}`}
                    </p>
                  </div>
                  {r.from_fork && <Badge>fork</Badge>}
                  <span className="w-28 text-right text-xs text-muted-foreground">
                    {relativeTime(r.created_at)}
                    {r.started_at && <span className="block">{duration(r.started_at, r.finished_at)}</span>}
                  </span>
                </li>
              ))}
            </ul>
          </Card>
        )}
        {runs.data && <Pager page={page} perPage={perPage} count={runs.data.runs.length} total={runs.data.total} onPage={(p) => update("page", String(p + 1))} />}
      </div>
    </div>
  );
}

// WorkflowTools: the status badge to copy and, for workflow_dispatch
// workflows, the Run workflow form.
function WorkflowTools({ workflow, canWrite }: { workflow: Workflow; canWrite: boolean }) {
  const repo = useRepo();
  const [open, setOpen] = useState(false);
  const badge = window.location.origin + badgeURL(repo.full_path, workflow.path);
  return (
    <Card className="space-y-3 p-4 text-sm">
      <div className="flex flex-wrap items-center gap-3">
        <img src={badgeURL(repo.full_path, workflow.path)} alt={`${workflow.name} status`} className="h-5" />
        <div className="min-w-0 flex-1">
          <CopyField value={`![${workflow.name}](${badge})`} label="Badge Markdown" />
        </div>
        {workflow.dispatch && canWrite && (
          <Button size="sm" onClick={() => setOpen(!open)}>
            <Play className="h-4 w-4" /> Run workflow
          </Button>
        )}
      </div>
      {open && <RunWorkflowForm workflow={workflow} onDone={() => setOpen(false)} />}
    </Card>
  );
}

function RunWorkflowForm({ workflow, onDone }: { workflow: Workflow; onDone: () => void }) {
  const repo = useRepo();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [branch, setBranch] = useState("");
  // A required choice shows its first option: start with it, so what is
  // sent is what the form shows.
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(workflow.inputs.map((i) => [i.name, i.default || (i.type === "choice" && i.required ? (i.options[0] ?? "") : "")])),
  );
  const file = workflow.path.split("/").pop()!;
  const run = useMutation({
    mutationFn: () => {
      const inputs = Object.fromEntries(Object.entries(values).filter(([, v]) => v !== ""));
      return api<Run>("POST", `${actionsBase(repo.full_path)}/workflows/${encodeURIComponent(file)}/dispatches`, { ref: branch, inputs });
    },
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: actionsKey(repo.full_path) });
      onDone();
      navigate(`/${repo.full_path}/actions/runs/${r.number}`);
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    run.mutate();
  };
  return (
    <form className="space-y-3 border-t pt-3" onSubmit={submit}>
      <div className="space-y-1">
        <Label htmlFor="dispatch-branch">Branch</Label>
        <Input id="dispatch-branch" value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="the default branch" />
      </div>
      {workflow.inputs.map((input) => {
        const id = `input-${input.name}`;
        const set = (v: string) => setValues({ ...values, [input.name]: v });
        return (
          <div key={input.name} className="space-y-1">
            {input.type === "boolean" ? (
              <label className="flex items-center gap-2">
                <input id={id} type="checkbox" checked={values[input.name] === "true"} onChange={(e) => set(e.target.checked ? "true" : "false")} />
                {input.description || input.name}
              </label>
            ) : (
              <>
                <Label htmlFor={id}>
                  {input.description || input.name}
                  {input.required && <span className="text-red-600"> *</span>}
                </Label>
                {input.type === "choice" ? (
                  <Select id={id} value={values[input.name] ?? ""} onChange={(e) => set(e.target.value)} required={input.required}>
                    {!input.required && <option value="" />}
                    {input.options.map((o) => (
                      <option key={o} value={o}>
                        {o}
                      </option>
                    ))}
                  </Select>
                ) : (
                  <Input id={id} type={input.type === "number" ? "number" : "text"} value={values[input.name] ?? ""} onChange={(e) => set(e.target.value)} required={input.required} />
                )}
              </>
            )}
          </div>
        );
      })}
      <ErrorText error={run.error} />
      <div className="flex gap-2">
        <Button type="submit" disabled={run.isPending}>
          Run
        </Button>
        <Button type="button" variant="ghost" onClick={onDone}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

export function RunPage() {
  const repo = useRepo();
  const number = Number(useParams().number);
  const run = useRun(repo.full_path, number);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const refresh = () => qc.invalidateQueries({ queryKey: actionsKey(repo.full_path) });
  const act = useMutation({
    mutationFn: (what: "cancel" | "rerun" | "approve") => api<Run>("POST", `${actionsBase(repo.full_path)}/runs/${number}/${what}`, {}),
    onSuccess: (r, what) => {
      refresh();
      if (what === "rerun") navigate(`/${repo.full_path}/actions/runs/${r.number}`);
    },
  });

  if (run.error instanceof ApiError && run.error.status === 404) return <NotFoundPage />;
  if (run.isError) return <Alert variant="destructive">{run.error.message}</Alert>;
  if (!run.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const r = run.data;
  const canWrite = atLeast(repo.role, "developer");
  const canApprove = atLeast(repo.role, "maintainer");

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start gap-3">
        <RunIcon status={r.status} className="mt-1.5 h-5 w-5" />
        <div className="min-w-0 flex-1">
          <h2 className="text-xl font-semibold">
            {runTitle(r)} <span className="font-normal text-muted-foreground">#{r.number}</span>
          </h2>
          <p className="text-sm text-muted-foreground">
            {statusLabel[r.status]} · {eventLabel(r)} on {shortRef(r.ref)} at{" "}
            <Link className="hover:underline" to={`/${repo.full_path}/commit/${r.sha}`}>
              {shortHash(r.sha)}
            </Link>
            {r.actor && ` · by ${r.actor}`} · {relativeTime(r.created_at)}
            {r.started_at && ` · ${duration(r.started_at, r.finished_at)}`}
          </p>
          <p className="text-xs text-muted-foreground">{r.workflow_path}</p>
        </div>
        <span className="flex gap-2">
          {canWrite && !runDone(r.status) && (
            <Button variant="outline" size="sm" disabled={act.isPending || r.cancel_requested} onClick={() => act.mutate("cancel")}>
              <Ban className="h-4 w-4" /> {r.cancel_requested ? "Cancelling…" : "Cancel"}
            </Button>
          )}
          {canWrite && runDone(r.status) && (
            <Button variant="outline" size="sm" disabled={act.isPending} onClick={() => act.mutate("rerun")}>
              <RotateCw className="h-4 w-4" /> Re-run
            </Button>
          )}
        </span>
      </div>
      {r.status === "waiting" && (
        <Alert>
          <div className="flex flex-wrap items-center gap-3">
            <p className="flex-1 text-sm">
              This run comes from a first-time contributor's fork. It runs without secrets and with a read-only token, but review the changes to its workflows before letting it run.
            </p>
            {canApprove && (
              <Button size="sm" disabled={act.isPending} onClick={() => act.mutate("approve")}>
                Approve and run
              </Button>
            )}
          </div>
        </Alert>
      )}
      <ErrorText error={act.error} />
      <Card className="overflow-hidden">
        <div className="border-b bg-muted/40 px-4 py-2 text-sm font-medium">Jobs</div>
        {r.jobs.length === 0 ? (
          <p className="px-4 py-3 text-sm text-muted-foreground">{runDone(r.status) ? "No job ran." : "Waiting for a runner."}</p>
        ) : (
          <ul className="divide-y">
            {r.jobs.map((j) => (
              <li key={j.name} className="flex items-center gap-3 px-4 py-2 text-sm">
                <RunIcon status={j.status} />
                <span className="flex-1 font-medium">{j.name}</span>
                <span className="text-xs text-muted-foreground">{duration(j.started_at, j.finished_at)}</span>
              </li>
            ))}
          </ul>
        )}
      </Card>
      <LogView fullPath={repo.full_path} number={r.number} />
    </div>
  );
}

// LogView follows a run's log, scrolled to the end unless the reader
// scrolled up.
function LogView({ fullPath, number }: { fullPath: string; number: number }) {
  const { log, error, done } = useRunLog(fullPath, number);
  const box = useRef<HTMLPreElement>(null);
  const follow = useRef(true);
  const lines = useMemo(() => log.split("\n"), [log]);
  useEffect(() => {
    const el = box.current;
    if (el && follow.current) el.scrollTop = el.scrollHeight;
  }, [log]);
  return (
    <Card className="overflow-hidden">
      <div className="flex items-center gap-2 border-b bg-muted/40 px-4 py-2 text-sm font-medium">
        Log
        {!done && <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" aria-label="Following" />}
      </div>
      <ErrorText error={error} />
      <pre
        ref={box}
        aria-label="Run log"
        className="max-h-[32rem] overflow-auto bg-zinc-950 p-4 text-xs leading-5 text-zinc-100"
        onScroll={(e) => {
          const el = e.currentTarget;
          follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        }}
      >
        {log === "" ? (done ? "No output." : "Waiting for output…") : lines.map((l, i) => <div key={i}>{l || " "}</div>)}
      </pre>
    </Card>
  );
}

// PullRuns lists a pull request's runs (latest per workflow), with links,
// approval and re-run.
export function PullRuns({ number }: { number: number }) {
  const repo = useRepo();
  const runs = useRuns(repo.full_path, { pull: number }, 0, 50);
  const qc = useQueryClient();
  const act = useMutation({
    mutationFn: ({ run, what }: { run: number; what: "rerun" | "approve" }) => api<Run>("POST", `${actionsBase(repo.full_path)}/runs/${run}/${what}`, {}),
    onSuccess: () => qc.invalidateQueries({ queryKey: actionsKey(repo.full_path) }),
  });
  const latest = new Map<string, Run>();
  for (const r of runs.data?.runs ?? []) if (!latest.has(r.workflow_path)) latest.set(r.workflow_path, r);
  if (latest.size === 0) return null;
  const canWrite = atLeast(repo.role, "developer");
  const canApprove = atLeast(repo.role, "maintainer");
  return (
    <div className="space-y-1 rounded-md border p-2 text-sm">
      <p className="text-xs font-medium text-muted-foreground">Workflow runs</p>
      <ul className="space-y-1">
        {[...latest.values()].map((r) => (
          <li key={r.id} className="flex items-center gap-2">
            <RunIcon status={r.status} />
            <Link to={`/${repo.full_path}/actions/runs/${r.number}`} className="font-medium hover:underline">
              {runTitle(r)} #{r.number}
            </Link>
            <span className="min-w-0 flex-1 truncate text-muted-foreground">{statusLabel[r.status]}</span>
            {r.status === "waiting" && canApprove && (
              <Button size="sm" variant="outline" className="h-7" disabled={act.isPending} onClick={() => act.mutate({ run: r.number, what: "approve" })}>
                Approve and run
              </Button>
            )}
            {runDone(r.status) && canWrite && (
              <Button size="sm" variant="ghost" className="h-7" aria-label={`Re-run ${runTitle(r)}`} disabled={act.isPending} onClick={() => act.mutate({ run: r.number, what: "rerun" })}>
                <RotateCw className="h-3.5 w-3.5" />
              </Button>
            )}
          </li>
        ))}
      </ul>
      <ErrorText error={act.error} />
    </div>
  );
}
