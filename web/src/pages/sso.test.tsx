import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { fakeAPI, renderRoutes } from "@/test/render";

afterEach(() => vi.unstubAllGlobals());

const instance = { version: "dev", registration_enabled: true, ssh_enabled: false };
const me = (over: object = {}) => ({ id: "u", username: "dana", email: "d@x", avatar: "", role: "user", has_password: true, ...over });
const providers = { providers: [{ slug: "corp", name: "Corp SSO", kind: "oidc" }] };

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

describe("single sign-on", () => {
  it("offers the providers on the sign-in page, with errors from them", async () => {
    fakeAPI({ "/instance": instance, "/auth/providers": providers, "/user/profile": [401, { code: 401, details: "no", type: "UNAUTHORIZED" }] });
    renderRoutes(routes, "/login?next=%2Fexplore&sso_error=this+account+isn%27t+allowed+to+sign+in+here");
    const button = await screen.findByRole("link", { name: "Sign in with Corp SSO" });
    expect(button).toHaveAttribute("href", "/api/v1/auth/sso/corp/start?redirect=%2Fexplore");
    expect(screen.getByText("this account isn't allowed to sign in here")).toBeInTheDocument();
  });

  it("asks for the 2FA code when the provider requires it", async () => {
    fakeAPI({ "/instance": instance, "/auth/providers": providers, "/user/profile": [401, { code: 401, details: "no", type: "UNAUTHORIZED" }] });
    renderRoutes(routes, "/login?challenge=c1&methods=totp&redirect=%2Fx");
    expect(await screen.findByRole("heading", { name: "Two-factor authentication" })).toBeInTheDocument();
    expect(screen.getByLabelText("Authentication code")).toBeInTheDocument();
  });

  it("links a provider to an existing account with its password and code", async () => {
    let calls = 0;
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me(),
      "/auth/sso/link?token=tok": { provider: "Corp SSO", email: "d@x", username: "dana" },
      "POST /auth/sso/link": () =>
        ++calls === 1 ? [401, { code: 401, details: "enter your two-factor code too", type: "TWO_FACTOR_CODE_REQUIRED" }] : { redirect: "/explore", expires_at: "2030-01-01T00:00:00Z" },
      "/explore": { repositories: [] },
    });
    const router = renderRoutes(routes, "/login/link#token=tok");
    expect(await screen.findByText(/already has an account here/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Password"), "pw");
    await userEvent.click(screen.getByRole("button", { name: "Link Corp SSO and sign in" }));
    await userEvent.type(await screen.findByLabelText("Authentication code"), "123456");
    await userEvent.click(screen.getByRole("button", { name: "Link Corp SSO and sign in" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/explore"));
    expect(sent(fetch, "POST", "/auth/sso/link")).toEqual([{ token: "tok", password: "pw" }, { token: "tok", password: "pw", code: "123456" }]);
  });

  it("lists linked providers; passwordless accounts set a password and confirm sudo through a provider", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me({ has_password: false }), "/auth/providers": { providers: [...providers.providers, { slug: "gh", name: "GitHub", kind: "github" }] },
      "/user/identities": { identities: [{ id: "i1", provider: "corp", provider_name: "Corp SSO", email: "d@x", username: "dana", last_login_at: null, created_at: "2026-01-01T00:00:00Z" }] },
      "DELETE /user/identities/i1": [403, { code: 403, details: "confirm it's you first", type: "SUDO_REQUIRED" }],
      "POST /user/password": { message: "password set" },
      "/user/ssh-keys": { keys: [] }, "/user/tokens": { tokens: [] }, "/user/2fa": { enabled: false, recovery_codes_left: 0, totp_available: true },
    });
    vi.stubGlobal("confirm", () => true);
    renderRoutes(routes, "/settings");
    const section = await waitFor(() => {
      const el = document.getElementById("sign-in");
      if (!el) throw new Error("not yet");
      return el;
    });
    expect(await within(section).findByText("Corp SSO")).toBeInTheDocument();
    expect(within(section).getByRole("link", { name: "Link GitHub" })).toHaveAttribute("href", "/api/v1/auth/sso/gh/start?redirect=%2Fsettings&purpose=link");
    expect(within(section).queryByRole("link", { name: "Link Corp SSO" })).not.toBeInTheDocument();

    expect(screen.getByRole("heading", { name: "Set a password" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Current password")).not.toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("New password"), "Password-123!");
    await userEvent.click(screen.getByRole("button", { name: "Set password" }));
    await waitFor(() => expect(sent(fetch, "POST", "/user/password")).toEqual([{ current_password: "", new_password: "Password-123!" }]));

    await userEvent.click(within(section).getByRole("button", { name: "Unlink" }));
    const dialog = await screen.findByRole("dialog", { name: "Confirm it's you" });
    expect(await within(dialog).findByRole("button", { name: "Confirm with Corp SSO" })).toBeInTheDocument();
    expect(within(dialog).queryByLabelText("Password")).not.toBeInTheDocument();
  });

  it("lets admins add a provider", async () => {
    let list: object[] = [];
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me({ role: "admin", has_password: false, two_factor_satisfied: true }),
      "/admin/settings": { require_two_factor: false, require_two_factor_forced: false },
      "/admin/users?q=&limit=20&offset=0": { users: [], total: 0 }, "/admin/repositories?q=&limit=20&offset=0": { repositories: [], total: 0 },
      "/admin/runners": { runners: [] }, "/admin/actions/secrets": { secrets: [] }, "/admin/actions/variables": { variables: [] },
      "/admin/sso/providers": () => ({ providers: list }),
      "POST /admin/sso/providers": (init: RequestInit) => {
        const p = JSON.parse(init.body as string);
        list = [{ ...p, has_secret: true, from_config: false, callback_url: "https://ogit.example/api/v1/auth/sso/kc/callback", client_secret: undefined }];
        return list[0];
      },
    });
    renderRoutes(routes, "/admin");
    await userEvent.click(await screen.findByRole("button", { name: "Add a provider" }));
    const section = document.getElementById("sign-in-providers")!;
    await userEvent.type(within(section).getByLabelText("Slug (in URLs)"), "kc");
    await userEvent.type(within(section).getByLabelText("Name (on the sign-in button)"), "Keycloak");
    await userEvent.type(within(section).getByLabelText("Issuer URL"), "https://sso.example/realms/main");
    await userEvent.type(within(section).getByLabelText("Client ID"), "ogit");
    await userEvent.type(within(section).getByLabelText("Client secret"), "s3cret");
    await userEvent.click(within(section).getByRole("button", { name: "Add provider" }));
    await waitFor(() =>
      expect(sent(fetch, "POST", "/admin/sso/providers")).toEqual([
        { slug: "kc", name: "Keycloak", kind: "oidc", issuer: "https://sso.example/realms/main", client_id: "ogit", scopes: "", allowed_domains: "", allowed_orgs: "",
          auto_create: true, require_two_factor: false, enabled: true, groups_claim: "", admin_group: "", group_mappings: [], client_secret: "s3cret" },
      ]),
    );
    expect(await within(section).findByDisplayValue("https://ogit.example/api/v1/auth/sso/kc/callback")).toBeInTheDocument();
  });

  it("hides the password form when only providers sign in, but keeps it for break-glass accounts", async () => {
    fakeAPI({
      "/instance": { ...instance, registration_enabled: false, sso_only: true }, "/auth/providers": providers,
      "/user/profile": [401, { code: 401, details: "no", type: "UNAUTHORIZED" }],
    });
    renderRoutes(routes, "/login");
    expect(await screen.findByRole("link", { name: "Sign in with Corp SSO" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Email")).not.toBeInTheDocument();
    expect(screen.queryByText("Create an account")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Sign in with a local account" }));
    expect(screen.getByLabelText("Email")).toBeInTheDocument();
  });

  it("maps provider groups and turns on sign-in only through providers", async () => {
    let list: object[] = [];
    const settings = { require_two_factor: false, require_two_factor_forced: false, sso_only: false, sso_only_forced: false, sso_only_exempt: ["root"] };
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me({ role: "admin", two_factor_enabled: true }),
      "/admin/settings": settings, "PATCH /admin/settings": { ...settings, sso_only: true },
      "/admin/users?q=&limit=20&offset=0": { users: [], total: 0 }, "/admin/repositories?q=&limit=20&offset=0": { repositories: [], total: 0 },
      "/admin/runners": { runners: [] }, "/admin/actions/secrets": { secrets: [] }, "/admin/actions/variables": { variables: [] },
      "/admin/sso/providers": () => ({ providers: list }),
      "POST /admin/sso/providers": (init: RequestInit) => {
        list = [{ ...JSON.parse(init.body as string), has_secret: true, from_config: false, callback_url: "" }];
        return list[0];
      },
    });
    vi.stubGlobal("confirm", () => true);
    renderRoutes(routes, "/admin");
    expect(await screen.findByText(/Break-glass accounts that keep their password: root/)).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText("Sign in only through sign-in providers"));
    await waitFor(() => expect(sent(fetch, "PATCH", "/admin/settings")).toEqual([{ sso_only: true }]));

    await userEvent.click(screen.getByRole("button", { name: "Add a provider" }));
    const section = document.getElementById("sign-in-providers")!;
    await userEvent.type(within(section).getByLabelText("Slug (in URLs)"), "kc");
    await userEvent.type(within(section).getByLabelText("Name (on the sign-in button)"), "Keycloak");
    await userEvent.type(within(section).getByLabelText("Issuer URL"), "https://sso.example/realms/main");
    await userEvent.type(within(section).getByLabelText("Client ID"), "ogit");
    await userEvent.type(within(section).getByLabelText("Client secret"), "s3cret");
    await userEvent.type(within(section).getByLabelText("Groups claim"), "realm_access.roles");
    await userEvent.type(within(section).getByLabelText("Admin group"), "ogit-admins");
    await userEvent.click(within(section).getByRole("button", { name: "Map a group" }));
    const row = within(section).getByTestId("group-mapping");
    await userEvent.type(within(row).getByLabelText("Provider group"), "eng");
    await userEvent.type(within(row).getByLabelText("oGit group"), "engineering");
    await userEvent.selectOptions(within(row).getByLabelText("Role"), "maintainer");
    await userEvent.click(within(section).getByRole("button", { name: "Add provider" }));
    await waitFor(() => expect(sent(fetch, "POST", "/admin/sso/providers")).toHaveLength(1));
    const body = sent(fetch, "POST", "/admin/sso/providers")[0];
    expect(body).toMatchObject({ groups_claim: "realm_access.roles", admin_group: "ogit-admins", group_mappings: [{ group: "eng", target: "engineering", role: "maintainer" }] });
  });
});
