import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Repository } from "@/lib/api";
import { fakeAPI } from "@/test/render";
import { ProtectedTags } from "./repo-protected-tags";

afterEach(() => vi.unstubAllGlobals());

const repo = { id: "r1", namespace: "alice", slug: "site", full_path: "alice/site", name: "Site", role: "owner" } as Repository;
const vRule = { id: "t1", pattern: "v*", allowed_users: [], allowed_groups: [] };

function show(isOwner: boolean) {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ProtectedTags repo={repo} isOwner={isOwner} />
    </QueryClientProvider>,
  );
}

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

describe("protected tags settings", () => {
  it("adds, edits and deletes rules", async () => {
    vi.stubGlobal("confirm", vi.fn(() => true));
    const fetch = fakeAPI({
      "/repository/r1/protected-tags": { rules: [vRule] },
      "POST /repository/r1/protected-tags": vRule, "PATCH /repository/r1/protected-tags/t1": vRule, "DELETE /repository/r1/protected-tags/t1": { message: "rule deleted" },
    });
    show(true);
    expect(await screen.findByText("maintainers only")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Protect tags" }));
    await userEvent.type(screen.getByLabelText("Tag name or pattern"), "release/**");
    await userEvent.type(screen.getByLabelText("These users can create them too"), "bot, carol");
    await userEvent.type(screen.getByLabelText("…and members of these groups"), "ops");
    await userEvent.click(screen.getByRole("button", { name: "Protect tags" }));
    await waitFor(() => expect(sent(fetch, "POST", "/repository/r1/protected-tags")).toEqual([
      { pattern: "release/**", allowed_users: ["bot", "carol"], allowed_groups: ["ops"] },
    ]));

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await userEvent.type(screen.getByLabelText("These users can create them too"), "dave");
    await userEvent.click(screen.getByRole("button", { name: "Save rule" }));
    await waitFor(() => expect(sent(fetch, "PATCH", "/repository/r1/protected-tags/t1")).toEqual([{ pattern: "v*", allowed_users: ["dave"], allowed_groups: [] }]));

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await userEvent.click(screen.getByRole("button", { name: "Delete rule" }));
    await waitFor(() => expect(sent(fetch, "DELETE", "/repository/r1/protected-tags/t1")).toHaveLength(1));
  });

  it("is read-only for maintainers", async () => {
    fakeAPI({ "/repository/r1/protected-tags": { rules: [{ ...vRule, allowed_users: ["bot"] }] } });
    show(false);
    expect(await screen.findByText("also bot")).toBeInTheDocument();
    expect(screen.getByText("Owners manage these rules.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Protect tags" })).not.toBeInTheDocument();
  });
});
