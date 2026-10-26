import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { inboxQuery } from "@/lib/notifications";
import { fakeAPI, renderRoutes } from "@/test/render";

afterEach(() => vi.unstubAllGlobals());

const instance = { version: "dev", registration_enabled: true, ssh_enabled: false };
const me = { id: "u1", username: "alice", email: "a@example.com", avatar: "", role: "user" };
const n = (over: object) => ({
  id: "n1", unread: true, reason: "mention", participating: true, repository: "acme/app", actor: "bob", updated_at: "2026-10-26T10:00:00Z",
  subject: { type: "issue", number: 3, title: "Crash on start", url: "/acme/app/issues/3" }, summary: "bob opened issue #3", ...over,
});
const inbox = {
  notifications: [
    n({}),
    n({ id: "n2", reason: "review_requested", subject: { type: "pull_request", number: 5, title: "Add login", url: "/acme/app/pulls/5" }, summary: "bob requested a review from alice on pull request #5" }),
    n({ id: "n3", repository: "acme/web", reason: "subscribed", participating: false, unread: false, subject: { type: "release", title: "v1.0", url: "/acme/web/releases/tag/v1.0" }, summary: "carol published release v1.0" }),
  ],
  total: 3, unread: 2,
};

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

describe("inbox", () => {
  it("builds its query", () => {
    expect(inboxQuery({ all: false, participating: false, repository: "" }, 0, 50)).toBe("offset=0&limit=50");
    expect(inboxQuery({ all: true, participating: true, repository: "acme/app" }, 1, 50)).toBe("offset=50&limit=50&all=true&participating=true&repository=acme%252Fapp");
  });

  it("shows the unread count on the bell", async () => {
    fakeAPI({ "/instance": instance, "/user/profile": me, "/notifications/count": { unread: 4 }, "/explore?limit=30&offset=0": { repositories: [], total: 0 } });
    renderRoutes(routes, "/explore");
    const bell = await screen.findByRole("link", { name: "Notifications (4 unread)" });
    expect(bell).toHaveAttribute("href", "/notifications");
    expect(within(bell).getByText("4")).toBeInTheDocument();
  });

  it("groups threads by repository; opening one marks it read", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me, "/notifications/count": { unread: 2 },
      "/notifications?offset=0&limit=50": inbox,
      "PATCH /notifications/n1": { message: "updated" },
      "/repository/acme%2Fapp": { id: "r1", namespace: "acme", slug: "app", full_path: "acme/app", name: "App", description: "", is_private: false, archived: false, owner_type: "group", role: "reporter", has_issues: true },
      "/repository/acme%2Fapp/issues/3": [404, { code: 404, details: "x", type: "ISSUE_NOT_FOUND" }],
    });
    const router = renderRoutes(routes, "/notifications");
    expect(await screen.findByRole("button", { name: "Unread (2)" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "acme/app" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "acme/web" })).toBeInTheDocument();
    expect(screen.getByText("review requested")).toBeInTheDocument();
    expect(screen.getByText("watching")).toBeInTheDocument();
    expect(screen.getByText("bob requested a review from alice on pull request #5")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("link", { name: /Crash on start/ }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/acme/app/issues/3"));
    expect(sent(fetch, "PATCH", "/notifications/n1")).toEqual([{ unread: false }]);
  });

  it("filters, marks all read, and unsubscribes", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me, "/notifications/count": { unread: 2 },
      "/notifications?offset=0&limit=50": inbox,
      "/notifications?offset=0&limit=50&all=true": inbox,
      "/notifications?offset=0&limit=50&all=true&participating=true": { ...inbox, notifications: inbox.notifications.slice(0, 2) },
      "/notifications?offset=0&limit=50&all=true&participating=true&repository=acme%252Fapp": { ...inbox, notifications: inbox.notifications.slice(0, 2) },
      "POST /notifications/read": { message: "marked as read" },
      "POST /notifications/n2/mute": { muted: true },
    });
    const router = renderRoutes(routes, "/notifications");
    await userEvent.click(await screen.findByRole("button", { name: "All" }));
    await userEvent.click(screen.getByLabelText("Participating only"));
    await waitFor(() => expect(screen.queryByText("v1.0")).not.toBeInTheDocument());
    await userEvent.click(screen.getByRole("button", { name: "acme/app" }));
    await waitFor(() => expect(router.state.location.search).toBe("?all=true&participating=true&repository=acme%2Fapp"));

    await userEvent.click(screen.getByRole("button", { name: "Mark all as read" }));
    await waitFor(() => expect(sent(fetch, "POST", "/notifications/read")).toEqual([{ repository: "acme/app" }]));

    const row = screen.getByRole("link", { name: /Add login/ }).closest("li")!;
    await userEvent.click(within(row).getByRole("button", { name: "Unsubscribe" }));
    await waitFor(() => expect(sent(fetch, "POST", "/notifications/n2/mute")).toHaveLength(1));
    expect(await within(row).findByRole("button", { name: "Subscribe again" })).toBeInTheDocument();
  });

  it("says when you're caught up", async () => {
    fakeAPI({ "/instance": instance, "/user/profile": me, "/notifications/count": { unread: 0 }, "/notifications?offset=0&limit=50": { notifications: [], total: 0, unread: 0 } });
    renderRoutes(routes, "/notifications");
    expect(await screen.findByText("All caught up.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Notifications" })).toBeInTheDocument();
  });
});

describe("watching and email", () => {
  it("sets the repository's watch level", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me, "/notifications/count": { unread: 0 },
      "/repository/acme%2Fapp": { id: "r1", namespace: "acme", slug: "app", full_path: "acme/app", name: "App", description: "", is_private: false, archived: false, owner_type: "group", role: "reporter", has_issues: true },
      "/repository/acme%2Fapp/tree?spec=": { empty: true, ref: "main", path: "", entries: [] },
      "/repository/r1/watch": { level: "participating" },
      "PUT /repository/r1/watch": { level: "releases" },
    });
    renderRoutes(routes, "/acme/app");
    const select = await screen.findByLabelText("Watch");
    expect(select).toHaveValue("participating");
    await userEvent.selectOptions(select, "releases");
    await waitFor(() => expect(sent(fetch, "PUT", "/repository/r1/watch")).toEqual([{ level: "releases" }]));
    await waitFor(() => expect(screen.getByLabelText("Watch")).toHaveValue("releases"));
  });

  it("chooses which notifications are emailed", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me, "/notifications/count": { unread: 0 },
      "/user/tokens": { tokens: [] }, "/user/ssh-keys": { keys: [] },
      "/user/2fa": { enabled: false, totp: false, keys: [], recovery_codes_left: 0, totp_available: true, webauthn_available: false },
      "/notifications/settings": { email: "participating" },
      "PUT /notifications/settings": { email: "none" },
    });
    renderRoutes(routes, "/settings");
    const group = await screen.findByRole("radiogroup", { name: "Notification emails" });
    expect(within(group).getByLabelText(/Threads I take part in/)).toBeChecked();
    await userEvent.click(within(group).getByLabelText(/No emails/));
    await waitFor(() => expect(sent(fetch, "PUT", "/notifications/settings")).toEqual([{ email: "none" }]));
    await waitFor(() => expect(within(group).getByLabelText(/No emails/)).toBeChecked());
  });
});
