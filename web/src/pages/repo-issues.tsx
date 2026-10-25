import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, CircleDot, Milestone as MilestoneIcon, Search } from "lucide-react";
import { useMemo, useState } from "react";
import { Link, Navigate, useNavigate, useParams, useSearchParams } from "react-router";
import { LabelBadge } from "@/components/label-badge";
import { pageParam, Pager } from "@/components/pager";
import { useRepo } from "@/components/repo-layout";
import { ErrorText } from "@/components/section";
import { AssigneesBox, MilestoneBox } from "@/components/tracking";
import { Alert } from "@/components/ui/alert";
import { Button, ButtonLink } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label as FieldLabel } from "@/components/ui/label";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api, ApiError, atLeast, repoId } from "@/lib/api";
import { useProfile } from "@/lib/auth";
import { relativeTime } from "@/lib/browse";
import { issuesBase, progress, useIssue, useIssueComments, useIssues, useMilestones, type Issue, type IssueFilters, type IssueSummary, type Milestone } from "@/lib/issues";
import { renderMarkdown } from "@/lib/markdown";
import { useLabels } from "@/lib/pulls";
import { formatDate } from "@/lib/queries";
import { cn } from "@/lib/utils";

export function IssueStateIcon({ state, className }: { state: IssueSummary["state"]; className?: string }) {
  return state === "open" ? (
    <CircleDot aria-label="open" className={cn("text-green-600 dark:text-green-400", className)} />
  ) : (
    <CheckCircle2 aria-label="closed" className={cn("text-purple-600 dark:text-purple-400", className)} />
  );
}

function Markdown({ text, fullPath }: { text: string; fullPath: string }) {
  const html = useMemo(() => renderMarkdown(text, { fullPath, ref: "HEAD", dir: "", links: true }), [text, fullPath]);
  return <div className="markdown text-sm" dangerouslySetInnerHTML={{ __html: html }} />;
}

export function IssuesPage() {
  const repo = useRepo();
  const profile = useProfile();
  const [params, setParams] = useSearchParams();
  const filters: IssueFilters = {
    state: params.get("state") ?? "open",
    label: params.get("label") ?? "",
    milestone: params.get("milestone") ?? "",
    assignee: params.get("assignee") ?? "",
    mine: params.get("mine") ?? "",
    q: params.get("q") ?? "",
  };
  const page = pageParam(params);
  const perPage = 30;
  const issues = useIssues(repo.full_path, filters, page, perPage);
  const labels = useLabels(repo.full_path);
  const milestones = useMilestones(repo.full_path, "all");
  const [search, setSearch] = useState(filters.q ?? "");
  const set = (k: string, v: string) => {
    const next = new URLSearchParams(params);
    if (v) next.set(k, v);
    else next.delete(k);
    next.delete("page");
    setParams(next);
  };
  const signedIn = !!profile.data;
  const counts = issues.data?.counts ?? {};
  const total = filters.state === "all" ? (counts.open ?? 0) + (counts.closed ?? 0) : counts[filters.state];

  return (
    <div className="space-y-4">
      {repo.has_issues === false && <Alert>Issues are turned off for this repository; existing ones are read-only.</Alert>}
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex rounded-md border text-sm">
          {(["open", "closed", "all"] as const).map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => set("state", s === "open" ? "" : s)}
              className={cn("px-3 py-1.5 capitalize", filters.state === s ? "bg-muted font-medium" : "text-muted-foreground hover:text-foreground")}
            >
              {s} {s !== "all" && <span className="text-xs">{counts[s] ?? 0}</span>}
            </button>
          ))}
        </div>
        <form
          className="flex min-w-48 flex-1 items-center gap-1"
          onSubmit={(e) => {
            e.preventDefault();
            set("q", search.trim());
          }}
        >
          <Input aria-label="Search issues" placeholder="Search titles and descriptions" value={search} onChange={(e) => setSearch(e.target.value)} className="h-9" />
          <Button type="submit" variant="outline" size="icon" aria-label="Search">
            <Search className="h-4 w-4" />
          </Button>
        </form>
        {signedIn && repo.has_issues !== false && (
          <ButtonLink to={`/${repo.full_path}/issues/new`}>New issue</ButtonLink>
        )}
      </div>
      <div className="flex flex-wrap gap-2 text-sm">
        {labels.data && labels.data.length > 0 && (
          <Select aria-label="Label" value={filters.label} onChange={(e) => set("label", e.target.value)}>
            <option value="">All labels</option>
            {labels.data.map((l) => (
              <option key={l.id}>{l.name}</option>
            ))}
          </Select>
        )}
        <Select aria-label="Milestone" value={filters.milestone} onChange={(e) => set("milestone", e.target.value)}>
          <option value="">Any milestone</option>
          <option value="none">No milestone</option>
          {milestones.data?.map((m) => (
            <option key={m.id} value={m.id}>
              {m.title}
            </option>
          ))}
        </Select>
        <Select aria-label="Assignee" value={filters.assignee === "none" ? "none" : ""} onChange={(e) => set("assignee", e.target.value)}>
          <option value="">Anyone assigned or not</option>
          <option value="none">Unassigned</option>
        </Select>
        {signedIn && (
          <Select aria-label="Mine" value={filters.mine} onChange={(e) => set("mine", e.target.value)}>
            <option value="">Everyone's</option>
            <option value="assigned">Assigned to me</option>
            <option value="created">Created by me</option>
          </Select>
        )}
        <Link to={`/${repo.full_path}/milestones`} className="inline-flex items-center gap-1 self-center text-primary hover:underline">
          <MilestoneIcon className="h-4 w-4" /> Milestones
        </Link>
      </div>
      {issues.isError && <Alert variant="destructive">{issues.error.message}</Alert>}
      {issues.data &&
        (issues.data.issues.length === 0 ? (
          <Card className="p-8 text-center text-sm text-muted-foreground">No issues here.</Card>
        ) : (
          <Card className="divide-y">
            {issues.data.issues.map((is) => (
              <div key={is.number} className="flex items-start gap-3 px-4 py-3">
                <IssueStateIcon state={is.state} className="mt-0.5 h-4 w-4 shrink-0" />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <Link to={`/${repo.full_path}/issues/${is.number}`} className="font-medium hover:text-primary hover:underline">
                      {is.title}
                    </Link>
                    {is.labels.map((l) => (
                      <LabelBadge key={l.id} label={l} />
                    ))}
                  </div>
                  <p className="text-xs text-muted-foreground">
                    #{is.number} opened by {is.author || "ghost"} · updated {relativeTime(is.updated_at)}
                    {is.milestone && (
                      <>
                        {" · "}
                        <Link to={`?milestone=${is.milestone.id}&state=all`} className="hover:underline">
                          {is.milestone.title}
                        </Link>
                      </>
                    )}
                  </p>
                </div>
                {is.assignees.length > 0 && <span className="shrink-0 text-xs text-muted-foreground">{is.assignees.join(", ")}</span>}
              </div>
            ))}
          </Card>
        ))}
      {issues.data && (
        <Pager page={page} perPage={perPage} count={issues.data.issues.length} total={total} onPage={(p) => set("page", p ? String(p + 1) : "")} />
      )}
    </div>
  );
}

export function NewIssuePage() {
  const repo = useRepo();
  const navigate = useNavigate();
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const create = useMutation({
    mutationFn: () => api<Issue>("POST", issuesBase(repo.full_path), { title, body }),
    onSuccess: (is) => navigate(`/${repo.full_path}/issues/${is.number}`),
  });
  return (
    <Card className="max-w-3xl space-y-3 p-5">
      <h2 className="text-lg font-semibold">New issue</h2>
      <div className="space-y-1.5">
        <FieldLabel htmlFor="issue-title">Title</FieldLabel>
        <Input id="issue-title" value={title} onChange={(e) => setTitle(e.target.value)} />
      </div>
      <div className="space-y-1.5">
        <FieldLabel htmlFor="issue-body">Description</FieldLabel>
        <Textarea id="issue-body" className="min-h-40" placeholder="Markdown, @mentions and #123 work." value={body} onChange={(e) => setBody(e.target.value)} />
      </div>
      <ErrorText error={create.error} />
      <Button onClick={() => create.mutate()} disabled={!title.trim() || create.isPending}>
        Open issue
      </Button>
    </Card>
  );
}

// NumberRedirect sends #n to the pull request when n is one (issues and
// pull requests share numbers; links point at /issues/n).
function NumberRedirect({ n }: { n: number }) {
  const repo = useRepo();
  const kind = useQuery({
    queryKey: ["number", repo.full_path, n],
    queryFn: () => api<{ type: "issue" | "pull" }>("GET", `/repository/${repoId(repo.full_path)}/numbers/${n}`),
    retry: false,
  });
  if (kind.data?.type === "pull") return <Navigate to={`/${repo.full_path}/pulls/${n}`} replace />;
  if (kind.isPending) return <p className="text-sm text-muted-foreground">Loading…</p>;
  return <Alert variant="destructive">No issue or pull request #{n}.</Alert>;
}

export function IssuePage() {
  const repo = useRepo();
  const n = Number(useParams().number);
  const issue = useIssue(repo.full_path, n);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [editing, setEditing] = useState<"title" | "body" | null>(null);
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const base = `${issuesBase(repo.full_path)}/${n}`;
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["issue", repo.full_path, n] });
    qc.invalidateQueries({ queryKey: ["issue-comments", repo.full_path, n] });
    qc.invalidateQueries({ queryKey: ["issues", repo.full_path] });
    qc.invalidateQueries({ queryKey: ["milestones", repo.full_path] });
  };
  const update = useMutation({ mutationFn: (fields: Record<string, string>) => api("PATCH", base, fields), onSuccess: () => (setEditing(null), refresh()) });
  const remove = useMutation({ mutationFn: () => api("DELETE", base), onSuccess: () => navigate(`/${repo.full_path}/issues`) });

  if (issue.error instanceof ApiError && issue.error.status === 404) return <NumberRedirect n={n} />;
  if (issue.isError) return <Alert variant="destructive">{issue.error.message}</Alert>;
  if (!issue.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const is = issue.data;

  return (
    <div className="space-y-4">
      <div className="space-y-2">
        {editing === "title" ? (
          <form className="flex gap-2" onSubmit={(e) => (e.preventDefault(), update.mutate({ title }))}>
            <Input aria-label="Title" value={title} onChange={(e) => setTitle(e.target.value)} autoFocus />
            <Button type="submit">Save</Button>
            <Button type="button" variant="ghost" onClick={() => setEditing(null)}>
              Cancel
            </Button>
          </form>
        ) : (
          <h2 className="flex flex-wrap items-center gap-2 text-2xl">
            <span>{is.title}</span> <span className="text-muted-foreground">#{is.number}</span>
            {is.can_edit && (
              <Button variant="ghost" size="sm" onClick={() => (setTitle(is.title), setEditing("title"))}>
                Edit
              </Button>
            )}
          </h2>
        )}
        <div className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
          <span className={cn("inline-flex items-center gap-1 rounded-full px-3 py-1 font-medium text-white", is.state === "open" ? "bg-green-600" : "bg-purple-600")}>
            <IssueStateIcon state={is.state} className="h-4 w-4 !text-white" /> {is.state === "open" ? "Open" : "Closed"}
          </span>
          <span>
            <span className="font-medium text-foreground">{is.author || "ghost"}</span> opened this {relativeTime(is.created_at)}
          </span>
          {is.state === "closed" && is.closed_by && (
            <span>
              · closed by {is.closed_by}
              {is.closed_by_pull && (
                <>
                  {" with "}
                  <Link to={`/${repo.full_path}/pulls/${is.closed_by_pull}`} className="text-primary hover:underline">
                    #{is.closed_by_pull}
                  </Link>
                </>
              )}
            </span>
          )}
        </div>
      </div>
      <div className="grid gap-6 lg:grid-cols-[1fr_16rem]">
        <div className="space-y-4">
          <Card>
            <div className="flex items-center justify-between border-b px-3 py-2 text-sm">
              <span>
                <span className="font-medium">{is.author || "ghost"}</span> <span className="text-muted-foreground">{relativeTime(is.created_at)}</span>
              </span>
              {is.can_edit && editing !== "body" && (
                <Button variant="ghost" size="sm" onClick={() => (setBody(is.body), setEditing("body"))}>
                  Edit
                </Button>
              )}
            </div>
            <div className="p-3">
              {editing === "body" ? (
                <form className="space-y-2" onSubmit={(e) => (e.preventDefault(), update.mutate({ body }))}>
                  <Textarea aria-label="Description" className="min-h-32" value={body} onChange={(e) => setBody(e.target.value)} />
                  <div className="flex gap-2">
                    <Button type="submit" size="sm">
                      Save
                    </Button>
                    <Button type="button" size="sm" variant="ghost" onClick={() => setEditing(null)}>
                      Cancel
                    </Button>
                  </div>
                </form>
              ) : is.body ? (
                <Markdown text={is.body} fullPath={repo.full_path} />
              ) : (
                <p className="text-sm text-muted-foreground">No description.</p>
              )}
            </div>
          </Card>
          <Comments issue={is} onChange={refresh} onState={(state) => update.mutate({ state })} />
          <ErrorText error={update.error ?? remove.error} />
        </div>
        <aside className="space-y-4 text-sm">
          <IssueLabelsBox issue={is} base={base} onChange={refresh} />
          <AssigneesBox fullPath={repo.full_path} base={base} assignees={is.assignees} canSet={is.can_label} onChange={refresh} />
          <MilestoneBox fullPath={repo.full_path} base={base} milestone={is.milestone} canSet={is.can_label} onChange={refresh} />
          {is.can_delete && (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => {
                if (window.confirm(`Delete issue #${is.number}? This cannot be undone.`)) remove.mutate();
              }}
            >
              Delete issue
            </Button>
          )}
        </aside>
      </div>
    </div>
  );
}

function Comments({ issue, onChange, onState }: { issue: Issue; onChange: () => void; onState: (state: string) => void }) {
  const repo = useRepo();
  const comments = useIssueComments(repo.full_path, issue.number);
  const [text, setText] = useState("");
  const base = `${issuesBase(repo.full_path)}/${issue.number}/comments`;
  const add = useMutation({ mutationFn: () => api("POST", base, { body: text }), onSuccess: () => (setText(""), onChange()) });
  const remove = useMutation({ mutationFn: (id: string) => api("DELETE", `${base}/${id}`), onSuccess: onChange });
  return (
    <div className="space-y-4">
      {comments.data?.map((c) => (
        <Card key={c.id}>
          <div className="flex items-center justify-between border-b px-3 py-2 text-sm">
            <span>
              <span className="font-medium">{c.author || "ghost"}</span> <span className="text-muted-foreground">{relativeTime(c.created_at)}</span>
            </span>
            {c.can_delete && (
              <Button variant="ghost" size="sm" onClick={() => remove.mutate(c.id)}>
                Delete
              </Button>
            )}
          </div>
          <div className="p-3">
            <Markdown text={c.body} fullPath={repo.full_path} />
          </div>
        </Card>
      ))}
      {issue.can_comment && (
        <Card className="space-y-2 p-3">
          <Textarea aria-label="Comment" placeholder="Leave a comment (Markdown, @mentions, #123)" value={text} onChange={(e) => setText(e.target.value)} />
          <ErrorText error={add.error ?? remove.error} />
          <div className="flex flex-wrap gap-2">
            <Button onClick={() => add.mutate()} disabled={!text.trim() || add.isPending}>
              Comment
            </Button>
            {issue.can_edit && (
              <Button
                variant="outline"
                onClick={() => {
                  if (text.trim()) add.mutate();
                  onState(issue.state === "open" ? "closed" : "open");
                }}
              >
                {issue.state === "open" ? (text.trim() ? "Close with comment" : "Close issue") : "Reopen issue"}
              </Button>
            )}
          </div>
        </Card>
      )}
    </div>
  );
}

function IssueLabelsBox({ issue, base, onChange }: { issue: Issue; base: string; onChange: () => void }) {
  const repo = useRepo();
  const all = useLabels(repo.full_path);
  const set = useMutation({ mutationFn: (names: string[]) => api("PUT", `${base}/labels`, { labels: names }), onSuccess: onChange });
  const current = issue.labels.map((l) => l.name);
  return (
    <div className="space-y-2">
      <h3 className="font-medium">Labels</h3>
      <div className="flex flex-wrap gap-1">
        {issue.labels.length === 0 && <span className="text-muted-foreground">None</span>}
        {issue.labels.map((l) => (
          <LabelBadge key={l.id} label={l} />
        ))}
      </div>
      {issue.can_label && all.data && all.data.length > 0 && (
        <div className="space-y-1">
          {all.data.map((l) => (
            <label key={l.id} className="flex items-center gap-2">
              <input
                type="checkbox"
                checked={current.includes(l.name)}
                onChange={(e) => set.mutate(e.target.checked ? [...current, l.name] : current.filter((n) => n !== l.name))}
              />
              <LabelBadge label={l} />
            </label>
          ))}
        </div>
      )}
      <ErrorText error={set.error} />
    </div>
  );
}

export function MilestonesPage() {
  const repo = useRepo();
  const [params, setParams] = useSearchParams();
  const state = params.get("state") ?? "open";
  const milestones = useMilestones(repo.full_path, state);
  const canManage = atLeast(repo.role, "maintainer") && !repo.archived;
  const [creating, setCreating] = useState(false);
  return (
    <div className="max-w-3xl space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="flex-1 text-lg font-semibold">Milestones</h2>
        <div className="flex rounded-md border text-sm">
          {(["open", "closed"] as const).map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => setParams(s === "open" ? {} : { state: s })}
              className={cn("px-3 py-1.5 capitalize", state === s ? "bg-muted font-medium" : "text-muted-foreground hover:text-foreground")}
            >
              {s}
            </button>
          ))}
        </div>
        {canManage && !creating && <Button onClick={() => setCreating(true)}>New milestone</Button>}
      </div>
      {creating && <MilestoneForm onDone={() => setCreating(false)} />}
      {milestones.isError && <Alert variant="destructive">{milestones.error.message}</Alert>}
      {milestones.data?.length === 0 && <Card className="p-8 text-center text-sm text-muted-foreground">No {state} milestones.</Card>}
      {milestones.data?.map((m) => (
        <MilestoneRow key={m.id} milestone={m} canManage={canManage} />
      ))}
    </div>
  );
}

function MilestoneRow({ milestone: m, canManage }: { milestone: Milestone; canManage: boolean }) {
  const repo = useRepo();
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const base = `/repository/${repoId(repo.full_path)}/milestones/${m.id}`;
  const done = () => qc.invalidateQueries({ queryKey: ["milestones", repo.full_path] });
  const toggle = useMutation({ mutationFn: () => api("PATCH", base, { state: m.state === "open" ? "closed" : "open" }), onSuccess: done });
  const remove = useMutation({ mutationFn: () => api("DELETE", base), onSuccess: done });
  if (editing) return <MilestoneForm milestone={m} onDone={() => setEditing(false)} />;
  const pct = progress(m);
  return (
    <Card className="space-y-2 p-4">
      <div className="flex flex-wrap items-baseline gap-2">
        <Link to={`/${repo.full_path}/issues?milestone=${m.id}&state=all`} className="text-lg font-medium hover:text-primary hover:underline">
          {m.title}
        </Link>
        <span className="text-sm text-muted-foreground">{m.due_on ? `Due ${formatDate(m.due_on)}` : "No due date"}</span>
      </div>
      {m.description && <p className="text-sm text-muted-foreground">{m.description}</p>}
      <div className="h-2 overflow-hidden rounded bg-muted" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label={`${m.title} progress`}>
        <div className="h-full bg-green-600" style={{ width: `${pct}%` }} />
      </div>
      <p className="text-xs text-muted-foreground">
        {pct}% complete · {m.open} open · {m.closed} closed
      </p>
      {canManage && (
        <div className="flex gap-2">
          <Button size="sm" variant="outline" onClick={() => setEditing(true)}>
            Edit
          </Button>
          <Button size="sm" variant="outline" onClick={() => toggle.mutate()} disabled={toggle.isPending}>
            {m.state === "open" ? "Close" : "Reopen"}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              if (window.confirm(`Delete milestone ${m.title}? Its issues and pull requests keep going without it.`)) remove.mutate();
            }}
          >
            Delete
          </Button>
        </div>
      )}
      <ErrorText error={toggle.error ?? remove.error} />
    </Card>
  );
}

function MilestoneForm({ milestone, onDone }: { milestone?: Milestone; onDone: () => void }) {
  const repo = useRepo();
  const qc = useQueryClient();
  const [title, setTitle] = useState(milestone?.title ?? "");
  const [description, setDescription] = useState(milestone?.description ?? "");
  const [due, setDue] = useState(milestone?.due_on ? milestone.due_on.slice(0, 10) : "");
  const id = milestone?.id ?? "new";
  const save = useMutation({
    mutationFn: () => {
      const body: Record<string, unknown> = { title, description };
      if (due) body.due_on = new Date(due + "T00:00:00Z").toISOString();
      else if (milestone?.due_on) body.clear_due_on = true;
      const base = `/repository/${repoId(repo.full_path)}/milestones`;
      return milestone ? api("PATCH", `${base}/${milestone.id}`, body) : api("POST", base, body);
    },
    onSuccess: () => (qc.invalidateQueries({ queryKey: ["milestones", repo.full_path] }), onDone()),
  });
  return (
    <Card className="space-y-3 p-4">
      <div className="space-y-1">
        <FieldLabel htmlFor={`ms-title-${id}`}>Title</FieldLabel>
        <Input id={`ms-title-${id}`} value={title} onChange={(e) => setTitle(e.target.value)} />
      </div>
      <div className="space-y-1">
        <FieldLabel htmlFor={`ms-due-${id}`}>Due date</FieldLabel>
        <Input id={`ms-due-${id}`} type="date" value={due} onChange={(e) => setDue(e.target.value)} className="w-48" />
      </div>
      <div className="space-y-1">
        <FieldLabel htmlFor={`ms-desc-${id}`}>Description</FieldLabel>
        <Textarea id={`ms-desc-${id}`} value={description} onChange={(e) => setDescription(e.target.value)} />
      </div>
      <ErrorText error={save.error} />
      <div className="flex gap-2">
        <Button onClick={() => save.mutate()} disabled={!title.trim() || save.isPending}>
          {milestone ? "Save milestone" : "Create milestone"}
        </Button>
        <Button variant="ghost" onClick={onDone}>
          Cancel
        </Button>
      </div>
    </Card>
  );
}

// MilestonePage shows a milestone's issues (its links from issue pages).
export function MilestonePage() {
  const repo = useRepo();
  const { id = "" } = useParams();
  return <Navigate to={`/${repo.full_path}/issues?milestone=${encodeURIComponent(id)}&state=all`} replace />;
}
