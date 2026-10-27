import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { fakeAPI, renderRoutes } from "@/test/render";

afterEach(() => vi.unstubAllGlobals());

const instance = { version: "dev", registration_enabled: true, ssh_enabled: false };
const alice = { id: "u1", username: "alice", email: "a@example.com", avatar: "", role: "user" };
const commit = { hash: "a".repeat(40), parents: [], message: "Init\n", author: { name: "A", email: "a@x", when: "2026-01-01T00:00:00Z" },
  committer: { name: "A", email: "a@x", when: "2026-01-01T00:00:00Z" } };
const repo = { id: "r1", namespace: "org/team", slug: "app", full_path: "org/team/app", name: "App", description: "", is_private: true,
  archived: false, owner_type: "group", role: "developer", has_issues: true };

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

describe("sub-groups", () => {
  it("shows a sub-group's repository and its files", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": alice,
      "/resolve?path=org%2Fteam%2Fapp%2Fblob%2Fmain%2Fa.txt": { type: "repository", path: "org/team/app" },
      "/repository/org%2Fteam%2Fapp": repo,
      "/repository/org%2Fteam%2Fapp/blob?spec=main%2Fa.txt": { ref: "main", commit, file: { path: "a.txt", size: 6, binary: false, truncated: false, content: "hello\n" } },
    });
    renderRoutes(routes, "/org/team/app/blob/main/a.txt");
    expect(await screen.findByRole("link", { name: "Raw" })).toHaveAttribute("href", "/org/team/app/raw/main/a.txt");
    const heading = screen.getByRole("heading", { name: /app/i });
    expect(within(heading).getByRole("link", { name: "org" })).toHaveAttribute("href", "/org");
    expect(within(heading).getByRole("link", { name: "team" })).toHaveAttribute("href", "/org/team");
  });

  it("shows a sub-group's page, at any depth", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": alice, "/group": { groups: [] },
      "/resolve?path=org%2Fteam%2Fb": { type: "group", path: "org/team/b" },
      "/namespace/org%2Fteam%2Fb?limit=30&offset=0": { name: "org/team/b", type: "group", can_create: true, total: 0, repositories: [], subgroups: [{ name: "org/team/b/c" }] },
    });
    renderRoutes(routes, "/org/team/b");
    expect(await screen.findByRole("link", { name: "c" })).toHaveAttribute("href", "/org/team/b/c");
    expect(screen.getByRole("link", { name: /New sub-group/ })).toHaveAttribute("href", "/groups/org/team/b#subgroups");
  });

  it("shows org/team, which takes the repository route, as a group", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": alice, "/group": { groups: [] },
      "/repository/org%2Fteam": [404, { code: 404, type: "REPOSITORY_NOT_FOUND", details: "repository not found" }],
      "/namespace/org%2Fteam?limit=30&offset=0": { name: "org/team", type: "group", can_create: false, total: 1, subgroups: [],
        repositories: [{ id: "r1", namespace: "org/team", slug: "app", full_path: "org/team/app", name: "App", description: "", is_private: false, archived: false }] },
    });
    renderRoutes(routes, "/org/team");
    expect(await screen.findByRole("link", { name: "App" })).toHaveAttribute("href", "/org/team/app");
  });

  it("redirects a moved group's former paths", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": alice,
      "/resolve?path=org%2Fold%2Fapp%2Fblob%2Fmain%2Fa.txt": { type: "repository", path: "org/team/app", from: "org/old/app" },
      "/resolve?path=org%2Fteam%2Fapp%2Fblob%2Fmain%2Fa.txt": { type: "repository", path: "org/team/app" },
      "/repository/org%2Fteam%2Fapp": repo,
      "/repository/org%2Fteam%2Fapp/blob?spec=main%2Fa.txt": { ref: "main", commit, file: { path: "a.txt", size: 6, binary: false, truncated: false, content: "hello\n" } },
    });
    const router = renderRoutes(routes, "/org/old/app/blob/main/a.txt");
    expect(await screen.findByRole("link", { name: "Raw" })).toHaveAttribute("href", "/org/team/app/raw/main/a.txt");
    expect(router.state.location.pathname).toBe("/org/team/app/blob/main/a.txt");
  });

  it("manages a sub-group: inherited members, sub-groups, moving", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": alice,
      "/group/org%2Fteam": { id: "g2", name: "org/team", parent: "org", description: "", role: "owner", require_two_factor: false, require_two_factor_inherited: true },
      "/group/org%2Fteam/members": { members: [{ username: "alice", role: "owner" }, { username: "dave", role: "maintainer", inherited_from: "org" }] },
      "/group/org%2Fteam/subgroups": { groups: [{ id: "g3", name: "org/team/ops", description: "" }] },
      "/group/org%2Fteam/hooks": { webhooks: [] },
      "/group/org%2Fteam/runners": { runners: [] }, "/group/org%2Fteam/actions/secrets": { secrets: [] }, "/group/org%2Fteam/actions/variables": { variables: [] },
      "POST /group": { id: "g4", name: "org/team/web", description: "" },
      "POST /group/org%2Fteam/move": { id: "g2", name: "corp/team", parent: "corp", description: "" },
    });
    vi.stubGlobal("confirm", () => true);
    renderRoutes(routes, "/groups/org/team");
    expect(await screen.findByText("from org")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove dave" })).not.toBeInTheDocument();
    expect(screen.getByText("A parent group requires two-factor authentication, so this group does too.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "ops" })).toHaveAttribute("href", "/org/team/ops");

    await userEvent.type(screen.getByLabelText("Sub-group name"), "web");
    await userEvent.click(screen.getByRole("button", { name: "Create sub-group" }));
    await waitFor(() => expect(sent(fetch, "POST", "/group")).toEqual([{ name: "web", parent: "org/team" }]));

    renderRoutes(routes, "/groups/org/team");
    const parent = (await screen.findAllByLabelText("Parent group")).at(-1)!;
    await userEvent.clear(parent);
    await userEvent.type(parent, "corp");
    await userEvent.click(screen.getAllByRole("button", { name: "Move" }).at(-1)!);
    await waitFor(() => expect(sent(fetch, "POST", "/group/org%2Fteam/move")).toEqual([{ parent: "corp", name: "team" }]));
  });
});
