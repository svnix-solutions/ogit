import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { leaveLogin, LoginPage, safeNext } from "./login";

afterEach(() => vi.unstubAllGlobals());

function renderLogin(url = "/login") {
  const router = createMemoryRouter(
    [
      { path: "/login", element: <LoginPage /> },
      { path: "/", element: <p>home page</p> },
      { path: "/repos/x", element: <p>repo page</p> },
    ],
    { initialEntries: [url] },
  );
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

async function fill() {
  await userEvent.type(screen.getByLabelText("Email"), "me@example.com");
  await userEvent.type(screen.getByLabelText("Password"), "secret");
  await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
}

describe("LoginPage", () => {
  it("creates a cookie session and follows ?next=", async () => {
    const fetch = vi.fn(async () => new Response(JSON.stringify({ expires_at: "2030-01-01T00:00:00Z" })));
    vi.stubGlobal("fetch", fetch);
    renderLogin("/login?next=%2Frepos%2Fx");
    await fill();
    expect(await screen.findByText("repo page")).toBeInTheDocument();
    const [url, init] = (fetch.mock.calls as unknown as [string, RequestInit][]).find(([u]) => u === "/api/v1/auth/session")!;
    expect(url).toBe("/api/v1/auth/session");
    expect(JSON.parse(init.body as string)).toEqual({ email: "me@example.com", password: "secret" });
  });

  it("shows a generic message for bad credentials", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify({ code: 401, details: "invalid credentials", type: "INVALID_CREDENTIALS" }), { status: 401 })),
    );
    renderLogin();
    await fill();
    expect(await screen.findByRole("alert")).toHaveTextContent("Incorrect email or password.");
  });

  it("offers a new link when the email isn't verified", async () => {
    const fetch = vi.fn(async (url: string) =>
      url.endsWith("/resend")
        ? new Response(JSON.stringify({ message: "ok" }))
        : new Response(JSON.stringify({ code: 403, details: "verify", type: "EMAIL_NOT_VERIFIED" }), { status: 403 }),
    );
    vi.stubGlobal("fetch", fetch);
    renderLogin();
    await fill();
    await userEvent.click(await screen.findByRole("button", { name: "Send a new link" }));
    expect(await screen.findByRole("button", { name: /a new link is on its way/ })).toBeDisabled();
    expect((fetch.mock.calls as unknown as [string][]).map(([u]) => u)).toContain("/api/v1/auth/verify/resend");
  });
});

describe("leaveLogin", () => {
  it("loads the server's route that opens a workspace, and stays in the app otherwise", () => {
    const replace = vi.fn();
    vi.stubGlobal("location", { replace });
    const navigate = vi.fn();
    leaveLogin("/api/v1/workspaces/abc/enter?next=%2F", navigate);
    expect(replace).toHaveBeenCalledWith("/api/v1/workspaces/abc/enter?next=%2F");
    expect(navigate).not.toHaveBeenCalled();
    replace.mockClear();
    leaveLogin("/api/v1/auth/logout", navigate);
    leaveLogin("/repos/x", navigate);
    expect(replace).not.toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledTimes(2);
  });
});

describe("safeNext", () => {
  it("only allows same-site paths", () => {
    expect(safeNext("/repos/x?tab=1")).toBe("/repos/x?tab=1");
    expect(safeNext(null)).toBe("/");
    expect(safeNext("https://evil.example")).toBe("/");
    expect(safeNext("//evil.example/x")).toBe("/");
    expect(safeNext("/\\evil.example")).toBe("/");
    expect(safeNext("javascript:alert(1)")).toBe("/");
  });
});
