import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { createMemoryRouter, RouterProvider, type RouteObject } from "react-router";
import { vi } from "vitest";

// fakeAPI answers fetch("/api/v1/<path>") from a table: path (without the
// prefix, query included) → JSON body, or [status, body].
export function fakeAPI(table: Record<string, unknown>) {
  const fetch = vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.replace(/^\/api\/v1/, "");
    const key = `${init?.method ?? "GET"} ${path}`;
    const hit = key in table ? table[key] : table[path];
    if (hit === undefined) return new Response(JSON.stringify({ code: 404, details: "no fake for " + key, type: "NOT_FOUND" }), { status: 404 });
    const [status, body] = Array.isArray(hit) && typeof hit[0] === "number" ? hit : [200, hit];
    return new Response(JSON.stringify(body), { status: status as number });
  });
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

export function renderRoutes(routes: RouteObject[], url: string) {
  const router = createMemoryRouter(routes, { initialEntries: [url] });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}
