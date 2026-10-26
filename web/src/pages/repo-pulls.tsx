import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, GitMerge, GitPullRequest, GitPullRequestClosed, GitPullRequestDraft, MessageSquare, ShieldCheck, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link, NavLink, useNavigate, useParams, useSearchParams } from "react-router";
import { DiffView } from "@/components/diff-view";
import { pageParam, Pager } from "@/components/pager";
import { LabelBadge } from "@/components/label-badge";
import { useRepo } from "@/components/repo-layout";
import { ErrorText } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Button, ButtonLink } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label as FieldLabel } from "@/components/ui/label";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api, ApiError, atLeast } from "@/lib/api";
import { safeLink, useCombined, useStatuses } from "@/lib/statuses";
import { CombinedIcon, StatusIcon } from "@/components/status-icon";
import { AssigneesBox, MilestoneBox, ReviewersBox } from "@/components/tracking";
import { useForks } from "@/components/forks";
import { useProfile } from "@/lib/auth";
import { relativeTime, shortHash, summary, useCompare, useRefs, type Comparison } from "@/lib/browse";
import { renderMarkdown } from "@/lib/markdown";
import { pullsBase, useLabels, usePull, usePullActivity, usePullCompare, usePulls, type Pull, type PullSummary } from "@/lib/pulls";
import { cn } from "@/lib/utils";

function StateIcon({ pr, className }: { pr: Pick<PullSummary, "state" | "draft">; className?: string }) {
  if (pr.state === "merged") return <GitMerge className={cn("text-purple-600 dark:text-purple-400", className)} />;
  if (pr.state === "closed") return <GitPullRequestClosed className={cn("text-red-600 dark:text-red-400", className)} />;
  if (pr.draft) return <GitPullRequestDraft className={cn("text-muted-foreground", className)} />;
  return <GitPullRequest className={cn("text-green-600 dark:text-green-400", className)} />;
}

export function StateBadge({ pr }: { pr: Pick<PullSummary, "state" | "draft"> }) {
  const text = pr.state === "open" ? (pr.draft ? "Draft" : "Open") : pr.state === "merged" ? "Merged" : "Closed";
  const color = pr.state === "merged" ? "bg-purple-600" : pr.state === "closed" ? "bg-red-600" : pr.draft ? "bg-muted-foreground" : "bg-green-600";
  return (
    <span className={cn("inline-flex items-center gap-1 rounded-full px-3 py-1 text-sm font-medium text-white", color)}>
      <StateIcon pr={pr} className="h-4 w-4 !text-white" /> {text}
    </span>
  );
}

function Markdown({ text, fullPath }: { text: string; fullPath: string }) {
  const html = useMemo(() => renderMarkdown(text, { fullPath, ref: "HEAD", dir: "", links: true }), [text, fullPath]);
  return <div className="markdown text-sm" dangerouslySetInnerHTML={{ __html: html }} />;
}

export function PullsPage() {
  const repo = useRepo();
  const [params, setParams] = useSearchParams();
  const state = params.get("state") ?? "open";
  const label = params.get("label") ?? "";
  const page = pageParam(params);
  const perPage = 30;
  const pulls = usePulls(repo.full_path, state, label, page, perPage);
  const statuses = useStatuses(repo.id, pulls.data?.pull_requests.map((p) => p.head_commit ?? "") ?? []);
  // Counts are per state for the whole repository; with a label we only
  // know whether more pages follow.
  const total = label || !pulls.data ? undefined : state === "all" ? Object.values(pulls.data.counts).reduce((a, b) => a + b, 0) : pulls.data.counts[state];
  const labels = useLabels(repo.full_path);
  const set = (k: string, v: string) => {
    const next = new URLSearchParams(params);
    if (v) next.set(k, v);
    else next.delete(k);
    if (k !== "page") next.delete("page");
    setParams(next);
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        {["open", "merged", "closed", "all"].map((s) => (
          <Button key={s} size="sm" variant={state === s ? "outline" : "ghost"} onClick={() => set("state", s === "open" ? "" : s)}>
            {s[0].toUpperCase() + s.slice(1)}
            {s !== "all" && pulls.data && <span className="text-muted-foreground">{pulls.data.counts[s] ?? 0}</span>}
          </Button>
        ))}
        {labels.data && labels.data.length > 0 && (
          <Select aria-label="Filter by label" value={label} onChange={(e) => set("label", e.target.value)}>
            <option value="">All labels</option>
            {labels.data.map((l) => (
              <option key={l.id}>{l.name}</option>
            ))}
          </Select>
        )}
        {atLeast(repo.role, "developer") && (
          <ButtonLink size="sm" className="ml-auto" to={`/${repo.full_path}/pulls/new`}>
            New pull request
          </ButtonLink>
        )}
      </div>
      {pulls.isError && <Alert variant="destructive">{pulls.error.message}</Alert>}
      {pulls.data &&
        (pulls.data.pull_requests.length === 0 ? (
          <Card className="p-8 text-center text-sm text-muted-foreground">No pull requests here.</Card>
        ) : (
          <Card className="divide-y">
            {pulls.data.pull_requests.map((p) => (
              <div key={p.number} className="flex items-start gap-3 px-4 py-3">
                <StateIcon pr={p} className="mt-0.5 h-4 w-4 shrink-0" />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <Link to={`/${repo.full_path}/pulls/${p.number}`} className="font-medium hover:text-primary hover:underline">
                      {p.title}
                    </Link>
                    {p.labels.map((l) => (
                      <LabelBadge key={l.id} label={l} />
                    ))}
                    {p.head_commit && <CombinedIcon combined={statuses.data?.[p.head_commit]} />}
                  </div>
                  <p className="text-xs text-muted-foreground">
                    #{p.number} by {p.author || "ghost"} · {p.source_repository ? `${p.source_repository}:` : ""}{p.source_branch} → {p.target_branch} · updated {relativeTime(p.updated_at)}
                  </p>
                </div>
              </div>
            ))}
          </Card>
        ))}
      {pulls.data && (
        <Pager page={page} perPage={perPage} count={pulls.data.pull_requests.length} total={total} onPage={(p) => set("page", String(p + 1))} />
      )}
    </div>
  );
}

export function NewPullPage() {
  const repo = useRepo();
  const refs = useRefs(repo.full_path);
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const branches = refs.data?.branches.map((b) => b.name) ?? [];
  const [target, setTarget] = useState(params.get("target") ?? "");
  const [source, setSource] = useState(params.get("source") ?? "");
  // The source branch may be in a fork of this repository.
  const [from, setFrom] = useState(params.get("source_repository") ?? "");
  const forks = useForks(repo.id);
  const fromFork = from !== "" && from !== repo.full_path;
  const forkRefs = useRefs(fromFork ? from : repo.full_path);
  const sourceBranches = fromFork ? (forkRefs.data?.branches.map((b) => b.name) ?? []) : branches;
  const base = target || refs.data?.default_branch || "";
  const sameRepo = useCompare(repo.full_path, fromFork ? "" : base, fromFork ? "" : source);
  const acrossForks = useQuery({
    queryKey: ["pull-preview", repo.full_path, from, source, base],
    queryFn: () =>
      api<Comparison>("GET", `${pullsBase(repo.full_path)}/preview?source_repository=${encodeURIComponent(from)}&source=${encodeURIComponent(source)}&target=${encodeURIComponent(base)}`),
    enabled: fromFork && !!source && !!base,
    retry: false,
  });
  const cmp = fromFork ? acrossForks : sameRepo;
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [draft, setDraft] = useState(false);
  const create = useMutation({
    mutationFn: () =>
      api<Pull>("POST", pullsBase(repo.full_path), {
        title: title || summary(cmp.data?.commits.at(-1)?.message ?? source), body, source, target: base, draft,
        ...(fromFork ? { source_repository: from } : {}),
      }),
    onSuccess: (pr) => navigate(`/${repo.full_path}/pulls/${pr.number}`),
  });

  return (
    <div className="space-y-4">
      <h2 className="text-lg font-semibold">New pull request</h2>
      {repo.fork_of && !fromFork && (
        <Alert>
          This is a fork.{" "}
          <Link to={`/${repo.fork_of.full_path}/pulls/new?source_repository=${encodeURIComponent(repo.full_path)}`} className="text-primary hover:underline">
            Open a pull request into {repo.fork_of.full_path}
          </Link>{" "}
          to propose your changes there.
        </Alert>
      )}
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span>Merge</span>
        {(forks.data?.length ?? 0) > 0 || fromFork ? (
          <Select aria-label="Source repository" value={fromFork ? from : ""} onChange={(e) => (setFrom(e.target.value), setSource(""))}>
            <option value="">{repo.full_path}</option>
            {fromFork && !forks.data?.some((f) => f.full_path === from) && <option value={from}>{from}</option>}
            {forks.data?.map((f) => (
              <option key={f.id} value={f.full_path}>
                {f.full_path}
              </option>
            ))}
          </Select>
        ) : null}
        <Select aria-label="Source branch" value={source} onChange={(e) => setSource(e.target.value)}>
          <option value="">choose a branch</option>
          {sourceBranches.filter((b) => fromFork || b !== base).map((b) => (
            <option key={b}>{b}</option>
          ))}
        </Select>
        <span>into</span>
        <Select aria-label="Target branch" value={base} onChange={(e) => setTarget(e.target.value)}>
          {branches.map((b) => (
            <option key={b}>{b}</option>
          ))}
        </Select>
      </div>
      {cmp.data && cmp.data.commits.length === 0 && <Alert>{source} has nothing that {base} doesn't have.</Alert>}
      {cmp.data && cmp.data.commits.length > 0 && (
        <>
          <Card className="space-y-3 p-5">
            <div className="space-y-1.5">
              <FieldLabel htmlFor="pr-title">Title</FieldLabel>
              <Input id="pr-title" placeholder={summary(cmp.data.commits.at(-1)!.message)} value={title} onChange={(e) => setTitle(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <FieldLabel htmlFor="pr-body">Description</FieldLabel>
              <Textarea id="pr-body" className="min-h-32" placeholder="What does it change, and why? Markdown, @mentions and #123 work." value={body} onChange={(e) => setBody(e.target.value)} />
            </div>
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={draft} onChange={(e) => setDraft(e.target.checked)} /> Draft (can't be merged until marked ready)
            </label>
            <ErrorText error={create.error} />
            <Button onClick={() => create.mutate()} disabled={create.isPending}>
              Create pull request
            </Button>
          </Card>
          <p className="text-sm text-muted-foreground">
            {cmp.data.commits.length} {cmp.data.commits.length === 1 ? "commit" : "commits"}
          </p>
          <DiffView diff={cmp.data.diff} fullPath={repo.full_path} gitRef={source} />
        </>
      )}
    </div>
  );
}

export function PullPage() {
  const repo = useRepo();
  const n = Number(useParams().number);
  const pr = usePull(repo.full_path, n);
  const tab = useParams()["tab"] ?? "";
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState("");
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["pull", repo.full_path, n] });
    qc.invalidateQueries({ queryKey: ["pull-activity", repo.full_path, n] });
    qc.invalidateQueries({ queryKey: ["pulls", repo.full_path] });
  };
  const rename = useMutation({ mutationFn: () => api("PATCH", `${pullsBase(repo.full_path)}/${n}`, { title }), onSuccess: () => (setEditing(false), refresh()) });

  if (pr.error instanceof ApiError && pr.error.status === 404) return <Alert variant="destructive">No such pull request.</Alert>;
  if (pr.isError) return <Alert variant="destructive">{pr.error.message}</Alert>;
  if (!pr.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const p = pr.data;
  const base = `/${repo.full_path}/pulls/${n}`;
  const tabClass = ({ isActive }: { isActive: boolean }) =>
    cn("border-b-2 px-3 py-2 text-sm", isActive ? "border-primary font-medium" : "border-transparent text-muted-foreground hover:text-foreground");

  return (
    <div className="space-y-4">
      <div className="space-y-2">
        {editing ? (
          <form className="flex gap-2" onSubmit={(e) => (e.preventDefault(), rename.mutate())}>
            <Input aria-label="Title" value={title} onChange={(e) => setTitle(e.target.value)} autoFocus />
            <Button type="submit">Save</Button>
            <Button type="button" variant="ghost" onClick={() => setEditing(false)}>
              Cancel
            </Button>
          </form>
        ) : (
          <h2 className="flex flex-wrap items-center gap-2 text-2xl">
            <span>{p.title}</span> <span className="text-muted-foreground">#{p.number}</span>
            {p.can_edit && (
              <Button variant="ghost" size="sm" onClick={() => (setTitle(p.title), setEditing(true))}>
                Edit
              </Button>
            )}
          </h2>
        )}
        <div className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
          <StateBadge pr={p} />
          <span>
            <span className="font-medium text-foreground">{p.author || "ghost"}</span> wants to merge{" "}
            <code className="text-foreground">{p.source_repository ? `${p.source_repository}:${p.source_branch}` : p.source_branch}</code> into{" "}
            <code className="text-foreground">{p.target_branch}</code>
          </span>
          {p.labels.map((l) => (
            <LabelBadge key={l.id} label={l} />
          ))}
        </div>
      </div>
      <nav className="flex gap-1 border-b">
        <NavLink to={base} end className={tabClass}>
          Conversation
        </NavLink>
        <NavLink to={`${base}/commits`} className={tabClass}>
          Commits
        </NavLink>
        <NavLink to={`${base}/files`} className={tabClass}>
          Files changed
        </NavLink>
      </nav>
      {tab === "commits" ? <PullCommits n={n} /> : tab === "files" ? <PullFiles pr={p} onChange={refresh} /> : <Conversation pr={p} onChange={refresh} />}
    </div>
  );
}

function PullCommits({ n }: { n: number }) {
  const repo = useRepo();
  const cmp = usePullCompare(repo.full_path, n);
  if (!cmp.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  return (
    <Card className="divide-y">
      {cmp.data.commits.map((c) => (
        <div key={c.hash} className="flex items-center gap-3 px-4 py-2 text-sm">
          <Link to={`/${repo.full_path}/commit/${c.hash}`} className="min-w-0 flex-1 truncate hover:underline">
            {summary(c.message)}
          </Link>
          <span className="text-xs text-muted-foreground">{c.author.name}</span>
          <span className="font-mono text-xs text-muted-foreground">{shortHash(c.hash)}</span>
        </div>
      ))}
    </Card>
  );
}

function PullFiles({ pr, onChange }: { pr: Pull; onChange: () => void }) {
  const repo = useRepo();
  const { data: profile } = useProfile();
  const cmp = usePullCompare(repo.full_path, pr.number);
  const activity = usePullActivity(repo.full_path, pr.number);
  if (!cmp.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  return (
    <DiffView
      diff={cmp.data.diff}
      fullPath={repo.full_path}
      gitRef={pr.head_commit || pr.source_branch}
      inline={{
        comments: activity.data?.comments.filter((c) => c.path) ?? [],
        canComment: !!profile && pr.state === "open" && !repo.archived,
        onSubmit: (target, body) => api("POST", `${pullsBase(repo.full_path)}/${pr.number}/comments`, { body, inline: target }).then(onChange),
      }}
    />
  );
}

type TimelineItem = { at: string; key: string; node: ReactNode };

function Conversation({ pr, onChange }: { pr: Pull; onChange: () => void }) {
  const repo = useRepo();
  const { data: profile } = useProfile();
  const activity = usePullActivity(repo.full_path, pr.number);
  const [comment, setComment] = useState("");
  const post = useMutation({
    mutationFn: () => api("POST", `${pullsBase(repo.full_path)}/${pr.number}/comments`, { body: comment }),
    onSuccess: () => (setComment(""), onChange()),
  });
  const remove = useMutation({ mutationFn: (id: string) => api("DELETE", `${pullsBase(repo.full_path)}/${pr.number}/comments/${id}`), onSuccess: onChange });

  const items: TimelineItem[] = [];
  for (const r of activity.data?.reviews ?? []) {
    const icon = r.state === "approved" ? <Check className="h-4 w-4 text-green-600" /> : r.state === "changes_requested" ? <X className="h-4 w-4 text-red-600" /> : <MessageSquare className="h-4 w-4" />;
    const verb = r.state === "approved" ? "approved these changes" : r.state === "changes_requested" ? "requested changes" : "reviewed";
    items.push({
      at: r.created_at,
      key: "r" + r.id,
      node: (
        <div className="space-y-2">
          <p className="flex items-center gap-2 text-sm">
            {icon} <span className="font-medium">{r.author || "ghost"}</span> {verb} {relativeTime(r.created_at)}
          </p>
          {r.body && (
            <Card className="ml-6 p-3">
              <Markdown text={r.body} fullPath={repo.full_path} />
            </Card>
          )}
        </div>
      ),
    });
  }
  for (const c of activity.data?.comments ?? []) {
    if (c.review_id) continue; // shown with the files
    items.push({
      at: c.created_at,
      key: "c" + c.id,
      node: (
        <Card className="overflow-hidden">
          <div className="flex items-center gap-2 border-b bg-muted/40 px-3 py-1.5 text-xs text-muted-foreground">
            <span className="font-medium text-foreground">{c.author || "ghost"}</span> commented {relativeTime(c.created_at)}
            {c.path && (
              <Link to={`/${repo.full_path}/pulls/${pr.number}/files`} className="font-mono hover:underline">
                on {c.path}:{c.line}
              </Link>
            )}
            {c.can_delete && (
              <button className="ml-auto hover:text-destructive" onClick={() => remove.mutate(c.id)}>
                Delete
              </button>
            )}
          </div>
          <div className="p-3">
            <Markdown text={c.body} fullPath={repo.full_path} />
          </div>
        </Card>
      ),
    });
  }
  if (pr.merged_at)
    items.push({ at: pr.merged_at, key: "merged", node: <p className="flex items-center gap-2 text-sm"><GitMerge className="h-4 w-4 text-purple-600" /> <span className="font-medium">{pr.merged_by || "ghost"}</span> merged commit <Link className="font-mono hover:underline" to={`/${repo.full_path}/commit/${pr.merge_commit}`}>{shortHash(pr.merge_commit ?? "")}</Link> ({pr.merge_method}) {relativeTime(pr.merged_at)}</p> });
  if (pr.closed_at && pr.state === "closed")
    items.push({ at: pr.closed_at, key: "closed", node: <p className="flex items-center gap-2 text-sm"><GitPullRequestClosed className="h-4 w-4 text-red-600" /> Closed {relativeTime(pr.closed_at)}</p> });
  items.sort((a, b) => a.at.localeCompare(b.at));

  return (
    <div className="grid gap-6 lg:grid-cols-[1fr_16rem]">
      <div className="min-w-0 space-y-4">
        <Card className="overflow-hidden">
          <div className="border-b bg-muted/40 px-3 py-1.5 text-xs text-muted-foreground">
            <span className="font-medium text-foreground">{pr.author || "ghost"}</span> opened this {relativeTime(pr.created_at)}
          </div>
          <div className="p-3">{pr.body ? <Markdown text={pr.body} fullPath={repo.full_path} /> : <p className="text-sm text-muted-foreground">No description.</p>}</div>
        </Card>
        {items.map((i) => (
          <div key={i.key}>{i.node}</div>
        ))}
        {pr.state === "open" && <MergeBox pr={pr} onChange={onChange} />}
        {profile && !repo.archived && (
          <Card className="space-y-2 p-3">
            <Textarea aria-label="Comment" placeholder="Leave a comment (Markdown, @mentions, #123)" value={comment} onChange={(e) => setComment(e.target.value)} />
            <ErrorText error={post.error ?? remove.error} />
            <div className="flex justify-end gap-2">
              <StateButtons pr={pr} onChange={onChange} />
              <Button size="sm" disabled={!comment.trim() || post.isPending} onClick={() => post.mutate()}>
                Comment
              </Button>
            </div>
          </Card>
        )}
      </div>
      <aside className="space-y-4 text-sm">
        <ReviewBox pr={pr} onChange={onChange} />
        <LabelsBox pr={pr} onChange={onChange} />
        <MaintainerEdits pr={pr} onChange={onChange} />
        <ReviewersBox fullPath={repo.full_path} base={`${pullsBase(repo.full_path)}/${pr.number}`} reviewers={pr.requested_reviewers ?? []} author={pr.author}
          canSet={pr.state === "open" && !repo.archived && (atLeast(repo.role, "developer") || (!!profile && profile.username === pr.author))} onChange={onChange} />
        <AssigneesBox fullPath={repo.full_path} base={`${pullsBase(repo.full_path)}/${pr.number}`} assignees={pr.assignees ?? []}
          canSet={atLeast(repo.role, "developer") && !repo.archived} onChange={onChange} />
        <MilestoneBox fullPath={repo.full_path} base={`${pullsBase(repo.full_path)}/${pr.number}`} milestone={pr.milestone}
          canSet={atLeast(repo.role, "developer") && !repo.archived} onChange={onChange} />
      </aside>
    </div>
  );
}

function StateButtons({ pr, onChange }: { pr: Pull; onChange: () => void }) {
  const repo = useRepo();
  const act = useMutation({ mutationFn: (what: string) => api("POST", `${pullsBase(repo.full_path)}/${pr.number}/${what}`), onSuccess: onChange });
  if (!pr.can_edit) return null;
  if (pr.state === "open")
    return (
      <Button size="sm" variant="outline" onClick={() => act.mutate("close")}>
        Close pull request
      </Button>
    );
  if (pr.state === "closed" && pr.branches_exist)
    return (
      <Button size="sm" variant="outline" onClick={() => act.mutate("reopen")}>
        Reopen
      </Button>
    );
  return null;
}

function MergeBox({ pr, onChange }: { pr: Pull; onChange: () => void }) {
  const repo = useRepo();
  const [method, setMethod] = useState("merge");
  const [deleteBranch, setDeleteBranch] = useState(true);
  const merge = useMutation({ mutationFn: () => api("POST", `${pullsBase(repo.full_path)}/${pr.number}/merge`, { method, delete_branch: deleteBranch }), onSuccess: onChange });
  const ready = useMutation({ mutationFn: () => api("PATCH", `${pullsBase(repo.full_path)}/${pr.number}`, { draft: false }), onSuccess: onChange });

  const checks: { ok: boolean; text: string }[] = [];
  if (!pr.branches_exist) checks.push({ ok: false, text: "A branch of this pull request no longer exists." });
  else if (pr.mergeable === false && pr.conflicts.length === 0) checks.push({ ok: false, text: `${pr.target_branch} already has every commit of ${pr.source_branch}.` });
  else if (pr.conflicts.length > 0) checks.push({ ok: false, text: `Conflicts in ${pr.conflicts.join(", ")}. Merge ${pr.target_branch} into ${pr.source_branch} locally and push.` });
  else checks.push({ ok: true, text: "No conflicts with the target branch." });
  checks.push({
    ok: pr.approved.length >= pr.required_approvals,
    text:
      pr.required_approvals > 0
        ? `${pr.approved.length} of ${pr.required_approvals} required approvals.`
        : `${pr.approved.length} ${pr.approved.length === 1 ? "approval" : "approvals"}.`,
  });
  if (pr.changes_requested.length) checks.push({ ok: false, text: `Changes requested by ${pr.changes_requested.join(", ")}.` });
  if (pr.draft) checks.push({ ok: false, text: "This is a draft." });
  if (pr.merge_blocked?.type === "BRANCH_PROTECTED") checks.push({ ok: false, text: pr.merge_blocked.message + "." });
  const required = pr.required_checks ?? [];
  if (required.length) {
    const passing = required.filter((c) => c.state === "success").length;
    checks.push({ ok: passing === required.length, text: `${passing} of ${required.length} required checks passed.` });
  }
  // An owner may merge although a check fails, when the rule lets them.
  const bypassing = pr.can_merge && pr.protected_by && checks.some((c) => !c.ok);

  return (
    <Card className="space-y-3 p-4">
      <ul className="space-y-1 text-sm">
        {checks.map((c, i) => (
          <li key={i} className="flex items-start gap-2">
            {c.ok ? <Check className="mt-0.5 h-4 w-4 text-green-600" /> : <X className="mt-0.5 h-4 w-4 text-red-600" />}
            {c.text}
          </li>
        ))}
      </ul>
      <Checks pr={pr} />
      {pr.protected_by && (
        <p className="flex items-center gap-1 text-xs text-muted-foreground">
          <ShieldCheck className="h-3 w-3 shrink-0" />
          <span>
            {pr.target_branch} is protected by the rule <code className="rounded bg-muted px-1">{pr.protected_by}</code>.
            {bypassing && " You can merge anyway as an owner; this is recorded."}
          </span>
        </p>
      )}
      {pr.draft && pr.can_edit && (
        <Button size="sm" variant="outline" onClick={() => ready.mutate()}>
          Ready for review
        </Button>
      )}
      {atLeast(repo.role, "maintainer") && (
        <div className="flex flex-wrap items-center gap-2">
          <Select aria-label="Merge method" value={method} onChange={(e) => setMethod(e.target.value)}>
            <option value="merge">Create a merge commit</option>
            <option value="squash">Squash and merge</option>
            <option value="rebase">Rebase and merge</option>
          </Select>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={deleteBranch} onChange={(e) => setDeleteBranch(e.target.checked)} /> Delete {pr.source_branch}
          </label>
          <Button disabled={!pr.can_merge || merge.isPending} onClick={() => merge.mutate()}>
            <GitMerge className="h-4 w-4" /> Merge
          </Button>
        </div>
      )}
      <ErrorText error={merge.error ?? ready.error} />
    </Card>
  );
}

// Checks lists the head commit's statuses (latest per context), required
// ones first, including required checks nobody reported yet.
function Checks({ pr }: { pr: Pull }) {
  const repo = useRepo();
  const qc = useQueryClient();
  const combined = useCombined(repo.id, pr.state === "open" ? pr.head_commit : undefined);
  // The checks refresh on their own while some are pending; when they
  // change, reload the pull request too, so the merge box follows.
  const signature = (combined.data?.statuses ?? []).map((s) => s.context + "=" + s.state).join(",");
  const seen = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!combined.data) return;
    if (seen.current !== undefined && seen.current !== signature) qc.invalidateQueries({ queryKey: ["pull", repo.full_path, pr.number] });
    seen.current = signature;
  }, [signature, combined.data, qc, repo.full_path, pr.number]);
  const required = new Set((pr.required_checks ?? []).map((c) => c.context));
  const reported = combined.data?.statuses ?? [];
  const missing = [...required].filter((ctx) => !reported.some((s) => s.context === ctx));
  if (reported.length === 0 && missing.length === 0) return null;
  const rows = [...reported].sort((a, b) => Number(required.has(b.context)) - Number(required.has(a.context)) || a.context.localeCompare(b.context));
  return (
    <div className="space-y-1 rounded-md border p-2 text-sm">
      <p className="text-xs font-medium text-muted-foreground">Checks</p>
      <ul className="space-y-1">
        {rows.map((s) => {
          const link = safeLink(s.target_url);
          return (
            <li key={s.context} className="flex items-center gap-2">
              <StatusIcon state={s.state} />
              <span className="font-medium">{s.context}</span>
              {required.has(s.context) && <span className="rounded border px-1 text-[10px] uppercase text-muted-foreground">required</span>}
              <span className="min-w-0 flex-1 truncate text-muted-foreground">{s.description}</span>
              {link && (
                <a href={link} target="_blank" rel="noopener noreferrer" className="text-xs text-primary hover:underline">
                  Details
                </a>
              )}
            </li>
          );
        })}
        {missing.map((ctx) => (
          <li key={ctx} className="flex items-center gap-2">
            <StatusIcon state="pending" title="not reported" />
            <span className="font-medium">{ctx}</span>
            <span className="rounded border px-1 text-[10px] uppercase text-muted-foreground">required</span>
            <span className="text-muted-foreground">Waiting for a report.</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

function ReviewBox({ pr, onChange }: { pr: Pull; onChange: () => void }) {
  const repo = useRepo();
  const [state, setState] = useState("approved");
  const [body, setBody] = useState("");
  const review = useMutation({
    mutationFn: () => api("POST", `${pullsBase(repo.full_path)}/${pr.number}/reviews`, { state, body }),
    onSuccess: () => (setBody(""), onChange()),
  });
  return (
    <div className="space-y-2">
      <h3 className="font-medium">Reviews</h3>
      {pr.approved.length === 0 && pr.changes_requested.length === 0 && <p className="text-muted-foreground">None yet.</p>}
      {pr.approved.map((u) => (
        <p key={u} className="flex items-center gap-1">
          <Check className="h-4 w-4 text-green-600" /> {u}
        </p>
      ))}
      {pr.changes_requested.map((u) => (
        <p key={u} className="flex items-center gap-1">
          <X className="h-4 w-4 text-red-600" /> {u}
        </p>
      ))}
      {pr.can_review && (
        <div className="space-y-2 rounded-md border p-2">
          <Select aria-label="Review" value={state} onChange={(e) => setState(e.target.value)} className="w-full">
            <option value="approved">Approve</option>
            <option value="changes_requested">Request changes</option>
            <option value="commented">Comment</option>
          </Select>
          <Textarea aria-label="Review comment" placeholder="Optional summary" value={body} onChange={(e) => setBody(e.target.value)} />
          <ErrorText error={review.error} />
          <Button size="sm" className="w-full" disabled={review.isPending} onClick={() => review.mutate()}>
            Submit review
          </Button>
        </div>
      )}
    </div>
  );
}

// MaintainerEdits lets a fork pull request's author allow this
// repository's developers to push to the source branch.
function MaintainerEdits({ pr, onChange }: { pr: Pull; onChange: () => void }) {
  const repo = useRepo();
  const { data: me } = useProfile();
  const set = useMutation({
    mutationFn: (allow: boolean) => api("PATCH", `${pullsBase(repo.full_path)}/${pr.number}`, { allow_maintainer_edits: allow }),
    onSuccess: onChange,
  });
  if (!pr.source_repository || pr.state !== "open") return null;
  const isAuthor = !!me && me.username === pr.author;
  return (
    <div className="space-y-1">
      <h3 className="font-medium">From a fork</h3>
      {isAuthor ? (
        <label className="flex items-start gap-2">
          <input type="checkbox" className="mt-1" checked={pr.allow_maintainer_edits} disabled={set.isPending} onChange={(e) => set.mutate(e.target.checked)} />
          <span>Allow edits by maintainers ({repo.full_path}'s developers can push to {pr.source_branch})</span>
        </label>
      ) : (
        <p className="text-muted-foreground">
          {pr.allow_maintainer_edits ? `Developers here can push to ${pr.source_repository}:${pr.source_branch}.` : "The author doesn't allow edits by maintainers."}
        </p>
      )}
      <ErrorText error={set.error} />
    </div>
  );
}

function LabelsBox({ pr, onChange }: { pr: Pull; onChange: () => void }) {
  const repo = useRepo();
  const all = useLabels(repo.full_path);
  const set = useMutation({
    mutationFn: (names: string[]) => api("PUT", `${pullsBase(repo.full_path)}/${pr.number}/labels`, { labels: names }),
    onSuccess: onChange,
  });
  const canSet = atLeast(repo.role, "developer");
  const current = pr.labels.map((l) => l.name);
  return (
    <div className="space-y-2">
      <h3 className="font-medium">Labels</h3>
      <div className="flex flex-wrap gap-1">
        {pr.labels.length === 0 && <span className="text-muted-foreground">None</span>}
        {pr.labels.map((l) => (
          <LabelBadge key={l.id} label={l} />
        ))}
      </div>
      {canSet && all.data && all.data.length > 0 && (
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
      {canSet && all.data?.length === 0 && atLeast(repo.role, "maintainer") && (
        <Link to={`/${repo.full_path}/settings#labels`} className="text-primary hover:underline">
          Create labels
        </Link>
      )}
      <ErrorText error={set.error} />
    </div>
  );
}

