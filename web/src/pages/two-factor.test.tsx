import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { fakeAPI, renderRoutes } from "@/test/render";

afterEach(() => vi.unstubAllGlobals());

const instance = { version: "dev", registration_enabled: true, ssh_enabled: false };
const me = (extra: object = {}) => ({ id: "u1", username: "alice", email: "a@example.com", avatar: "", role: "user", ...extra });
const anon = [401, { code: 401, details: "authentication required", type: "x" }];

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

describe("sign-in with two-factor authentication", () => {
  it("asks for a code after the password", async () => {
    let signedIn = false;
    const fetch = fakeAPI({
      "/instance": instance,
      "/user/profile": () => (signedIn ? me() : anon),
      "POST /auth/session": { two_factor_required: true, challenge: "ch1" },
      "POST /auth/session/2fa": (init: RequestInit) => {
        const { code } = JSON.parse(init.body as string);
        if (code !== "123456") return [401, { code: 401, details: "invalid or already used code", type: "INVALID_CODE" }];
        signedIn = true;
        return { expires_at: "2026-12-01T00:00:00Z" };
      },
      "/repository": { repositories: [] },
    });
    const router = renderRoutes(routes, "/login?next=/settings");
    await userEvent.type(await screen.findByLabelText("Email"), "a@example.com");
    await userEvent.type(screen.getByLabelText("Password"), "pw");
    await userEvent.click(within(screen.getByLabelText("Password").closest("form")!).getByRole("button", { name: "Sign in" }));

    const code = await screen.findByLabelText("Authentication code");
    expect(router.state.location.pathname).toBe("/login");
    await userEvent.type(code, "000000");
    await userEvent.click(screen.getByRole("button", { name: "Verify" }));
    expect(await screen.findByText(/That code didn't work/)).toBeInTheDocument();
    await userEvent.clear(code);
    await userEvent.type(code, "123456");
    await userEvent.click(screen.getByRole("button", { name: "Verify" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/settings"));
    expect(sent(fetch, "POST", "/auth/session/2fa").at(-1)).toEqual({ challenge: "ch1", code: "123456" });
  });

  it("starts over when the sign-in expired", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": anon,
      "POST /auth/session": { two_factor_required: true, challenge: "ch1" },
      "POST /auth/session/2fa": [401, { code: 401, details: "expired", type: "CHALLENGE_EXPIRED" }],
    });
    renderRoutes(routes, "/login");
    await userEvent.type(await screen.findByLabelText("Email"), "a@example.com");
    await userEvent.type(screen.getByLabelText("Password"), "pw");
    await userEvent.click(within(screen.getByLabelText("Password").closest("form")!).getByRole("button", { name: "Sign in" }));
    await userEvent.type(await screen.findByLabelText("Authentication code"), "123456");
    await userEvent.click(screen.getByRole("button", { name: "Verify" }));
    await userEvent.click(await screen.findByRole("button", { name: "Sign in again" }));
    expect(await screen.findByLabelText("Password")).toHaveValue("");
  });
});

describe("two-factor settings", () => {
  it("sets up an authenticator app and shows the recovery codes once", async () => {
    let enabled = false;
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": () => me({ two_factor_enabled: enabled }),
      "/user/tokens": { tokens: [] }, "/user/ssh-keys": { keys: [] },
      "/user/2fa": () => ({ enabled, recovery_codes_left: enabled ? 10 : 0, totp_available: true }),
      "POST /user/2fa/totp": { secret: "JBSWY3DPEHPK3PXP", uri: "otpauth://totp/oGit:alice?secret=JBSWY3DPEHPK3PXP", qr: ["101", "010", "101"] },
      "POST /user/2fa/totp/confirm": () => ((enabled = true), { recovery_codes: ["aaaaa-bbbbb", "ccccc-ddddd"] }),
    });
    renderRoutes(routes, "/settings");
    await userEvent.click(await screen.findByRole("button", { name: "Set up an authenticator app" }));
    const qr = await screen.findByRole("img", { name: "QR code for your authenticator app" });
    expect(qr.querySelector("path")?.getAttribute("d")).toBe("M0 0h1v1h-1zM2 0h1v1h-1zM1 1h1v1h-1zM0 2h1v1h-1zM2 2h1v1h-1z");
    expect(screen.getByLabelText("setup key")).toHaveValue("JBSWY3DPEHPK3PXP");
    await userEvent.type(screen.getByLabelText("Code from the app"), "123456");
    await userEvent.click(screen.getByRole("button", { name: "Turn on" }));
    const list = await screen.findByRole("list", { name: "Recovery codes" });
    expect(within(list).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["aaaaa-bbbbb", "ccccc-ddddd"]);
    expect(screen.getByRole("link", { name: "Download" })).toHaveAttribute("download", "ogit-recovery-codes.txt");
    expect(sent(fetch, "POST", "/user/2fa/totp/confirm")).toEqual([{ code: "123456" }]);
    await userEvent.click(screen.getByRole("button", { name: "I've saved them" }));
    expect(await screen.findByText(/10 recovery codes left/)).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Recovery codes" })).not.toBeInTheDocument();
  });

  it("explains when the server can't store authenticator secrets", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me(), "/user/tokens": { tokens: [] }, "/user/ssh-keys": { keys: [] },
      "/user/2fa": { enabled: false, recovery_codes_left: 0, totp_available: false },
    });
    renderRoutes(routes, "/settings");
    expect(await screen.findByText(/security.encryption_key/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Set up an authenticator app" })).not.toBeInTheDocument();
  });
});

describe("sudo", () => {
  it("asks to confirm, then retries the action", async () => {
    let confirmed = false;
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me({ two_factor_enabled: true }), "/user/ssh-keys": { keys: [] },
      "/user/2fa": { enabled: true, recovery_codes_left: 10, totp_available: true },
      "/user/tokens": { tokens: [] },
      "POST /user/token": () =>
        confirmed ? { token: "ogit_x", id: "t1", name: "laptop" } : [403, { code: 403, details: "confirm", type: "SUDO_REQUIRED" }],
      "POST /auth/sudo": (init: RequestInit) => {
        if (JSON.parse(init.body as string).code !== "654321") return [401, { code: 401, details: "bad", type: "INVALID_CODE" }];
        confirmed = true;
        return { until: "2026-10-26T12:10:00Z" };
      },
    });
    renderRoutes(routes, "/settings");
    await userEvent.type(await screen.findByLabelText("Name"), "laptop");
    await userEvent.click(screen.getByRole("button", { name: "Create token" }));

    const dialog = await screen.findByRole("dialog", { name: "Confirm it's you" });
    await userEvent.type(within(dialog).getByLabelText("Authentication code"), "000000");
    await userEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    expect(await within(dialog).findByText("That code didn't work.")).toBeInTheDocument();
    await userEvent.clear(within(dialog).getByLabelText("Authentication code"));
    await userEvent.type(within(dialog).getByLabelText("Authentication code"), "654321");
    await userEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(sent(fetch, "POST", "/user/token")).toHaveLength(2)); // retried once
    expect(await screen.findByDisplayValue("ogit_x")).toBeInTheDocument();
  });

  it("asks for the password without two-factor, and cancelling fails the action", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me(), "/user/ssh-keys": { keys: [] }, "/user/tokens": { tokens: [] },
      "/user/2fa": { enabled: false, recovery_codes_left: 0, totp_available: true },
      "POST /user/token": [403, { code: 403, details: "confirm it's you first", type: "SUDO_REQUIRED" }],
    });
    renderRoutes(routes, "/settings");
    await userEvent.type(await screen.findByLabelText("Name"), "laptop");
    await userEvent.click(screen.getByRole("button", { name: "Create token" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("Password")).toHaveAttribute("type", "password");
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(await screen.findByText("confirm it's you first")).toBeInTheDocument();
    expect(sent(fetch, "POST", "/user/token")).toHaveLength(1);
  });
});

describe("admin", () => {
  it("resets a user's two-factor authentication", async () => {
    vi.stubGlobal("confirm", vi.fn(() => true));
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me({ role: "admin", two_factor_enabled: true }),
      "/admin/settings": { require_two_factor: false, require_two_factor_forced: false },
      "/admin/users?limit=50&offset=0&q=": { users: [{ id: "u2", username: "bob", email: "b@x", role: "user", active: true, email_verified_at: "2026-01-01T00:00:00Z", created_at: "2026-01-01T00:00:00Z", two_factor_enabled: true }], total: 1 },
      "/admin/repositories?limit=50&offset=0": { repositories: [], total: 0 },
      "POST /admin/users/bob/reset-2fa": { message: "two-factor authentication reset" },
    });
    renderRoutes(routes, "/admin");
    expect(await screen.findByText("2FA")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Reset 2FA" }));
    await waitFor(() => expect(sent(fetch, "POST", "/admin/users/bob/reset-2fa")).toHaveLength(1));
  });
});
