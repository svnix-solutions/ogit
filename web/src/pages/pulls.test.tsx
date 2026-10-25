import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { renderMarkdown } from "@/lib/markdown";
import { textColor } from "@/lib/pulls";
import { fakeAPI, renderRoutes } from "@/test/render";

afterEach(() => vi.unstubAllGlobals());

const instance = { version: "dev", registration_enabled: true, ssh_enabled: false };
const me = (name: string) => ({ id: "u", username: name, email: `${name}@x`, avatar: "", role: "user" });
const repo = (role: string) => ({ id: "r1", namespace: "acme", slug: "app", full_path: "acme/app", name: "App", description: "", is_private: true, archived: false, owner_type: "group", role });
const bug = { id: "l1", name: "bug", color: "#d73a4a", description: "" };
const summary = { number: 7, title: "Add login", state: "open", draft: false, author: "dev", source_branch: "feature/login", target_branch: "main", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z", labels: [bug] };
const pull = (over: object) => ({
  ...summary, body: "Fixes #3, cc @maint", base_commit: "b".repeat(40), head_commit: "h".repeat(40), mergeable: true, conflicts: [], branches_exist: true,
  can_edit: false, can_merge: false, can_review: true, approved: [], changes_requested: [], required_approvals: 0, ...over,
});

describe("markdown links", () => {
  it("links mentions and pull requests outside code", () => {
    const html = renderMarkdown("Thanks @bob! See #12 and `#13`, mail bob@example.com", { fullPath: "a/b", ref: "main", dir: "", links: true });
    expect(html).toContain('<a href="/bob">@bob</a>');
    expect(html).toContain('<a href="/a/b/issues/12">#12</a>');
    expect(html).toContain("<code>#13</code>");
    expect(html).toContain("bob@example.com");
    expect(html).not.toContain('href="/example.com"');
  });
  it("picks readable label text", () => {
    expect(textColor("#ffffff")).toBe("#000");
    expect(textColor("#d73a4a")).toBe("#fff");
  });
});

describe("pull requests", () => {
  it("lists them with counts and labels; developers can open one", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me("dev"), "/repository/acme%2Fapp": repo("developer"),
      "/repository/acme%2Fapp/pulls?state=open&limit=30&offset=0": { pull_requests: [summary], counts: { open: 1, merged: 4, closed: 0 } },
      "/repository/acme%2Fapp/labels": { labels: [bug] },
    });
    renderRoutes(routes, "/acme/app/pulls");
    expect(await screen.findByRole("link", { name: "Add login" })).toHaveAttribute("href", "/acme/app/pulls/7");
    expect(screen.getAllByText("bug").length).toBe(2); // the badge, and the label filter
    expect(screen.getByRole("button", { name: /Merged\s*4/ })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New pull request" })).toHaveAttribute("href", "/acme/app/pulls/new");
  });

  it("explains what blocks the merge and only enables it when allowed", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me("maint"), "/repository/acme%2Fapp": repo("maintainer"),
      "/repository/acme%2Fapp/pulls/7": pull({ required_approvals: 2, approved: ["dev2"], changes_requested: ["qa"], can_merge: false }),
      "/repository/acme%2Fapp/pulls/7/activity": { reviews: [], comments: [] },
      "/repository/acme%2Fapp/labels": { labels: [bug] },
    });
    renderRoutes(routes, "/acme/app/pulls/7");
    expect(await screen.findByText("1 of 2 required approvals.")).toBeInTheDocument();
    expect(screen.getByText("Changes requested by qa.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Merge" })).toBeDisabled();
    expect(screen.getByRole("link", { name: "#3" })).toHaveAttribute("href", "/acme/app/issues/3");
    expect(screen.getByRole("button", { name: "Submit review" })).toBeInTheDocument();
  });

  it("enables merging when approved, and hides review from the author", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me("dev"), "/repository/acme%2Fapp": repo("maintainer"),
      "/repository/acme%2Fapp/pulls/7": pull({ can_merge: true, can_review: false, can_edit: true, conflicts: [] }),
      "/repository/acme%2Fapp/pulls/7/activity": { reviews: [{ id: "x", author: "dev2", state: "approved", body: "LGTM", commit_id: "", created_at: "2026-01-02T00:00:00Z" }], comments: [] },
      "/repository/acme%2Fapp/labels": { labels: [] },
    });
    renderRoutes(routes, "/acme/app/pulls/7");
    expect(await screen.findByRole("button", { name: "Merge" })).toBeEnabled();
    expect(await screen.findByText((_, el) => el?.tagName === "P" && /dev2 approved these changes/.test(el.textContent ?? ""))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Submit review" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Close pull request" })).toBeInTheDocument();
  });

  it("names the protecting rule, its refusal, and an owner's bypass", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me("maint"), "/repository/acme%2Fapp": repo("maintainer"),
      "/repository/acme%2Fapp/pulls/7": pull({ protected_by: "main", can_merge: false,
        merge_blocked: { type: "BRANCH_PROTECTED", message: "branch main is protected: you are not allowed to merge into it" } }),
      "/repository/acme%2Fapp/pulls/7/activity": { reviews: [], comments: [] },
      "/repository/acme%2Fapp/labels": { labels: [bug] },
    });
    renderRoutes(routes, "/acme/app/pulls/7");
    expect(await screen.findByText("branch main is protected: you are not allowed to merge into it.")).toBeInTheDocument();
    expect(screen.getByText(/is protected by the rule/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Merge" })).toBeDisabled();
    expect(screen.queryByText(/merge anyway as an owner/)).not.toBeInTheDocument();
  });

  it("tells owners when they would bypass a rule", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me("boss"), "/repository/acme%2Fapp": repo("owner"),
      "/repository/acme%2Fapp/pulls/7": pull({ protected_by: "main", can_merge: true, required_approvals: 2, approved: [] }),
      "/repository/acme%2Fapp/pulls/7/activity": { reviews: [], comments: [] },
      "/repository/acme%2Fapp/labels": { labels: [bug] },
    });
    renderRoutes(routes, "/acme/app/pulls/7");
    expect(await screen.findByText(/merge anyway as an owner/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Merge" })).toBeEnabled();
  });

  it("lists the head's checks, required ones first, with safe links", async () => {
    const st = (context: string, state: string, target_url = "") => ({ id: context, context, state, description: `${context} ${state}`, target_url, created_at: "2026-01-01T00:00:00Z" });
    fakeAPI({
      "/instance": instance, "/user/profile": me("maint"), "/repository/acme%2Fapp": repo("maintainer"),
      "/repository/acme%2Fapp/pulls/7": pull({ protected_by: "main", required_checks: [{ context: "lint", state: "failure" }, { context: "e2e", state: "" }],
        merge_blocked: { type: "CHECKS_REQUIRED", message: "required checks are not passing: lint (failure), e2e (not reported)" } }),
      "/repository/acme%2Fapp/pulls/7/activity": { reviews: [], comments: [] },
      "/repository/acme%2Fapp/labels": { labels: [bug] },
      [`/repository/r1/status?ref=${"h".repeat(40)}`]: { sha: "h".repeat(40), state: "failure", total_count: 2, statuses: [
        st("build", "success", "https://ci.example.com/1"), st("lint", "failure", "javascript:alert(1)")] },
    });
    renderRoutes(routes, "/acme/app/pulls/7");
    expect(await screen.findByText("0 of 2 required checks passed.")).toBeInTheDocument();
    expect(await screen.findByText("build success")).toBeInTheDocument();
    const box = within(screen.getByText("Checks").parentElement!);
    expect(box.getAllByRole("listitem").map((li) => li.querySelector(".font-medium")?.textContent)).toEqual(["lint", "build", "e2e"]);
    expect(screen.getByText("Waiting for a report.")).toBeInTheDocument();
    const details = screen.getAllByRole("link", { name: "Details" });
    expect(details).toHaveLength(1); // the javascript: URL gets no link
    expect(details[0]).toHaveAttribute("href", "https://ci.example.com/1");
    expect(details[0]).toHaveAttribute("rel", "noopener noreferrer");
    expect(screen.getByRole("button", { name: "Merge" })).toBeDisabled();
  });

  it("reloads the merge box when checks change", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const st = (state: string) => ({ sha: "h".repeat(40), state, total_count: 1, statuses: [{ id: "s", context: "lint", state, description: "", target_url: "", created_at: "2026-01-01T00:00:00Z" }] });
    const table: Record<string, unknown> = {
      "/instance": instance, "/user/profile": me("maint"), "/repository/acme%2Fapp": repo("maintainer"),
      "/repository/acme%2Fapp/pulls/7": pull({ protected_by: "main", required_checks: [{ context: "lint", state: "pending" }], can_merge: false }),
      "/repository/acme%2Fapp/pulls/7/activity": { reviews: [], comments: [] },
      "/repository/acme%2Fapp/labels": { labels: [bug] },
      [`/repository/r1/status?ref=${"h".repeat(40)}`]: st("pending"),
    };
    fakeAPI(table);
    renderRoutes(routes, "/acme/app/pulls/7");
    expect(await screen.findByText("0 of 1 required checks passed.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Merge" })).toBeDisabled();

    // CI reports success: the checks poll picks it up, and the pull request follows.
    table[`/repository/r1/status?ref=${"h".repeat(40)}`] = st("success");
    table["/repository/acme%2Fapp/pulls/7"] = pull({ protected_by: "main", required_checks: [{ context: "lint", state: "success" }], can_merge: true });
    await vi.advanceTimersByTimeAsync(11_000);
    expect(await screen.findByText("1 of 1 required checks passed.")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Merge" })).toBeEnabled());
    vi.useRealTimers();
  });

  it("shows status icons in the list", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me("dev"), "/repository/acme%2Fapp": repo("developer"),
      "/repository/acme%2Fapp/pulls?state=open&limit=30&offset=0": { pull_requests: [{ ...summary, head_commit: "c".repeat(40) }], counts: { open: 1, merged: 0, closed: 0 } },
      "/repository/acme%2Fapp/labels": { labels: [bug] },
      [`/repository/r1/statuses?shas=${"c".repeat(40)}`]: { commits: { ["c".repeat(40)]: { sha: "c".repeat(40), state: "success", total_count: 2, statuses: [{ state: "success" }, { state: "success" }] } } },
    });
    renderRoutes(routes, "/acme/app/pulls");
    expect(await screen.findByRole("img", { name: "2 of 2 checks passed" })).toBeInTheDocument();
  });

  it("shows conflicting files", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me("maint"), "/repository/acme%2Fapp": repo("maintainer"),
      "/repository/acme%2Fapp/pulls/7": pull({ mergeable: false, conflicts: ["go.mod", "main.go"] }),
      "/repository/acme%2Fapp/pulls/7/activity": { reviews: [], comments: [] },
      "/repository/acme%2Fapp/labels": { labels: [] },
    });
    renderRoutes(routes, "/acme/app/pulls/7");
    expect(await screen.findByText(/Conflicts in go.mod, main.go/)).toBeInTheDocument();
  });
});

describe("new pull request", () => {
  it("previews the changes and creates it", async () => {
    const commit = { hash: "c".repeat(40), parents: [], message: "Add login form\n", author: { name: "Dev", email: "d@x", when: "2026-01-01T00:00:00Z" }, committer: { name: "Dev", email: "d@x", when: "2026-01-01T00:00:00Z" } };
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me("dev"), "/repository/acme%2Fapp": repo("developer"),
      "/repository/acme%2Fapp/refs": { default_branch: "main", branches: [{ name: "main", commit }, { name: "feature/login", commit }], tags: [] },
      "/repository/acme%2Fapp/compare?base=main&head=feature%2Flogin": { merge_base: "", commits: [commit], commits_truncated: false, diff: { files: [], additions: 0, deletions: 0, truncated: false } },
      "POST /repository/acme%2Fapp/pulls": pull({ number: 8 }),
      "/repository/acme%2Fapp/pulls/8": pull({ number: 8 }),
      "/repository/acme%2Fapp/pulls/8/activity": { reviews: [], comments: [] },
      "/repository/acme%2Fapp/labels": { labels: [] },
    });
    renderRoutes(routes, "/acme/app/pulls/new?source=feature/login");
    const title = await screen.findByLabelText("Title");
    expect(title).toHaveAttribute("placeholder", "Add login form");
    await userEvent.click(screen.getByRole("button", { name: "Create pull request" }));
    await screen.findByRole("heading", { name: /Add login/ });
    const post = fetch.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === "POST")!;
    expect(JSON.parse((post[1] as RequestInit).body as string)).toEqual({ title: "Add login form", body: "", source: "feature/login", target: "main", draft: false });
  });
});
