import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { fakeAPI, renderRoutes } from "@/test/render";

afterEach(() => vi.unstubAllGlobals());

const instance = { version: "dev", registration_enabled: true, ssh_enabled: false };
const me = { id: "u", username: "bob", email: "b@x", avatar: "", role: "user" };
const repo = (over: object = {}) => ({ id: "r1", namespace: "bob", slug: "copy", full_path: "bob/copy", name: "Copy", description: "", is_private: true,
  archived: false, owner_type: "user", role: "owner", has_issues: true, ...over });
const mirror = (over: object = {}) => ({ url: "https://example.com/a.git", mirror: true, auth: "password", username: "me", has_password: true, lfs: false,
  interval_seconds: 28800, status: "ok", last_sync_at: "2026-01-01T00:00:00Z", last_success_at: "2026-01-01T00:00:00Z", next_sync_at: "2026-01-01T08:00:00Z", ...over });

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

describe("mirrors", () => {
  it("imports from an https URL as a mirror", async () => {
    const fetch = fakeAPI({ "/instance": instance, "/user/profile": me, "/group": { groups: [] }, "POST /repository/import": { repository: repo(), mirror: mirror({ status: "pending" }) } });
    const router = renderRoutes(routes, "/new?import=1");
    await userEvent.type(await screen.findByLabelText("Name"), "Copy");
    await userEvent.type(screen.getByLabelText("Remote URL"), "https://example.com/a.git");
    await userEvent.type(screen.getByLabelText("Username (optional)"), "me");
    await userEvent.type(screen.getByLabelText("Password or token (optional)"), "tok");
    await userEvent.click(screen.getByLabelText(/Keep it as a mirror/));
    await userEvent.clear(screen.getByLabelText("Sync every"));
    await userEvent.type(screen.getByLabelText("Sync every"), "2");
    await userEvent.click(screen.getByRole("button", { name: "Import repository" }));
    await waitFor(() =>
      expect(sent(fetch, "POST", "/repository/import")).toEqual([
        { namespace: "bob", name: "Copy", description: "", is_private: true, url: "https://example.com/a.git", mirror: true, lfs: false, username: "me", password: "tok", interval_seconds: 7200 },
      ]),
    );
    await waitFor(() => expect(router.state.location.pathname).toBe("/bob/copy"));
  });

  it("imports over SSH without credentials, then shows the key to add", async () => {
    const fetch = fakeAPI({ "/instance": instance, "/user/profile": me, "/group": { groups: [] }, "POST /repository/import": { repository: repo(), mirror: mirror({ auth: "ssh" }) } });
    const router = renderRoutes(routes, "/new?import=1");
    await userEvent.type(await screen.findByLabelText("Name"), "Copy");
    await userEvent.type(screen.getByLabelText("Remote URL"), "git@example.com:a/b.git");
    expect(screen.queryByLabelText("Username (optional)")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Import repository" }));
    await waitFor(() => expect(sent(fetch, "POST", "/repository/import")[0]).not.toHaveProperty("password"));
    await waitFor(() => expect(router.state.location.pathname).toBe("/bob/copy/settings"));
  });

  it("shows an import in progress to maintainers, and a mirror badge to readers", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me, "/repository/bob%2Fcopy": repo(), "/repository/bob%2Fcopy/mirror": mirror({ mirror: false, status: "syncing" }),
      "/repository/bob%2Fcopy/tree?spec=": { empty: true, ref: "main", path: "", entries: [] },
    });
    renderRoutes(routes, "/bob/copy");
    expect(await screen.findByText("Importing from https://example.com/a.git…")).toBeInTheDocument();
  });

  it("manages a mirror in the settings", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me, "/repository/bob%2Fcopy": repo({ is_mirror: true }),
      "/repository/bob%2Fcopy/mirror": mirror({ url: "ssh://git@example.com/a.git", auth: "ssh", ssh_public_key: "ssh-ed25519 AAAAkey ogit-mirror", status: "failed", last_error: "authentication failed" }),
      "POST /repository/bob%2Fcopy/mirror/sync": mirror({ status: "pending" }),
      "PATCH /repository/bob%2Fcopy/mirror": mirror(),
    });
    renderRoutes(routes, "/bob/copy/settings");
    expect(await screen.findByLabelText("Mirror SSH public key")).toHaveValue("ssh-ed25519 AAAAkey ogit-mirror");
    expect(screen.getAllByText("authentication failed").length).toBeGreaterThan(0);
    await userEvent.click(screen.getByRole("button", { name: "Sync now" }));
    await waitFor(() => expect(sent(fetch, "POST", "/repository/bob%2Fcopy/mirror/sync")).toHaveLength(1));
    await userEvent.click(within(document.getElementById("mirror")!).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(sent(fetch, "PATCH", "/repository/bob%2Fcopy/mirror")).toEqual([{ url: "ssh://git@example.com/a.git", lfs: false, interval_seconds: 28800 }]));
  });

  it("adds a push mirror", async () => {
    const created = { id: "pm1", url: "https://example.com/b.git", mode: "branches", auth: "password", username: "me", has_password: true, lfs: true,
      interval_seconds: 28800, status: "pending", last_sync_at: null, last_success_at: null, next_sync_at: "2026-01-01T00:00:00Z", created_at: "2026-01-01T00:00:00Z" };
    let list: object[] = [];
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me, "/repository/bob%2Fcopy": repo(),
      "/repository/bob%2Fcopy/push-mirrors": () => ({ push_mirrors: list }),
      "POST /repository/bob%2Fcopy/push-mirrors": () => ((list = [created]), created),
    });
    renderRoutes(routes, "/bob/copy/settings");
    await userEvent.click(await screen.findByRole("button", { name: "Add a push mirror" }));
    const section = document.getElementById("push-mirrors")!;
    await userEvent.type(within(section).getByLabelText("Remote URL"), "https://example.com/b.git");
    await userEvent.type(within(section).getByLabelText("Username"), "me");
    await userEvent.type(within(section).getByLabelText("Password or token"), "tok");
    await userEvent.selectOptions(within(section).getByLabelText("Mode"), "branches");
    await userEvent.click(within(section).getByLabelText("Push Git LFS objects too"));
    await userEvent.click(within(section).getByRole("button", { name: "Add push mirror" }));
    await waitFor(() =>
      expect(sent(fetch, "POST", "/repository/bob%2Fcopy/push-mirrors")).toEqual([
        { url: "https://example.com/b.git", mode: "branches", lfs: true, interval_seconds: 28800, username: "me", password: "tok" },
      ]),
    );
    expect(await within(section).findByText("https://example.com/b.git")).toBeInTheDocument();
  });

  it("shows, pushes, edits and removes push mirrors", async () => {
    vi.stubGlobal("confirm", () => true);
    const pm = { id: "pm1", url: "ssh://git@example.com/b.git", mode: "mirror", auth: "ssh", has_password: false, ssh_public_key: "ssh-ed25519 AAAApush ogit-mirror",
      lfs: false, interval_seconds: 3600, status: "failed", last_error: "not pushed: main: diverged from the remote (not a fast-forward)",
      last_sync_at: null, last_success_at: null, next_sync_at: "2026-01-01T00:00:00Z", created_at: "2026-01-01T00:00:00Z" };
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me, "/repository/bob%2Fcopy": repo(),
      "/repository/bob%2Fcopy/push-mirrors": { push_mirrors: [pm] },
      "POST /repository/bob%2Fcopy/push-mirrors/pm1/sync": { ...pm, status: "pending" },
      "PATCH /repository/bob%2Fcopy/push-mirrors/pm1": pm,
      "DELETE /repository/bob%2Fcopy/push-mirrors/pm1": { message: "push mirror removed" },
    });
    renderRoutes(routes, "/bob/copy/settings");
    expect(await screen.findByLabelText("Push mirror SSH public key")).toHaveValue("ssh-ed25519 AAAApush ogit-mirror");
    const row = screen.getByTestId("push-mirror");
    expect(within(row).getByText(/main: diverged/)).toBeInTheDocument();
    await userEvent.click(within(row).getByRole("button", { name: "Push now" }));
    await waitFor(() => expect(sent(fetch, "POST", "/repository/bob%2Fcopy/push-mirrors/pm1/sync")).toHaveLength(1));
    await userEvent.click(within(row).getByRole("button", { name: "Edit" }));
    expect(within(row).queryByLabelText("Password or token")).not.toBeInTheDocument();
    await userEvent.clear(within(row).getByLabelText("Also push every"));
    await userEvent.type(within(row).getByLabelText("Also push every"), "2");
    await userEvent.click(within(row).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(sent(fetch, "PATCH", "/repository/bob%2Fcopy/push-mirrors/pm1")).toEqual([{ url: "ssh://git@example.com/b.git", mode: "mirror", lfs: false, interval_seconds: 7200 }]),
    );
    await userEvent.click(within(row).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(sent(fetch, "DELETE", "/repository/bob%2Fcopy/push-mirrors/pm1")).toHaveLength(1));
  });

  it("imports issues and pull requests too, guessing the forge", async () => {
    const fetch = fakeAPI({ "/instance": instance, "/user/profile": me, "/group": { groups: [] }, "POST /repository/import": { repository: repo(), mirror: mirror({ mirror: false, status: "pending" }) } });
    renderRoutes(routes, "/new?import=1");
    await userEvent.type(await screen.findByLabelText("Name"), "Copy");
    await userEvent.type(screen.getByLabelText("Remote URL"), "https://gitlab.com/group/app.git");
    await userEvent.click(screen.getByLabelText(/Also import issues/));
    expect(screen.getByLabelText("From")).toHaveValue("gitlab");
    await userEvent.type(screen.getByLabelText("API token (optional)"), "glpat");
    await userEvent.click(screen.getByRole("button", { name: "Import repository" }));
    await waitFor(() =>
      expect(sent(fetch, "POST", "/repository/import")).toEqual([
        { namespace: "bob", name: "Copy", description: "", is_private: true, url: "https://gitlab.com/group/app.git", mirror: false, lfs: false, username: "", password: "",
          forge: "gitlab", api_token: "glpat" },
      ]),
    );
  });

  it("shows an API import's progress, and resumes a failed one", async () => {
    const running = { forge: "github", api_url: "https://api.github.com", project: "o/r", status: "running", stage: "issues", issues: 12, pulls: 1, releases: 0,
      comments: 30, warnings: [], next_run_at: "2026-01-01T00:00:00Z", finished_at: null };
    let state: object = running;
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me, "/repository/bob%2Fcopy": repo(), "/repository/bob%2Fcopy/import": () => state,
      "POST /repository/bob%2Fcopy/import/retry": { ...running, status: "pending" },
      "/repository/bob%2Fcopy/tree?spec=": { empty: true, ref: "main", path: "", entries: [] },
    });
    renderRoutes(routes, "/bob/copy");
    expect(await screen.findByText(/Importing issues, pull requests and releases from GitHub… 12 issues, 1 pull request and 0 releases so far/)).toBeInTheDocument();
    cleanup();
    state = { ...running, status: "failed", last_error: "the forge refused the API token" };
    renderRoutes(routes, "/bob/copy");
    expect(await screen.findByText(/failed: the forge refused the API token/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Resume" }));
    await waitFor(() => expect(sent(fetch, "POST", "/repository/bob%2Fcopy/import/retry")).toHaveLength(1));
  });
});
