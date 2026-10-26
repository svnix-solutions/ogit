import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { creationOptions, credentialJSON, fromB64url, requestOptions, toB64url } from "@/lib/webauthn";
import { fakeAPI, renderRoutes } from "@/test/render";

const instance = { version: "dev", registration_enabled: true, ssh_enabled: false, passkeys: true };
const me = (extra: object = {}) => ({ id: "u1", username: "alice", email: "a@example.com", avatar: "", role: "user", ...extra });
const anon = [401, { code: 401, details: "authentication required", type: "x" }];
const bytes = (...b: number[]) => new Uint8Array(b).buffer;
const assertionOptions = { ceremony: "cer1", options: { publicKey: { challenge: "AAEC", rpId: "git.example.com", allowCredentials: [{ id: "BAU", type: "public-key" }] } } };
const creation = { ceremony: "cer2", options: { publicKey: { challenge: "AAEC", rp: { id: "git.example.com", name: "oGit" }, user: { id: "dTE", name: "alice", displayName: "alice" }, pubKeyCredParams: [{ type: "public-key", alg: -7 }] } } };

// A browser's answers, as the WebAuthn API returns them.
const assertion = {
  id: "BAU", rawId: bytes(4, 5), type: "public-key", getClientExtensionResults: () => ({}),
  response: { clientDataJSON: bytes(1), authenticatorData: bytes(2), signature: bytes(3), userHandle: bytes(117, 49) },
};
const attestation = {
  id: "BgY", rawId: bytes(6, 6), type: "public-key", getClientExtensionResults: () => ({}),
  response: { clientDataJSON: bytes(1), attestationObject: bytes(7), getTransports: () => ["usb"] },
};

let credentials: { create: ReturnType<typeof vi.fn>; get: ReturnType<typeof vi.fn> };
beforeEach(() => {
  credentials = { create: vi.fn(async () => attestation), get: vi.fn(async () => assertion) };
  vi.stubGlobal("PublicKeyCredential", function () {});
  Object.defineProperty(navigator, "credentials", { value: credentials, configurable: true });
});
afterEach(() => vi.unstubAllGlobals());

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

describe("conversions", () => {
  it("decodes options and encodes answers as base64url", () => {
    expect(toB64url(fromB64url("-_8"))).toBe("-_8");
    expect(new Uint8Array(fromB64url("AAEC"))).toEqual(new Uint8Array([0, 1, 2]));
    const c = creationOptions(creation.options.publicKey);
    expect(new Uint8Array(c.challenge as ArrayBuffer)).toEqual(new Uint8Array([0, 1, 2]));
    expect(new Uint8Array(c.user.id as ArrayBuffer)).toEqual(new Uint8Array([117, 49]));
    const r = requestOptions(assertionOptions.options.publicKey);
    expect(new Uint8Array(r.allowCredentials![0].id as ArrayBuffer)).toEqual(new Uint8Array([4, 5]));
    expect(credentialJSON(assertion as unknown as PublicKeyCredential)).toEqual({
      id: "BAU", rawId: "BAU", type: "public-key", clientExtensionResults: {},
      response: { clientDataJSON: "AQ", authenticatorData: "Ag", signature: "Aw", userHandle: "dTE" },
    });
    expect(credentialJSON(attestation as unknown as PublicKeyCredential).response).toEqual({ clientDataJSON: "AQ", attestationObject: "Bw", transports: ["usb"] });
  });
});

describe("signing in", () => {
  it("signs in with a passkey", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": anon,
      "POST /auth/passkey": assertionOptions, "POST /auth/session/passkey": { expires_at: "2030-01-01T00:00:00Z" },
    });
    const router = renderRoutes(routes, "/login?next=/explore");
    await userEvent.click(await screen.findByRole("button", { name: "Sign in with a passkey" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/explore"));
    const body = sent(fetch, "POST", "/auth/session/passkey")[0];
    expect(body.ceremony).toBe("cer1");
    expect(body.credential.response.userHandle).toBe("dTE");
    expect(credentials.get).toHaveBeenCalledOnce();
  });

  it("says so when the passkey request is cancelled", async () => {
    credentials.get.mockRejectedValueOnce(Object.assign(new Error("x"), { name: "NotAllowedError" }));
    fakeAPI({ "/instance": instance, "/user/profile": anon, "POST /auth/passkey": assertionOptions });
    renderRoutes(routes, "/login");
    await userEvent.click(await screen.findByRole("button", { name: "Sign in with a passkey" }));
    expect(await screen.findByText(/cancelled or timed out/)).toBeInTheDocument();
  });

  it("hides passkeys when the server has none", async () => {
    fakeAPI({ "/instance": { ...instance, passkeys: false }, "/user/profile": anon });
    renderRoutes(routes, "/login");
    expect(await screen.findByLabelText("Email")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sign in with a passkey" })).not.toBeInTheDocument();
  });

  it("uses a security key as the second factor", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": anon,
      "POST /auth/session": { two_factor_required: true, challenge: "ch1", methods: ["webauthn"] },
      "POST /auth/login/2fa/webauthn": assertionOptions, "POST /auth/session/2fa": { expires_at: "2030-01-01T00:00:00Z" },
    });
    const router = renderRoutes(routes, "/login?next=/explore");
    await userEvent.type(await screen.findByLabelText("Email"), "a@example.com");
    await userEvent.type(screen.getByLabelText("Password"), "pw");
    await userEvent.click(within(screen.getByLabelText("Password").closest("form")!).getByRole("button", { name: "Sign in" }));
    expect(await screen.findByLabelText("Recovery code")).toBeInTheDocument(); // keys only: the field is for recovery codes
    await userEvent.click(screen.getByRole("button", { name: "Use your security key" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/explore"));
    expect(sent(fetch, "POST", "/auth/login/2fa/webauthn")).toEqual([{ challenge: "ch1" }]);
    const body = sent(fetch, "POST", "/auth/session/2fa")[0];
    expect(body.challenge).toBe("ch1");
    expect(body.ceremony).toBe("cer1");
    expect(body.credential.id).toBe("BAU");
  });
});

describe("settings", () => {
  it("adds a passkey, shows the recovery codes, and removes a key", async () => {
    vi.stubGlobal("confirm", vi.fn(() => true));
    let keys: object[] = [];
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me(), "/user/tokens": { tokens: [] }, "/user/ssh-keys": { keys: [] },
      "/user/2fa": () => ({ enabled: keys.length > 0, totp: false, keys, recovery_codes_left: keys.length ? 10 : 0, totp_available: true, webauthn_available: true }),
      "POST /user/2fa/webauthn": creation,
      "POST /user/2fa/webauthn/finish": () => {
        keys = [{ id: "k1", name: "Phone", passkey: true, last_used_at: null, created_at: "2026-10-26T00:00:00Z" }];
        return { key: keys[0], recovery_codes: ["aaaaa-bbbbb"] };
      },
      "DELETE /user/2fa/webauthn/k1": () => ((keys = []), { message: "security key removed" }),
    });
    renderRoutes(routes, "/settings");
    await userEvent.type(await screen.findByLabelText("Name", { selector: "#key-name" }), "Phone");
    await userEvent.click(screen.getByRole("button", { name: "Add passkey" }));
    expect(await screen.findByRole("list", { name: "Recovery codes" })).toHaveTextContent("aaaaa-bbbbb");
    expect(sent(fetch, "POST", "/user/2fa/webauthn")).toEqual([{ passkey: true }]);
    const finish = sent(fetch, "POST", "/user/2fa/webauthn/finish")[0];
    expect(finish).toMatchObject({ ceremony: "cer2", name: "Phone", passkey: true });
    expect(finish.credential.response.attestationObject).toBe("Bw");
    const opts = credentials.create.mock.calls[0][0].publicKey;
    expect(new Uint8Array(opts.user.id)).toEqual(new Uint8Array([117, 49]));

    await userEvent.click(screen.getByRole("button", { name: "I've saved them" }));
    expect(await screen.findByText("passkey")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Remove Phone" }));
    await waitFor(() => expect(sent(fetch, "DELETE", "/user/2fa/webauthn/k1")).toHaveLength(1));
  });

  it("explains when the server has no public URL", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me(), "/user/tokens": { tokens: [] }, "/user/ssh-keys": { keys: [] },
      "/user/2fa": { enabled: false, totp: false, keys: [], recovery_codes_left: 0, totp_available: true, webauthn_available: false },
    });
    renderRoutes(routes, "/settings");
    expect(await screen.findByText(/http.public_url/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add security key" })).not.toBeInTheDocument();
  });
});

describe("sudo", () => {
  it("confirms with a security key", async () => {
    let confirmed = false;
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me({ two_factor_enabled: true }), "/user/ssh-keys": { keys: [] }, "/user/tokens": { tokens: [] },
      "/user/2fa": { enabled: true, totp: false, keys: [{ id: "k1", name: "Key", passkey: false, last_used_at: null, created_at: "2026-10-26T00:00:00Z" }], recovery_codes_left: 10, totp_available: true, webauthn_available: true },
      "POST /user/token": () => (confirmed ? { token: "ogit_x", id: "t1", name: "laptop" } : [403, { code: 403, details: "confirm", type: "SUDO_REQUIRED" }]),
      "POST /auth/sudo/webauthn": assertionOptions,
      "POST /auth/sudo": () => ((confirmed = true), { until: "2026-10-26T12:10:00Z" }),
    });
    renderRoutes(routes, "/settings");
    await userEvent.type(await screen.findByLabelText("Name", { selector: "#token-name" }), "laptop");
    await userEvent.click(screen.getByRole("button", { name: "Create token" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(await within(dialog).findByRole("button", { name: "Use security key" }));
    expect(await screen.findByDisplayValue("ogit_x")).toBeInTheDocument();
    expect(sent(fetch, "POST", "/auth/sudo")[0]).toMatchObject({ ceremony: "cer1" });
  });
});
