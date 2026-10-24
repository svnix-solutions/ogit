import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider, type RouteObject } from "react-router";
import { Layout } from "@/components/layout";
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
import { RepoPage } from "@/pages/repo";
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
      {
        element: <RequireAuth />,
        children: [
          { path: "/", element: <HomePage /> },
          { path: "/new", element: <NewRepoPage /> },
          { path: "/groups", element: <GroupsPage /> },
          { path: "/groups/:name", element: <GroupPage /> },
          { path: "/settings", element: <SettingsPage /> },
          { path: "/admin", element: <AdminPage /> },
          { path: "/:owner/:repo", element: <RepoPage /> },
          { path: "/:owner/:repo/settings", element: <RepoSettingsPage /> },
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
