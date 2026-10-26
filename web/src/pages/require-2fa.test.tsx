import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { fakeAPI, renderRoutes } from "@/test/render";

afterEach(() => vi.unstubAllGlobals());

const instance = { version: "dev", registration_enabled: true, ssh_enabled: false };
const me = (extra: object = {}) => ({ id: "u1", username: "alice", email: "a@example.com", avatar: "", role: "user", ...extra });
const status = { enabled: false, totp: false, keys: [], recovery_codes_left: 0, totp_available: true, webauthn_available: true };

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

describe("required two-factor authentication", () => {
  it("replaces every page with the setup until the user enrolls", async () => {
    // The server refuses the rest (TWO_FACTOR_REQUIRED); the UI shows the setup instead.
    fakeAPI({ "/instance": instance, "/user/profile": me({ two_factor_required: true }), "/user/2fa": status });
    renderRoutes(routes, "/explore");
    expect(await screen.findByRole("heading", { name: "Set up two-factor authentication" })).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Set up an authenticator app" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /Explore/ })).not.toBeInTheDocument();
  });

  it("shows the recovery codes before letting the user in", async () => {
    let enabled = false;
    fakeAPI({
      "/instance": instance,
      "/user/profile": () => me({ two_factor_required: !enabled, two_factor_enabled: enabled }),
      "/user/2fa": () => ({ ...status, enabled, totp: enabled }),
      "POST /user/2fa/totp": { secret: "JBSWY3DPEHPK3PXP", uri: "otpauth://totp/oGit:alice", qr: ["1"] },
      "POST /user/2fa/totp/confirm": () => ((enabled = true), { recovery_codes: ["aaaaa-bbbbb"] }),
      "/explore?limit=30&offset=0": { repositories: [], total: 0 },
    });
    renderRoutes(routes, "/explore");
    await userEvent.click(await screen.findByRole("button", { name: "Set up an authenticator app" }));
    await userEvent.type(await screen.findByLabelText("Code from the app"), "123456");
    await userEvent.click(screen.getByRole("button", { name: "Turn on" }));
    // The requirement is met now, but the codes must be seen first.
    expect(await screen.findByRole("list", { name: "Recovery codes" })).toHaveTextContent("aaaaa-bbbbb");
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.getByRole("list", { name: "Recovery codes" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "I've saved them" }));
    await waitFor(() => expect(screen.queryByRole("heading", { name: "Set up two-factor authentication" })).not.toBeInTheDocument());
  });

  it("lets admins require it, unless the configuration already does", async () => {
    vi.stubGlobal("confirm", vi.fn(() => true));
    const admin = me({ role: "admin", two_factor_enabled: true });
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": admin,
      "/admin/settings": { require_two_factor: false, require_two_factor_forced: false },
      "PATCH /admin/settings": { require_two_factor: true, require_two_factor_forced: false },
      "/admin/users?limit=50&offset=0&q=": { users: [], total: 0 }, "/admin/repositories?limit=50&offset=0": { repositories: [], total: 0 },
    });
    renderRoutes(routes, "/admin");
    await userEvent.click(await screen.findByLabelText("Require two-factor authentication for everyone"));
    await waitFor(() => expect(sent(fetch, "PATCH", "/admin/settings")).toEqual([{ require_two_factor: true }]));
  });

  it("shows the requirement forced by the configuration", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me({ role: "admin", two_factor_enabled: true }),
      "/admin/settings": { require_two_factor: true, require_two_factor_forced: true },
      "/admin/users?limit=50&offset=0&q=": { users: [], total: 0 }, "/admin/repositories?limit=50&offset=0": { repositories: [], total: 0 },
    });
    renderRoutes(routes, "/admin");
    const box = await screen.findByLabelText(/Require two-factor authentication for everyone/);
    expect(box).toBeChecked();
    expect(box).toBeDisabled();
    expect(screen.getByText(/security.require_2fa/)).toBeInTheDocument();
  });

  it("asks admins without 2FA to set it up first", async () => {
    fakeAPI({ "/instance": instance, "/user/profile": me({ role: "admin" }) });
    renderRoutes(routes, "/admin");
    expect(await screen.findByText(/Site administration needs two-factor authentication/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Set it up in your settings" })).toHaveAttribute("href", "/settings");
  });

  it("lets a group's owner require it, and shows who lacks it", async () => {
    let required = false;
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me(),
      "/group/secure": () => ({ id: "g1", name: "secure", description: "", role: "owner", require_two_factor: required }),
      "/group/secure/members": { members: [{ username: "alice", role: "owner", two_factor_enabled: true }, { username: "erin", role: "member", two_factor_enabled: false }] },
      "PATCH /group/secure": () => ((required = true), { id: "g1", name: "secure", description: "", role: "owner", require_two_factor: true }),
    });
    renderRoutes(routes, "/groups/secure");
    expect(await screen.findByText("no 2FA")).toBeInTheDocument();
    const box = screen.getByLabelText("Require two-factor authentication for members");
    expect(box).not.toBeChecked();
    await userEvent.click(box);
    await waitFor(() => expect(sent(fetch, "PATCH", "/group/secure")).toEqual([{ require_two_factor: true }]));
    await waitFor(() => expect(screen.getByLabelText("Require two-factor authentication for members")).toBeChecked());
  });

  it("tells members the group requires it", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me(),
      "/group/secure": { id: "g1", name: "secure", description: "", role: "member", require_two_factor: true },
      "/group/secure/members": { members: [{ username: "alice", role: "member" }] },
    });
    renderRoutes(routes, "/groups/secure");
    expect(await screen.findByText("This group requires two-factor authentication.")).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });
});
