import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { fakeAPI, renderRoutes } from "@/test/render";

afterEach(() => vi.unstubAllGlobals());

const instance = { version: "dev", registration_enabled: true, ssh_enabled: false };
const me = (name: string) => ({ id: "u", username: name, email: `${name}@x`, avatar: "", role: "user" });
const repo = (path: string, role: string, extra: object = {}) => {
  const [namespace, slug] = path.split("/");
  return { id: path.replace("/", "-"), namespace, slug, full_path: path, name: "App", description: "", is_private: false, archived: false,
    owner_type: "user", role, has_issues: true, ...extra };
};
const tree = { empty: true, ref: "main", path: "", entries: [] };
const commit = (h: string) => ({ hash: h.repeat(40), parents: [], message: "Fix it\n", author: { name: "B", email: "b@x", when: "2026-01-01T00:00:00Z" }, committer: { name: "B", email: "b@x", when: "2026-01-01T00:00:00Z" } });
const refs = (...branches: string[]) => ({ default_branch: "main", branches: branches.map((b) => ({ name: b, commit: commit("a") })), tags: [] });

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

describe("forks", () => {
  it("forks into your namespace or a group you maintain", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me("bob"), "/repository/alice%2Fapp": repo("alice/app", "none"),
      "/repository/alice%2Fapp/tree?spec=": tree,
      "/group": { groups: [{ id: "g1", name: "team", description: "", role: "maintainer" }, { id: "g2", name: "club", description: "", role: "member" }] },
      "POST /repository/alice-app/forks": repo("team/app", "owner", { is_fork: true, fork_of: { id: "alice-app", full_path: "alice/app" } }),
      "/repository/team%2Fapp": repo("team/app", "owner", { is_fork: true, fork_of: { id: "alice-app", full_path: "alice/app" } }),
      "/repository/team%2Fapp/tree?spec=": tree,
    });
    const router = renderRoutes(routes, "/alice/app");
    await userEvent.click(await screen.findByRole("button", { name: /Fork/ }));
    const owner = screen.getByLabelText("Owner");
    expect([...owner.querySelectorAll("option")].map((o) => o.textContent)).toEqual(["bob", "team"]); // not "club": only a member
    await userEvent.selectOptions(owner, "team");
    await userEvent.click(screen.getByRole("button", { name: "Create fork" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/team/app"));
    expect(sent(fetch, "POST", "/repository/alice-app/forks")).toEqual([{ namespace: "team", name: "App" }]);
    expect(await screen.findByRole("link", { name: "alice/app" })).toHaveAttribute("href", "/alice/app");
  });

  it("syncs a fork, for its writers", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me("bob"),
      "/repository/bob%2Fapp": repo("bob/app", "owner", { is_fork: true, fork_of: { id: "alice-app", full_path: "alice/app" } }),
      "/repository/bob%2Fapp/tree?spec=": tree,
      "POST /repository/bob-app/sync": { branch: "main", before: "a", after: "b", updated: true },
    });
    renderRoutes(routes, "/bob/app");
    await userEvent.click(await screen.findByRole("button", { name: "Sync fork" }));
    expect(await screen.findByText("main is now up to date with alice/app.")).toBeInTheDocument();
    expect(sent(fetch, "POST", "/repository/bob-app/sync")).toEqual([{}]);
  });

  it("hides sync from readers and the parent you can't see", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me("carol"),
      "/repository/bob%2Fapp": repo("bob/app", "reporter", { is_fork: true }),
      "/repository/bob%2Fapp/tree?spec=": tree,
    });
    renderRoutes(routes, "/bob/app");
    expect(await screen.findByText(/a repository you can't see/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sync fork" })).not.toBeInTheDocument();
  });

  it("opens a pull request from a fork, compared across forks", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me("bob"), "/repository/alice%2Fapp": repo("alice/app", "none"),
      "/repository/alice%2Fapp/refs": refs("main"),
      "/repository/alice-app/forks": { forks: [{ id: "bob-app", namespace: "bob", slug: "app", full_path: "bob/app", name: "App", description: "", is_private: false, archived: false }] },
      "/repository/bob%2Fapp/refs": refs("main", "fix"),
      "/repository/alice%2Fapp/pulls/preview?source_repository=bob%2Fapp&source=fix&target=main": { merge_base: "a".repeat(40), commits: [commit("b")], commits_truncated: false, diff: { files: [] } },
      "POST /repository/alice%2Fapp/pulls": { number: 9 },
      "/repository/alice%2Fapp/pulls/9": [404, { code: 404, details: "x", type: "x" }],
    });
    const router = renderRoutes(routes, "/alice/app/pulls/new");
    await userEvent.selectOptions(await screen.findByLabelText("Source repository"), "bob/app");
    await waitFor(() => expect(screen.getByLabelText("Source branch").querySelectorAll("option").length).toBe(3));
    await userEvent.selectOptions(screen.getByLabelText("Source branch"), "fix");
    await userEvent.click(await screen.findByRole("button", { name: "Create pull request" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/alice/app/pulls/9"));
    expect(sent(fetch, "POST", "/repository/alice%2Fapp/pulls")).toEqual([{ title: "Fix it", body: "", source: "fix", target: "main", draft: false, source_repository: "bob/app" }]);
  });

  it("points a fork's new pull requests at the original", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me("bob"),
      "/repository/bob%2Fapp": repo("bob/app", "owner", { is_fork: true, fork_of: { id: "alice-app", full_path: "alice/app" } }),
      "/repository/bob%2Fapp/refs": refs("main", "fix"), "/repository/bob-app/forks": { forks: [] },
    });
    renderRoutes(routes, "/bob/app/pulls/new");
    expect(await screen.findByRole("link", { name: "Open a pull request into alice/app" })).toHaveAttribute("href", "/alice/app/pulls/new?source_repository=bob%2Fapp");
  });
});

describe("pull requests from forks", () => {
  const pull = (over: object) => ({
    number: 7, title: "Fix it", state: "open", draft: false, author: "bob", source_branch: "fix", target_branch: "main", source_repository: "bob/app",
    created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z", labels: [], body: "", base_commit: "b".repeat(40), head_commit: "h".repeat(40),
    mergeable: true, conflicts: [], branches_exist: true, can_edit: true, can_merge: false, can_review: false, approved: [], changes_requested: [],
    required_approvals: 0, required_checks: [], assignees: [], allow_maintainer_edits: true, ...over,
  });
  const table = (who: string, role: string) => ({
    "/instance": instance, "/user/profile": me(who), "/repository/alice%2Fapp": repo("alice/app", role),
    "/repository/alice%2Fapp/pulls/7": pull({}), "/repository/alice%2Fapp/pulls/7/activity": { reviews: [], comments: [] },
    "/repository/alice%2Fapp/labels": { labels: [] }, "/repository/alice%2Fapp/milestones?state=open": { milestones: [] },
    "/repository/alice%2Fapp/members": { members: [] },
    "PATCH /repository/alice%2Fapp/pulls/7": pull({ allow_maintainer_edits: false }),
  });

  it("names the fork and lets the author allow edits", async () => {
    const fetch = fakeAPI(table("bob", "none"));
    renderRoutes(routes, "/alice/app/pulls/7");
    expect(await screen.findByText("bob/app:fix")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: /Allow edits by maintainers/ }));
    await waitFor(() => expect(sent(fetch, "PATCH", "/repository/alice%2Fapp/pulls/7")).toEqual([{ allow_maintainer_edits: false }]));
  });

  it("only tells others", async () => {
    fakeAPI(table("dave", "developer"));
    renderRoutes(routes, "/alice/app/pulls/7");
    expect(await screen.findByText("Developers here can push to bob/app:fix.")).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: /Allow edits by maintainers/ })).not.toBeInTheDocument();
  });
});
