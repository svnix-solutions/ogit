import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider, type RouteObject } from "react-router";
import { Layout } from "@/components/layout";
import { RepoLayout } from "@/components/repo-layout";
import { RequireAuth } from "@/components/require-auth";
import { ExplorePage } from "@/pages/explore";
import { HomePage } from "@/pages/home";
import { LoginPage } from "@/pages/login";
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

// Fixed paths win over /:owner (React Router ranks static segments first);
// the server reserves these words as user and group names.
export const routes: RouteObject[] = [
  {
    element: <Layout />,
    children: [
      { path: "/login", element: <LoginPage /> },
      { path: "/register", element: <RegisterPage /> },
      { path: "/explore", element: <ExplorePage /> },
      { path: "/:owner", element: <NamespacePage /> },
      // Public repositories are browsable signed out; settings need a session.
      {
        path: "/:owner/:repo",
        element: <RepoLayout />,
        children: [
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
          {
            element: <RequireAuth />,
            children: [
              { path: "pulls/new", lazy: page(pulls, "NewPullPage") },
              { path: "issues/new", lazy: page(issues, "NewIssuePage") },
              { path: "settings", lazy: page(() => import("@/pages/repo-settings"), "RepoSettingsPage") },
            ],
          },
        ],
      },
      {
        element: <RequireAuth />,
        children: [
          { path: "/", element: <HomePage /> },
          { path: "/new", lazy: page(() => import("@/pages/new-repo"), "NewRepoPage") },
          { path: "/groups", lazy: page(() => import("@/pages/groups"), "GroupsPage") },
          { path: "/groups/:name", lazy: page(() => import("@/pages/groups"), "GroupPage") },
          { path: "/settings", lazy: page(() => import("@/pages/settings"), "SettingsPage") },
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
      <RouterProvider router={createBrowserRouter(routes)} />
    </QueryClientProvider>
  );
}
