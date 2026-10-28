import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, Navigate, RouterProvider, useLocation, type PatchRoutesOnNavigationFunction, type RouteObject } from "react-router";
import { Layout } from "@/components/layout";
import { RepoLayout } from "@/components/repo-layout";
import { RequireAuth } from "@/components/require-auth";
import { api } from "@/lib/api";
import { ExplorePage } from "@/pages/explore";
import { HomePage } from "@/pages/home";
import { LoginPage } from "@/pages/login";
import { LoginLinkPage } from "@/pages/login-link";
import { NamespacePage } from "@/pages/namespace";
import { NotFoundPage } from "@/pages/not-found";
import { RegisterPage } from "@/pages/register";

// Pages load on first visit: the main bundle only has what signing in and
// the first screens need. Markdown, diff and admin code come with their
// pages.
const page = (load: () => Promise<Record<string, unknown>>, name: string) => async () => ({
  Component: (await load())[name] as React.ComponentType,
});
const code = () => import("@/pages/repo-code");
const history = () => import("@/pages/repo-history");
const pulls = () => import("@/pages/repo-pulls");
const issues = () => import("@/pages/repo-issues");
const releases = () => import("@/pages/repo-releases");
const actions = () => import("@/pages/repo-actions");
const packages = () => import("@/pages/packages");
const wiki = () => import("@/pages/repo-wiki");

// repoChildren are a repository's pages, under each repository route.
const repoChildren = (): RouteObject[] => [
  { index: true, lazy: page(code, "TreePage") },
  { path: "tree/*", lazy: page(code, "TreePage") },
  { path: "blob/*", lazy: page(code, "BlobPage") },
  { path: "blame/*", lazy: page(code, "BlamePage") },
  { path: "commits/*", lazy: page(history, "CommitsPage") },
  { path: "commit/:sha", lazy: page(history, "CommitPage") },
  { path: "branches", lazy: page(history, "BranchesPage") },
  { path: "compare/*", lazy: page(history, "ComparePage") },
  { path: "pulls", lazy: page(pulls, "PullsPage") },
  { path: "pulls/:number", lazy: page(pulls, "PullPage") },
  { path: "pulls/:number/:tab", lazy: page(pulls, "PullPage") },
  { path: "issues", lazy: page(issues, "IssuesPage") },
  { path: "issues/:number", lazy: page(issues, "IssuePage") },
  { path: "milestones", lazy: page(issues, "MilestonesPage") },
  { path: "milestones/:id", lazy: page(issues, "MilestonePage") },
  { path: "forks", lazy: page(() => import("@/pages/repo-forks"), "RepoForksPage") },
  { path: "releases", lazy: page(releases, "ReleasesPage") },
  { path: "releases/tag/*", lazy: page(releases, "ReleasePage") },
  { path: "actions", lazy: page(actions, "ActionsPage") },
  { path: "actions/runs/:number", lazy: page(actions, "RunPage") },
  { path: "packages", lazy: page(packages, "RepoPackagesPage") },
  { path: "wiki", lazy: page(wiki, "WikiPage") },
  { path: "wiki/*", lazy: page(wiki, "WikiPage") },
  {
    element: <RequireAuth />,
    children: [
      { path: "pulls/new", lazy: page(pulls, "NewPullPage") },
      { path: "issues/new", lazy: page(issues, "NewIssuePage") },
      { path: "releases/new", lazy: page(releases, "NewReleasePage") },
      { path: "releases/edit/:id", lazy: page(releases, "EditReleasePage") },
      { path: "settings", lazy: page(() => import("@/pages/repo-settings"), "RepoSettingsPage") },
    ],
  },
];

// MovedPath sends a moved group's former path (and what's under it) to the
// new one.
function MovedPath({ from, to }: { from: string; to: string }) {
  const loc = useLocation();
  return <Navigate replace to={"/" + to + loc.pathname.slice(from.length + 1) + loc.search + loc.hash} />;
}

// nestedRoutes adds, on navigation, the routes of sub-groups' pages, which
// don't fit /:owner/:repo: org/team/app/... is the repository org/team/app,
// org/team/b the group (the server knows, /resolve). org/team itself takes
// the repository route, whose page shows the namespace when no repository
// is there. One per router: it remembers what it added.
export function nestedRoutes(): PatchRoutesOnNavigationFunction {
  const tried = new Set<string>(); // by first three segments, found or not
  const added = new Set<string>();
  return async ({ path, matches, patch }) => {
    const leaf = matches[matches.length - 1]?.route;
    if (leaf?.path && leaf.path !== "*" && !leaf.path.startsWith("/:owner")) return; // a page matched
    const parts = path.split(/[?#]/)[0].split("/").filter(Boolean);
    const key = parts.slice(0, 3).join("/");
    if (parts.length < 3 || tried.has(key)) return;
    tried.add(key);
    let r: { type: string; path: string; from?: string };
    try {
      r = await api<{ type: string; path: string; from?: string }>("GET", `/resolve?path=${encodeURIComponent(parts.join("/"))}`);
    } catch {
      return; // not found: the catch-all answers
    }
    if (r.from && !added.has(r.from)) {
      // A moved group's former path: its pages redirect.
      added.add(r.from);
      patch("root", [{ path: "/" + r.from + "/*", element: <MovedPath from={r.from} to={r.path} /> }]);
      return;
    }
    if (added.has(r.path)) return;
    added.add(r.path);
    patch("root", [
      r.type === "repository"
        ? { path: "/" + r.path, element: <RepoLayout path={r.path} />, children: repoChildren() }
        : { path: "/" + r.path, element: <NamespacePage path={r.path} /> },
    ]);
  };
}

// Fixed paths win over /:owner (React Router ranks static segments first);
// the server reserves these words as user and group names.
export const routes: RouteObject[] = [
  {
    id: "root",
    element: <Layout />,
    children: [
      { path: "/login", element: <LoginPage /> },
      { path: "/login/link", element: <LoginLinkPage /> },
      { path: "/register", element: <RegisterPage /> },
      { path: "/explore", element: <ExplorePage /> },
      { path: "/packages/*", lazy: page(packages, "PackagePage") },
      { path: "/:owner", element: <NamespacePage /> },
      // Public repositories are browsable signed out; settings need a
      // session. Sub-groups' repositories (org/team/app) are added by
      // nestedRoutes.
      { path: "/:owner/:repo", element: <RepoLayout />, children: repoChildren() },
      {
        element: <RequireAuth />,
        children: [
          { path: "/", element: <HomePage /> },
          { path: "/new", lazy: page(() => import("@/pages/new-repo"), "NewRepoPage") },
          { path: "/groups", lazy: page(() => import("@/pages/groups"), "GroupsPage") },
          { path: "/groups/:name/*", lazy: page(() => import("@/pages/groups"), "GroupPage") },
          { path: "/settings", lazy: page(() => import("@/pages/settings"), "SettingsPage") },
          { path: "/notifications", lazy: page(() => import("@/pages/notifications"), "NotificationsPage") },
          { path: "/admin", lazy: page(() => import("@/pages/admin"), "AdminPage") },
        ],
      },
      { path: "*", element: <NotFoundPage /> },
    ],
  },
];

// Data is reused for 30 seconds before a refetch (on focus or remount);
// pages invalidate what they change themselves.
const defaultClient = () => new QueryClient({ defaultOptions: { queries: { staleTime: 30_000 } } });

export function App({ client = defaultClient() }: { client?: QueryClient }) {
  return (
    <QueryClientProvider client={client}>
      <RouterProvider router={createBrowserRouter(routes, { patchRoutesOnNavigation: nestedRoutes() })} />
    </QueryClientProvider>
  );
}
