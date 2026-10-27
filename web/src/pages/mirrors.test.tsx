import { screen, waitFor, within } from "@testing-library/react";
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
});
