import { ArrowRight, Download, ShieldCheck } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { DiffView } from "@/components/diff-view";
import { RefPicker, useRepo } from "@/components/repo-layout";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Select } from "@/components/ui/select";
import { ApiError, atLeast } from "@/lib/api";
import { ruleFor, ruleSummary, useBranchRules } from "@/lib/protection";
import { useStatuses, type Combined } from "@/lib/statuses";
import { CombinedIcon } from "@/components/status-icon";
import { encodePath, relativeTime, shortHash, summary, useCommit, useCommits, useCompare, useRefs, type Commit } from "@/lib/browse";

function CommitRow({ commit, status }: { commit: Commit; status?: Combined }) {
  const repo = useRepo();
  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <CombinedIcon combined={status} />
      <div className="min-w-0 flex-1">
        <Link to={`/${repo.full_path}/commit/${commit.hash}`} className="block truncate font-medium hover:text-primary hover:underline">
          {summary(commit.message)}
        </Link>
        <p className="text-xs text-muted-foreground">
          {commit.author.name} committed {relativeTime(commit.committer.when)}
        </p>
      </div>
      <Link to={`/${repo.full_path}/commit/${commit.hash}`} className="font-mono text-xs text-muted-foreground hover:underline">
        {shortHash(commit.hash)}
      </Link>
      <Link to={`/${repo.full_path}/tree/${commit.hash}`} className="text-xs text-primary hover:underline">
        Browse
      </Link>
    </div>
  );
}

export function CommitsPage() {
  const repo = useRepo();
  const spec = useParams()["*"] ?? "";
  const [page, setPage] = useState(0);
  const commits = useCommits(repo.full_path, spec, page);
  const statuses = useStatuses(repo.id, commits.data?.commits.map((c) => c.hash) ?? []);

  if (commits.isError) return <Alert variant="destructive">{commits.error instanceof ApiError && commits.error.status === 404 ? "No such branch, tag or path." : commits.error.message}</Alert>;
  if (!commits.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const { ref, path } = commits.data;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        {ref && <RefPicker current={ref} view="commits" path={path} />}
        {path && (
          <span className="text-sm text-muted-foreground">
            History of <code className="text-foreground">{path}</code>
          </span>
        )}
      </div>
      {commits.data.commits.length === 0 ? (
        <Card className="p-8 text-center text-sm text-muted-foreground">No commits.</Card>
      ) : (
        <Card className="divide-y">
          {commits.data.commits.map((c) => (
            <CommitRow key={c.hash} commit={c} status={statuses.data?.[c.hash]} />
          ))}
        </Card>
      )}
      <div className="flex justify-between">
        <Button variant="outline" size="sm" disabled={page === 0} onClick={() => setPage(page - 1)}>
          Newer
        </Button>
        <Button variant="outline" size="sm" disabled={!commits.data.more} onClick={() => setPage(page + 1)}>
          Older
        </Button>
      </div>
    </div>
  );
}

export function CommitPage() {
  const repo = useRepo();
  const { sha = "" } = useParams();
  const c = useCommit(repo.full_path, sha);

  if (c.isError) return <Alert variant="destructive">{c.error instanceof ApiError && c.error.status === 404 ? "No such commit." : c.error.message}</Alert>;
  if (!c.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const { commit, diff } = c.data;
  const [title, ...body] = commit.message.trim().split("\n");

  return (
    <div className="space-y-4">
      <Card className="space-y-3 p-5">
        <h2 className="text-lg font-semibold">{title}</h2>
        {body.join("\n").trim() && <pre className="whitespace-pre-wrap font-sans text-sm text-muted-foreground">{body.join("\n").trim()}</pre>}
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-muted-foreground">
          <span>
            <span className="font-medium text-foreground">{commit.author.name}</span> committed {relativeTime(commit.committer.when)}
          </span>
          <span className="font-mono text-xs">{commit.hash}</span>
          {commit.parents.map((p) => (
            <span key={p}>
              parent{" "}
              <Link to={`/${repo.full_path}/commit/${p}`} className="font-mono text-xs text-primary hover:underline">
                {shortHash(p)}
              </Link>
            </span>
          ))}
          <Link to={`/${repo.full_path}/tree/${commit.hash}`} className="ml-auto text-primary hover:underline">
            Browse files
          </Link>
        </div>
      </Card>
      <DiffView diff={diff} fullPath={repo.full_path} gitRef={commit.hash} />
    </div>
  );
}

export function BranchesPage() {
  const repo = useRepo();
  const refs = useRefs(repo.full_path);
  // Developers and up can read the rules.
  const rules = useBranchRules(repo.id, atLeast(repo.role, "developer"));
  const statuses = useStatuses(repo.id, refs.data?.branches.map((b) => b.commit.hash) ?? []);
  if (refs.isError) return <Alert variant="destructive">{refs.error.message}</Alert>;
  if (!refs.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const archive = (name: string, fmt: string) => `/${repo.full_path}/archive/${encodePath(name)}.${fmt}`;

  const section = (title: string, list: typeof refs.data.branches, branches: boolean) => (
    <section className="space-y-2">
      <h2 className="font-semibold">{title}</h2>
      {list.length === 0 ? (
        <p className="text-sm text-muted-foreground">None.</p>
      ) : (
        <Card className="divide-y">
          {list.map((r) => (
            <div key={r.name} className="flex flex-wrap items-center gap-3 px-4 py-2 text-sm">
              {branches && <CombinedIcon combined={statuses.data?.[r.commit.hash]} />}
              <Link to={`/${repo.full_path}/tree/${encodePath(r.name)}`} className="font-mono text-primary hover:underline">
                {r.name}
              </Link>
              {r.name === refs.data!.default_branch && <Badge>default</Badge>}
              {branches && rules.data && ruleFor(rules.data, r.name) && (
                <Badge title={`${ruleFor(rules.data, r.name)!.pattern}: ${ruleSummary(ruleFor(rules.data, r.name)!).join(", ")}`}>
                  <ShieldCheck className="h-3 w-3" /> protected
                </Badge>
              )}
              <span className="min-w-0 flex-1 truncate text-muted-foreground">
                {summary(r.commit.message)} · {relativeTime(r.commit.committer.when)}
              </span>
              {branches && r.name !== refs.data!.default_branch && (
                <Link to={`/${repo.full_path}/compare/${encodePath(refs.data!.default_branch)}...${encodePath(r.name)}`} className="text-xs text-primary hover:underline">
                  Compare
                </Link>
              )}
              <a href={archive(r.name, "zip")} className="inline-flex items-center gap-1 text-xs text-primary hover:underline">
                <Download className="h-3 w-3" /> zip
              </a>
              <a href={archive(r.name, "tar.gz")} className="text-xs text-primary hover:underline">
                tar.gz
              </a>
            </div>
          ))}
        </Card>
      )}
    </section>
  );

  return (
    <div className="space-y-6">
      {section("Branches", refs.data.branches, true)}
      {section("Tags", refs.data.tags, false)}
    </div>
  );
}

// splitCompare parses "base...head" (refs may contain slashes, not "...").
export function splitCompare(spec: string): [string, string] {
  const i = spec.indexOf("...");
  return i < 0 ? ["", ""] : [spec.slice(0, i), spec.slice(i + 3)];
}

export function ComparePage() {
  const repo = useRepo();
  const refs = useRefs(repo.full_path);
  const navigate = useNavigate();
  const [base, head] = splitCompare(useParams()["*"] ?? "");
  const cmp = useCompare(repo.full_path, base, head);
  const names = [...(refs.data?.branches ?? []), ...(refs.data?.tags ?? [])].map((r) => r.name);
  const go = (b: string, h: string) => navigate(`/${repo.full_path}/compare/${encodePath(b)}...${encodePath(h)}`);
  const effectiveBase = base || refs.data?.default_branch || "";

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Select aria-label="Base" value={effectiveBase} onChange={(e) => go(e.target.value, head || effectiveBase)}>
          {names.map((n) => (
            <option key={n}>{n}</option>
          ))}
        </Select>
        <ArrowRight className="h-4 w-4 text-muted-foreground" />
        <Select aria-label="Compare" value={head} onChange={(e) => go(effectiveBase, e.target.value)}>
          <option value="">choose a branch</option>
          {names.map((n) => (
            <option key={n}>{n}</option>
          ))}
        </Select>
      </div>
      {!head && <p className="text-sm text-muted-foreground">Choose a branch to see what it adds to {effectiveBase || "the base"}.</p>}
      {cmp.isError && <Alert variant="destructive">{cmp.error instanceof ApiError && cmp.error.status === 404 ? "No such branch, tag or commit." : cmp.error.message}</Alert>}
      {cmp.data &&
        (cmp.data.commits.length === 0 ? (
          <Card className="p-8 text-center text-sm text-muted-foreground">
            {head} has nothing that {base} doesn't have.
          </Card>
        ) : (
          <>
            <Card className="divide-y">
              {cmp.data.commits.map((c) => (
                <CommitRow key={c.hash} commit={c} />
              ))}
            </Card>
            {cmp.data.commits_truncated && <p className="text-sm text-muted-foreground">Only the first 250 commits are listed.</p>}
            <DiffView diff={cmp.data.diff} fullPath={repo.full_path} gitRef={head} />
          </>
        ))}
    </div>
  );
}
