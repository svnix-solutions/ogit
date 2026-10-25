import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Repository } from "@/lib/api";
import { fakeAPI } from "@/test/render";
import { BranchProtection } from "./repo-protection";

afterEach(() => vi.unstubAllGlobals());

const repo = { id: "r1", namespace: "alice", slug: "site", full_path: "alice/site", name: "Site", role: "owner", required_approvals: 1 } as Repository;
const refs = { default_branch: "main", branches: [], tags: [] };
const mainRule = {
  id: "p1", pattern: "main", allow_force_push: false, allow_deletion: false, require_pull_request: true, required_approvals: 2,
  dismiss_stale_approvals: false, allowed_users: ["bob"], allowed_groups: [], owners_bypass: false,
};

function show(isOwner: boolean) {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <BranchProtection repo={repo} isOwner={isOwner} />
    </QueryClientProvider>,
  );
}

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

describe("branch protection settings", () => {
  it("suggests protecting the default branch", async () => {
    const fetch = fakeAPI({
      "/repository/r1/branch-protection": { rules: [] }, "/repository/alice%2Fsite/refs": refs,
      "POST /repository/r1/branch-protection": mainRule,
    });
    show(true);
    await userEvent.click(await screen.findByRole("button", { name: "Protect main" }));
    await waitFor(() => expect(sent(fetch, "POST", "/repository/r1/branch-protection")).toEqual([{ pattern: "main" }]));
  });

  it("adds a rule with the chosen options", async () => {
    const fetch = fakeAPI({
      "/repository/r1/branch-protection": { rules: [mainRule] }, "/repository/alice%2Fsite/refs": refs,
      "POST /repository/r1/branch-protection": mainRule,
    });
    show(true);
    expect(await screen.findByText("pull requests only")).toBeInTheDocument();
    expect(screen.getByText("only bob")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Protect main" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Add rule" }));
    await userEvent.type(screen.getByLabelText("Branch name or pattern"), "release/*");
    await userEvent.click(screen.getByLabelText(/Set the required approvals/));
    await userEvent.clear(screen.getByLabelText("Required approvals"));
    await userEvent.type(screen.getByLabelText("Required approvals"), "3");
    await userEvent.click(screen.getByLabelText("Dismiss approvals when new commits are pushed"));
    await userEvent.type(screen.getByLabelText("…and members of these groups"), "release-team, ops");
    await userEvent.click(screen.getByLabelText(/Owners can bypass/));
    await userEvent.click(screen.getByRole("button", { name: "Add rule" }));
    await waitFor(() => expect(sent(fetch, "POST", "/repository/r1/branch-protection")).toHaveLength(1));
    expect(sent(fetch, "POST", "/repository/r1/branch-protection")[0]).toEqual({
      pattern: "release/*", allow_force_push: false, allow_deletion: false, require_pull_request: false,
      required_approvals: 3, dismiss_stale_approvals: true, allowed_users: [], allowed_groups: ["release-team", "ops"], owners_bypass: true,
    });
  });

  it("edits a rule back to the repository's approvals, and deletes it", async () => {
    const fetch = fakeAPI({
      "/repository/r1/branch-protection": { rules: [mainRule] }, "/repository/alice%2Fsite/refs": refs,
      "PATCH /repository/r1/branch-protection/p1": mainRule, "DELETE /repository/r1/branch-protection/p1": { message: "rule deleted" },
    });
    vi.stubGlobal("confirm", vi.fn(() => true));
    show(true);
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    expect(screen.getByLabelText("Required approvals")).toHaveValue(2);
    await userEvent.click(screen.getByLabelText(/Set the required approvals/)); // off
    await userEvent.click(screen.getByRole("button", { name: "Save rule" }));
    await waitFor(() => expect(sent(fetch, "PATCH", "/repository/r1/branch-protection/p1")).toHaveLength(1));
    const body = sent(fetch, "PATCH", "/repository/r1/branch-protection/p1")[0];
    expect(body.required_approvals).toBe(-1);
    expect(body.allowed_users).toEqual(["bob"]);
    expect(body.require_pull_request).toBe(true);

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await userEvent.click(screen.getByRole("button", { name: "Delete rule" }));
    await waitFor(() => expect(sent(fetch, "DELETE", "/repository/r1/branch-protection/p1")).toHaveLength(1));
  });

  it("is read-only for maintainers", async () => {
    fakeAPI({ "/repository/r1/branch-protection": { rules: [mainRule] }, "/repository/alice%2Fsite/refs": refs });
    show(false);
    expect(await screen.findByText("pull requests only")).toBeInTheDocument();
    expect(screen.getByText("Owners manage these rules.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add rule" })).not.toBeInTheDocument();
  });
});
