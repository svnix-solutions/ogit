import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider, type RouteObject } from "react-router";
import { Layout } from "@/components/layout";
import { RepoLayout } from "@/components/repo-layout";
import { RequireAuth } from "@/components/require-auth";
import { AdminPage } from "@/pages/admin";
import { ExplorePage } from "@/pages/explore";
import { GroupPage, GroupsPage } from "@/pages/groups";
import { HomePage } from "@/pages/home";
import { LoginPage } from "@/pages/login";
import { NamespacePage } from "@/pages/namespace";
import { NewRepoPage } from "@/pages/new-repo";
import { NotFoundPage } from "@/pages/not-found";
import { RegisterPage } from "@/pages/register";
import { BlamePage, BlobPage, TreePage } from "@/pages/repo-code";
import { BranchesPage, CommitPage, CommitsPage, ComparePage } from "@/pages/repo-history";
import { NewPullPage, PullPage, PullsPage } from "@/pages/repo-pulls";
import { RepoSettingsPage } from "@/pages/repo-settings";
import { SettingsPage } from "@/pages/settings";

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
          { index: true, element: <TreePage /> },
          { path: "tree/*", element: <TreePage /> },
          { path: "blob/*", element: <BlobPage /> },
          { path: "blame/*", element: <BlamePage /> },
          { path: "commits/*", element: <CommitsPage /> },
          { path: "commit/:sha", element: <CommitPage /> },
          { path: "branches", element: <BranchesPage /> },
          { path: "compare/*", element: <ComparePage /> },
          { path: "pulls", element: <PullsPage /> },
          { path: "pulls/:number", element: <PullPage /> },
          { path: "pulls/:number/:tab", element: <PullPage /> },
          {
            element: <RequireAuth />,
            children: [
              { path: "pulls/new", element: <NewPullPage /> },
              { path: "settings", element: <RepoSettingsPage /> },
            ],
          },
        ],
      },
      {
        element: <RequireAuth />,
        children: [
          { path: "/", element: <HomePage /> },
          { path: "/new", element: <NewRepoPage /> },
          { path: "/groups", element: <GroupsPage /> },
          { path: "/groups/:name", element: <GroupPage /> },
          { path: "/settings", element: <SettingsPage /> },
          { path: "/admin", element: <AdminPage /> },
        ],
      },
      { path: "*", element: <NotFoundPage /> },
    ],
  },
];

export function App({ client = new QueryClient() }: { client?: QueryClient }) {
  return (
    <QueryClientProvider client={client}>
      <RouterProvider router={createBrowserRouter(routes)} />
    </QueryClientProvider>
  );
}
