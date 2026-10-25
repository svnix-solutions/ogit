import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Repository } from "@/lib/api";
import { fakeAPI } from "@/test/render";
import { RepoTokens } from "./repo-tokens";

afterEach(() => vi.unstubAllGlobals());

const repo = { id: "r1", namespace: "alice", slug: "app", full_path: "alice/app", name: "App", role: "maintainer" } as Repository;
const existing = { id: "t1", name: "deploy", description: "", scopes: ["read:repository"], expires_at: null, last_used_at: "2026-10-20T10:00:00Z", created_at: "2026-10-01T00:00:00Z" };

function show() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RepoTokens repo={repo} />
    </QueryClientProvider>,
  );
}

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

describe("repository tokens", () => {
  it("creates a token and shows its value once", async () => {
    const fetch = fakeAPI({
      "/repository/r1/tokens": { tokens: [existing] },
      "POST /repository/r1/tokens": { ...existing, id: "t2", name: "ci", scopes: ["read:repository", "write:status"], token: "ogit_" + "a".repeat(64) },
    });
    show();
    expect(await screen.findByText("deploy")).toBeInTheDocument();
    expect(screen.getByText(/no expiry/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Name"), "ci");
    await userEvent.type(screen.getByLabelText("Expires (optional)"), "2027-01-31");
    await userEvent.click(screen.getByLabelText("Post commit statuses"));
    await userEvent.click(screen.getByRole("button", { name: "Create token" }));
    await waitFor(() => expect(sent(fetch, "POST", "/repository/r1/tokens")).toHaveLength(1));
    expect(sent(fetch, "POST", "/repository/r1/tokens")[0]).toEqual({ name: "ci", scopes: ["read:repository", "write:status"], expires_at: "2027-01-31T23:59:59.000Z" });
    expect(await screen.findByLabelText("New token")).toHaveValue("ogit_" + "a".repeat(64));
    expect(screen.getByText(/won't be shown again/)).toBeInTheDocument();
  });

  it("revokes after confirmation", async () => {
    const fetch = fakeAPI({ "/repository/r1/tokens": { tokens: [existing] }, "DELETE /repository/r1/tokens/t1": { message: "token revoked" } });
    const confirm = vi.fn(() => false);
    vi.stubGlobal("confirm", confirm);
    show();
    await userEvent.click(await screen.findByRole("button", { name: "Revoke token deploy" }));
    expect(sent(fetch, "DELETE", "/repository/r1/tokens/t1")).toHaveLength(0);
    confirm.mockReturnValue(true);
    await userEvent.click(screen.getByRole("button", { name: "Revoke token deploy" }));
    await waitFor(() => expect(sent(fetch, "DELETE", "/repository/r1/tokens/t1")).toHaveLength(1));
  });
});
