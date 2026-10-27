import { Archive, ChevronDown, CircleDot, Code2, Copy, GitBranch, GitCommitHorizontal, GitCompare, GitPullRequest, Globe, Lock, Package, PlayCircle, Settings, Tag } from "lucide-react";
import { ForkButton, ForkedFrom, SyncButton, WatchSelect } from "@/components/forks";
import { useState } from "react";
import { Link, NavLink, Outlet, useLocation, useNavigate, useOutletContext, useParams } from "react-router";
import { ApiError, atLeast, type Repository } from "@/lib/api";
import { useProfile } from "@/lib/auth";
import { encodePath, useRefs } from "@/lib/browse";
import { useInstance } from "@/lib/queries";
import { useWorkflows } from "@/lib/actions";
import { useGoModule, useRepoPackages } from "@/lib/packages";
import { useReleaseCount } from "@/lib/releases";
import { cn } from "@/lib/utils";
import { useRepository } from "@/pages/repo";
import { NamespacePage } from "@/pages/namespace";
import { NotFoundPage } from "@/pages/not-found";
import { CopyField } from "./copy-field";
import { APIImportBanner, MirrorBanner } from "./mirror-banner";
import { NamespacePath } from "./namespace-path";
import { Alert } from "./ui/alert";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Card } from "./ui/card";
import { Select } from "./ui/select";

export type RepoContext = { repo: Repository };
export const useRepo = () => useOutletContext<RepoContext>().repo;

// cloneURLs builds the HTTPS and (if enabled) SSH clone URLs for this server.
export function cloneURLs(fullPath: string, location: { origin: string; hostname: string }, ssh?: { enabled: boolean; port?: number }) {
  const urls = [{ label: "HTTPS", url: `${location.origin}/${fullPath}.git` }];
  if (ssh?.enabled) {
    const port = ssh.port && ssh.port !== 22 ? `:${ssh.port}` : "";
    urls.push({ label: "SSH", url: `ssh://git@${location.hostname}${port}/${fullPath}.git` });
  }
  return urls;
}

// RepoLayout is a repository's page: /:owner/:repo, or path for a
// sub-group's repository (see nestedRoutes in app.tsx).
export function RepoLayout({ path: nested }: { path?: string } = {}) {
  const params = useParams();
  const loc = useLocation();
  const path = nested ?? `${params.owner}/${params.repo}`;
  const r = useRepository(path);
  const { data: profile } = useProfile();

  if (r.error instanceof ApiError && r.error.status === 404) {
    // No repository here: maybe a sub-group (org/team).
    const notFound = profile ? (
      <NotFoundPage />
    ) : (
      <div className="pt-16 text-center">
        <h1 className="text-2xl font-semibold">Repository not found</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          If it is private,{" "}
          <Link to={`/login?next=${encodeURIComponent(location.pathname)}`} className="text-primary hover:underline">
            sign in
          </Link>{" "}
          to see it.
        </p>
      </div>
    );
    return loc.pathname.split("/").filter(Boolean).length === path.split("/").length ? <NamespacePage path={path} notFound={notFound} /> : notFound;
  }
  if (r.isError) return <Alert variant="destructive">{r.error.message}</Alert>;
  if (!r.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const repo = r.data;

  const tab = ({ isActive }: { isActive: boolean }) =>
    cn("flex items-center gap-1.5 border-b-2 px-3 py-2 text-sm", isActive ? "border-primary font-medium" : "border-transparent text-muted-foreground hover:text-foreground");
  const base = `/${repo.full_path}`;
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl">
          <NamespacePath path={repo.namespace} />
          <span className="text-muted-foreground"> / </span>
          <Link to={base} className="font-semibold hover:underline">
            {repo.name}
          </Link>
        </h1>
        <Badge>
          {repo.is_private ? <Lock className="h-3 w-3" /> : <Globe className="h-3 w-3" />}
          {repo.is_private ? "private" : "public"}
        </Badge>
        {repo.archived && (
          <Badge>
            <Archive className="h-3 w-3" /> archived
          </Badge>
        )}
        <div className="ml-auto flex gap-2">
          <WatchSelect repo={repo} />
          <ForkButton repo={repo} />
          <CloneMenu repo={repo} />
        </div>
      </div>
      <ForkedFrom repo={repo} />
      <MirrorBanner repo={repo} />
      <APIImportBanner repo={repo} />
      {repo.description && <p className="text-muted-foreground">{repo.description}</p>}
      <SyncButton repo={repo} />
      {repo.archived && <Alert>This repository is archived: it can be cloned but not pushed to.</Alert>}
      <nav className="flex gap-1 overflow-x-auto border-b">
        <NavLink to={base} end className={({ isActive }) => tab({ isActive: isActive || /\/(tree|blob|blame)\//.test(location.pathname) })}>
          <Code2 className="h-4 w-4" /> Code
        </NavLink>
        {repo.has_issues !== false && (
          <NavLink to={`${base}/issues`} className={({ isActive }) => tab({ isActive: isActive || location.pathname.startsWith(`${base}/milestones`) })}>
            <CircleDot className="h-4 w-4" /> Issues
          </NavLink>
        )}
        <NavLink to={`${base}/pulls`} className={tab}>
          <GitPullRequest className="h-4 w-4" /> Pull requests
        </NavLink>
        <NavLink to={`${base}/commits`} className={({ isActive }) => tab({ isActive: isActive || location.pathname.startsWith(`${base}/commit/`) })}>
          <GitCommitHorizontal className="h-4 w-4" /> Commits
        </NavLink>
        <NavLink to={`${base}/branches`} className={tab}>
          <GitBranch className="h-4 w-4" /> Branches
        </NavLink>
        <NavLink to={`${base}/compare`} className={tab}>
          <GitCompare className="h-4 w-4" /> Compare
        </NavLink>
        <NavLink to={`${base}/releases`} className={tab}>
          <Tag className="h-4 w-4" /> Releases <ReleaseCount fullPath={repo.full_path} />
        </NavLink>
        <ActionsTab fullPath={repo.full_path} base={base} tab={tab} />
        <PackagesTab fullPath={repo.full_path} base={base} tab={tab} />
        {atLeast(repo.role, "maintainer") && (
          <NavLink to={`${base}/settings`} className={tab}>
            <Settings className="h-4 w-4" /> Settings
          </NavLink>
        )}
      </nav>
      <Outlet context={{ repo } satisfies RepoContext} />
    </div>
  );
}

// The Actions tab shows once the repository has workflows or runs.
function ActionsTab({ fullPath, base, tab }: { fullPath: string; base: string; tab: (p: { isActive: boolean }) => string }) {
  const workflows = useWorkflows(fullPath);
  if (!workflows.data?.uses_actions) return null;
  return (
    <NavLink to={`${base}/actions`} className={tab}>
      <PlayCircle className="h-4 w-4" /> Actions
    </NavLink>
  );
}

// The Packages tab shows once packages are linked to the repository, or
// it serves a Go module.
function PackagesTab({ fullPath, base, tab }: { fullPath: string; base: string; tab: (p: { isActive: boolean }) => string }) {
  const list = useRepoPackages(fullPath);
  const goModule = useGoModule(fullPath, true);
  if (!list.data?.length && !goModule.data) return null;
  return (
    <NavLink to={`${base}/packages`} className={tab}>
      <Package className="h-4 w-4" /> Packages
    </NavLink>
  );
}

function ReleaseCount({ fullPath }: { fullPath: string }) {
  const count = useReleaseCount(fullPath);
  if (!count.data) return null;
  return (
    <span className="rounded-full bg-muted px-1.5 text-xs" aria-label={`${count.data} releases`}>
      {count.data}
    </span>
  );
}

function CloneMenu({ repo }: { repo: Repository }) {
  const [open, setOpen] = useState(false);
  const instance = useInstance();
  const urls = cloneURLs(repo.full_path, window.location, { enabled: !!instance.data?.ssh_enabled, port: instance.data?.ssh_port });
  return (
    <div className="relative">
      <Button size="sm" onClick={() => setOpen(!open)} aria-expanded={open}>
        <Copy className="h-4 w-4" /> Clone <ChevronDown className="h-3 w-3" />
      </Button>
      {open && (
        <Card className="absolute right-0 z-10 mt-2 w-[min(28rem,90vw)] space-y-3 p-4 shadow-lg">
          {urls.map((u) => (
            <div key={u.label} className="space-y-1">
              <p className="text-xs font-medium text-muted-foreground">{u.label}</p>
              <CopyField value={u.url} label={`${u.label} clone URL`} />
            </div>
          ))}
          <p className="text-xs text-muted-foreground">
            Over HTTPS, git asks for an{" "}
            <Link to="/settings#tokens" className="text-primary hover:underline">
              access token
            </Link>
            's name and value; for SSH, add your key in{" "}
            <Link to="/settings#ssh-keys" className="text-primary hover:underline">
              settings
            </Link>
            .
          </p>
        </Card>
      )}
    </div>
  );
}

// RefPicker switches branch or tag, keeping the current view and path.
export function RefPicker({ current, view, path }: { current: string; view: "tree" | "blob" | "blame" | "commits"; path: string }) {
  const repo = useRepo();
  const refs = useRefs(repo.full_path);
  const navigate = useNavigate();
  const known = [...(refs.data?.branches ?? []), ...(refs.data?.tags ?? [])].some((r) => r.name === current);
  return (
    <Select
      aria-label="Branch or tag"
      value={current}
      onChange={(e) => navigate(`/${repo.full_path}/${view}/${encodePath(e.target.value)}${path ? "/" + encodePath(path) : ""}`)}
    >
      {!known && <option value={current}>{current.length === 40 ? current.slice(0, 7) : current}</option>}
      {refs.data && refs.data.branches.length > 0 && (
        <optgroup label="Branches">
          {refs.data.branches.map((b) => (
            <option key={b.name} value={b.name}>
              {b.name}
            </option>
          ))}
        </optgroup>
      )}
      {refs.data && refs.data.tags.length > 0 && (
        <optgroup label="Tags">
          {refs.data.tags.map((t) => (
            <option key={t.name} value={t.name}>
              {t.name}
            </option>
          ))}
        </optgroup>
      )}
    </Select>
  );
}

// Breadcrumbs: repo / dir / dir / file, each linking to its tree.
export function Breadcrumbs({ gitRef, path }: { gitRef: string; path: string }) {
  const repo = useRepo();
  const parts = path ? path.split("/") : [];
  return (
    <nav className="flex flex-wrap items-center gap-1 font-mono text-sm" aria-label="Path">
      <Link to={`/${repo.full_path}/tree/${encodePath(gitRef)}`} className="text-primary hover:underline">
        {repo.slug}
      </Link>
      {parts.map((p, i) => (
        <span key={i} className="flex items-center gap-1">
          <span className="text-muted-foreground">/</span>
          {i === parts.length - 1 ? (
            <span>{p}</span>
          ) : (
            <Link to={`/${repo.full_path}/tree/${encodePath(gitRef)}/${encodePath(parts.slice(0, i + 1).join("/"))}`} className="text-primary hover:underline">
              {p}
            </Link>
          )}
        </span>
      ))}
    </nav>
  );
}

