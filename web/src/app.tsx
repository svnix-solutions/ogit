import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider, type RouteObject } from "react-router";
import { Layout } from "@/components/layout";
import { RequireAuth } from "@/components/require-auth";
import { HomePage } from "@/pages/home";
import { LoginPage } from "@/pages/login";
import { NotFoundPage } from "@/pages/not-found";
import { RegisterPage } from "@/pages/register";

export const routes: RouteObject[] = [
  {
    element: <Layout />,
    children: [
      { path: "/login", element: <LoginPage /> },
      { path: "/register", element: <RegisterPage /> },
      { element: <RequireAuth />, children: [{ path: "/", element: <HomePage /> }] },
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
