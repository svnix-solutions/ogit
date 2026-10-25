import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { issuesQuery, progress } from "@/lib/issues";
import { fakeAPI, renderRoutes } from "@/test/render";

afterEach(() => vi.unstubAllGlobals());

const instance = { version: "dev", registration_enabled: true, ssh_enabled: false };
const me = (name: string) => ({ id: "u", username: name, email: `${name}@x`, avatar: "", role: "user" });
const repo = (role: string, extra: object = {}) => ({ id: "r1", namespace: "acme", slug: "app", full_path: "acme/app", name: "App", description: "",
  is_private: true, archived: false, owner_type: "group", role, has_issues: true, ...extra });
const anon = [401, { code: 401, details: "authentication required", type: "x" }];
const issue = (over: object = {}) => ({
  number: 4, title: "Crash on start", state: "open", author: "rep", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
  labels: [], assignees: [], body: "It crashes, see #7", can_edit: true, can_comment: true, can_label: true, can_delete: false, ...over,
});
const v1 = { id: "m1", title: "v1", description: "First", due_on: "2026-12-01T00:00:00Z", state: "open", open: 1, closed: 3 };

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

describe("helpers", () => {
  it("builds list queries and progress", () => {
    expect(issuesQuery({ state: "open", label: "bug", q: "a b", milestone: "" }, 1, 30)).toBe("state=open&limit=30&offset=30&label=bug&q=a+b");
    expect(progress({ open: 1, closed: 3 })).toBe(75);
    expect(progress({ open: 0, closed: 0 })).toBe(0);
  });
});

describe("issues", () => {
  it("lists with filters from the URL and counts", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me("rep"), "/repository/acme%2Fapp": repo("reporter"),
      "/repository/acme%2Fapp/issues?state=open&limit=30&offset=0&milestone=m1&mine=assigned": { issues: [issue({ milestone: { id: "m1", title: "v1", state: "open" }, assignees: ["dev"] })], counts: { open: 1, closed: 2 } },
      "/repository/acme%2Fapp/labels": { labels: [] },
      "/repository/acme%2Fapp/milestones?state=all": { milestones: [v1] },
    });
    renderRoutes(routes, "/acme/app/issues?milestone=m1&mine=assigned");
    expect(await screen.findByRole("link", { name: "Crash on start" })).toHaveAttribute("href", "/acme/app/issues/4");
    expect(screen.getByRole("button", { name: /closed\s*2/i })).toBeInTheDocument();
    expect(screen.getByText("dev")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New issue" })).toHaveAttribute("href", "/acme/app/issues/new");
    expect(screen.getByRole("link", { name: /Issues/ })).toHaveAttribute("href", "/acme/app/issues");

    await userEvent.type(screen.getByLabelText("Search issues"), "crash");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(fetch.mock.calls.some(([url]) => String(url).includes("q=crash") && String(url).includes("milestone=m1"))).toBe(true));
  });

  it("hides the tab and new issues when issues are off", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me("rep"), "/repository/acme%2Fapp": repo("reporter", { has_issues: false }),
      "/repository/acme%2Fapp/issues?state=open&limit=30&offset=0": { issues: [], counts: {} },
      "/repository/acme%2Fapp/labels": { labels: [] }, "/repository/acme%2Fapp/milestones?state=all": { milestones: [] },
    });
    renderRoutes(routes, "/acme/app/issues");
    expect(await screen.findByText(/Issues are turned off/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New issue" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /^Issues$/ })).not.toBeInTheDocument();
  });

  it("shows an issue, comments and closes it, and sets assignees and the milestone", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me("dev"), "/repository/acme%2Fapp": repo("developer"),
      "/repository/acme%2Fapp/issues/4": issue(),
      "/repository/acme%2Fapp/issues/4/comments": { comments: [{ id: "c1", author: "maint", body: "Thanks", created_at: "2026-01-02T00:00:00Z", updated_at: "", can_edit: false, can_delete: false }] },
      "/repository/acme%2Fapp/labels": { labels: [] },
      "/repository/acme%2Fapp/members": { members: [{ id: "1", type: "user", username: "dev", role: "developer" }, { id: "2", type: "user", username: "rep", role: "reporter" }] },
      "/repository/acme%2Fapp/milestones?state=open": { milestones: [v1] },
      "POST /repository/acme%2Fapp/issues/4/comments": { id: "c2" },
      "PATCH /repository/acme%2Fapp/issues/4": issue({ state: "closed" }),
      "PUT /repository/acme%2Fapp/issues/4/assignees": { assignees: ["dev"] },
      "PUT /repository/acme%2Fapp/issues/4/milestone": issue(),
    });
    renderRoutes(routes, "/acme/app/issues/4");
    expect(await screen.findByText("Thanks")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "#7" })).toHaveAttribute("href", "/acme/app/issues/7");

    await userEvent.type(screen.getByLabelText("Comment"), "Fixed in main");
    await userEvent.click(screen.getByRole("button", { name: "Close with comment" }));
    await waitFor(() => expect(sent(fetch, "POST", "/repository/acme%2Fapp/issues/4/comments")).toEqual([{ body: "Fixed in main" }]));
    await waitFor(() => expect(sent(fetch, "PATCH", "/repository/acme%2Fapp/issues/4")).toEqual([{ state: "closed" }]));

    // Only developers and up are offered as assignees.
    expect(await screen.findByRole("checkbox", { name: "dev" })).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: "rep" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: "dev" }));
    await waitFor(() => expect(sent(fetch, "PUT", "/repository/acme%2Fapp/issues/4/assignees")).toEqual([{ assignees: ["dev"] }]));
    await userEvent.selectOptions(screen.getByLabelText("Milestone"), "m1");
    await waitFor(() => expect(sent(fetch, "PUT", "/repository/acme%2Fapp/issues/4/milestone")).toEqual([{ milestone_id: "m1" }]));
  });

  it("shows read-only boxes and who closed it with which pull request", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": anon, "/repository/acme%2Fapp": repo("none", { is_private: false }),
      "/repository/acme%2Fapp/issues/4": issue({ state: "closed", closed_by: "maint", closed_by_pull: 9, can_edit: false, can_comment: false, can_label: false, assignees: ["dev"] }),
      "/repository/acme%2Fapp/issues/4/comments": { comments: [] },
      "/repository/acme%2Fapp/labels": { labels: [] },
      "/repository/acme%2Fapp/milestones?state=open": { milestones: [] },
    });
    renderRoutes(routes, "/acme/app/issues/4");
    expect(await screen.findByRole("link", { name: "#9" })).toHaveAttribute("href", "/acme/app/pulls/9");
    expect(screen.getByText("dev")).toBeInTheDocument();
    expect(screen.queryByLabelText("Comment")).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });

  it("forwards #n to the pull request when n is one", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me("dev"), "/repository/acme%2Fapp": repo("developer"),
      "/repository/acme%2Fapp/issues/7": [404, { code: 404, details: "issue not found", type: "ISSUE_NOT_FOUND" }],
      "/repository/acme%2Fapp/numbers/7": { type: "pull" },
    });
    const router = renderRoutes(routes, "/acme/app/issues/7");
    await waitFor(() => expect(router.state.location.pathname).toBe("/acme/app/pulls/7"));
  });

  it("opens an issue", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me("rep"), "/repository/acme%2Fapp": repo("reporter"),
      "POST /repository/acme%2Fapp/issues": issue({ number: 12 }),
      "/repository/acme%2Fapp/issues/12": issue({ number: 12 }),
      "/repository/acme%2Fapp/issues/12/comments": { comments: [] },
      "/repository/acme%2Fapp/labels": { labels: [] }, "/repository/acme%2Fapp/milestones?state=open": { milestones: [] },
    });
    const router = renderRoutes(routes, "/acme/app/issues/new");
    await userEvent.type(await screen.findByLabelText("Title"), "Crash on start");
    await userEvent.type(screen.getByLabelText("Description"), "Steps");
    await userEvent.click(screen.getByRole("button", { name: "Open issue" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/acme/app/issues/12"));
    expect(sent(fetch, "POST", "/repository/acme%2Fapp/issues")).toEqual([{ title: "Crash on start", body: "Steps" }]);
  });
});

describe("milestones", () => {
  it("shows progress and lets maintainers create one", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me("maint"), "/repository/acme%2Fapp": repo("maintainer"),
      "/repository/acme%2Fapp/milestones?state=open": { milestones: [v1] },
      "POST /repository/acme%2Fapp/milestones": v1,
    });
    renderRoutes(routes, "/acme/app/milestones");
    expect(await screen.findByRole("link", { name: "v1" })).toHaveAttribute("href", "/acme/app/issues?milestone=m1&state=all");
    expect(screen.getByRole("progressbar", { name: "v1 progress" })).toHaveAttribute("aria-valuenow", "75");
    expect(screen.getByText(/75% complete · 1 open · 3 closed/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "New milestone" }));
    await userEvent.type(screen.getByLabelText("Title"), "v2");
    await userEvent.type(screen.getByLabelText("Due date"), "2027-03-01");
    await userEvent.click(screen.getByRole("button", { name: "Create milestone" }));
    await waitFor(() => expect(sent(fetch, "POST", "/repository/acme%2Fapp/milestones")).toEqual([{ title: "v2", description: "", due_on: "2027-03-01T00:00:00.000Z" }]));
  });

  it("is read-only for developers", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me("dev"), "/repository/acme%2Fapp": repo("developer"),
      "/repository/acme%2Fapp/milestones?state=open": { milestones: [v1] },
    });
    renderRoutes(routes, "/acme/app/milestones");
    expect(await screen.findByRole("link", { name: "v1" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New milestone" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
  });
});
