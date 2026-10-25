import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { atLeast } from "@/lib/api";
import { fakeAPI, renderRoutes } from "@/test/render";
import { slugPreview } from "./new-repo";
import { cloneURLs } from "@/components/repo-layout";

afterEach(() => vi.unstubAllGlobals());

const alice = { id: "u1", username: "alice", email: "a@example.com", avatar: "", role: "user" };
const instance = { version: "dev", registration_enabled: true, ssh_enabled: true, ssh_port: 2222 };
const repo = (role: string) => ({
  id: "r1", namespace: "platform", slug: "infra", full_path: "platform/infra", name: "Infra", description: "Terraform",
  is_private: true, archived: false, owner_type: "group", role,
});

describe("helpers", () => {
  it("builds clone URLs", () => {
    const loc = { origin: "https://git.example.com", hostname: "git.example.com" };
    expect(cloneURLs("a/b", loc)).toEqual([{ label: "HTTPS", url: "https://git.example.com/a/b.git" }]);
    expect(cloneURLs("a/b", loc, { enabled: true, port: 2222 })[1].url).toBe("ssh://git@git.example.com:2222/a/b.git");
    expect(cloneURLs("a/b", loc, { enabled: true, port: 22 })[1].url).toBe("ssh://git@git.example.com/a/b.git");
  });
  it("previews slugs like the server", () => {
    expect(slugPreview("My Project")).toBe("my-project");
    expect(slugPreview("  Café / Déjà  ")).toBe("cafe-deja");
    expect(slugPreview("!!!")).toBe("");
  });
  it("compares roles", () => {
    expect(atLeast("owner", "maintainer")).toBe(true);
    expect(atLeast("developer", "maintainer")).toBe(false);
    expect(atLeast(undefined, "reporter")).toBe(false);
  });
});

describe("routing", () => {
  it("keeps fixed pages ahead of /:owner", async () => {
    fakeAPI({ "/instance": instance, "/user/profile": alice, "/explore?limit=50&q=": { repositories: [], total: 0 } });
    renderRoutes(routes, "/explore");
    expect(await screen.findByRole("heading", { name: "Explore public repositories" })).toBeInTheDocument();
  });

  it("shows a namespace page without signing in", async () => {
    fakeAPI({
      "/instance": instance,
      "/user/profile": [401, { code: 401, details: "authentication required", type: "x" }],
      "/namespace/alice?limit=30&offset=0": { name: "alice", type: "user", can_create: false, total: 1, repositories: [
        { id: "r", namespace: "alice", slug: "tools", full_path: "alice/tools", name: "Tools", description: "", is_private: false, archived: false },
      ] },
    });
    renderRoutes(routes, "/alice");
    expect(await screen.findByRole("link", { name: "Tools" })).toHaveAttribute("href", "/alice/tools");
    expect(screen.queryByRole("link", { name: /New repository/ })).not.toBeInTheDocument();
  });
});

const tree = {
  empty: false, ref: "main", path: "",
  commit: { hash: "a".repeat(40), parents: [], message: "Initial commit\n", author: { name: "Ann", email: "a@x", when: "2026-01-01T00:00:00Z" }, committer: { name: "Ann", email: "a@x", when: "2026-01-01T00:00:00Z" } },
  entries: [
    { name: "docs", path: "docs", type: "dir" },
    { name: "README.md", path: "README.md", type: "file", size: 20 },
  ],
  readme: { path: "README.md", size: 20, binary: false, truncated: false, content: "# Infra\n\nSee [docs](docs/guide.md).\n" },
};

describe("repository pages", () => {
  it("shows the tree, README and settings for maintainers, and clone URLs on demand", async () => {
    fakeAPI({ "/instance": instance, "/user/profile": alice, "/repository/platform%2Finfra": repo("maintainer"), "/repository/platform%2Finfra/tree?spec=": tree });
    renderRoutes(routes, "/platform/infra");
    expect(await screen.findByRole("heading", { name: "Infra" })).toBeInTheDocument();
    const docs = screen.getAllByRole("link", { name: "docs" }).map((a) => a.getAttribute("href"));
    expect(docs).toEqual(["/platform/infra/tree/main/docs", "/platform/infra/blob/main/docs/guide.md"]); // the folder, and the README's link
    expect(screen.getByRole("link", { name: /Settings/ })).toHaveAttribute("href", "/platform/infra/settings");

    await userEvent.click(screen.getByRole("button", { name: /Clone/ }));
    expect(screen.getByLabelText("HTTPS clone URL")).toHaveValue(`${window.location.origin}/platform/infra.git`);
    expect(screen.getByLabelText("SSH clone URL")).toHaveValue(`ssh://git@${window.location.hostname}:2222/platform/infra.git`);
  });

  it("hides settings from developers", async () => {
    fakeAPI({ "/instance": instance, "/user/profile": alice, "/repository/platform%2Finfra": repo("developer"), "/repository/platform%2Finfra/tree?spec=": tree });
    renderRoutes(routes, "/platform/infra");
    expect(await screen.findByRole("heading", { name: "Infra" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Settings/ })).not.toBeInTheDocument();
  });

  it("is browsable signed out, and suggests signing in when not found", async () => {
    const anon = [401, { code: 401, details: "authentication required", type: "x" }];
    fakeAPI({ "/instance": instance, "/user/profile": anon, "/repository/platform%2Finfra": repo("none"), "/repository/platform%2Finfra/tree?spec=": tree });
    renderRoutes(routes, "/platform/infra");
    expect(await screen.findByRole("heading", { name: "Infra" })).toBeInTheDocument();
    vi.unstubAllGlobals();

    fakeAPI({ "/instance": instance, "/user/profile": anon });
    renderRoutes(routes, "/platform/secret");
    expect(await screen.findByRole("link", { name: "sign in" })).toBeInTheDocument();
  });

  it("shows a file with line numbers and a raw link", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": alice, "/repository/platform%2Finfra": repo("developer"),
      "/repository/platform%2Finfra/blob?spec=feature%2Fx%2Fmain.go": { ref: "feature/x", commit: tree.commit, file: { path: "main.go", size: 26, binary: false, truncated: false, content: "package main\n\nfunc main() {}\n" } },
    });
    renderRoutes(routes, "/platform/infra/blob/feature/x/main.go");
    expect(await screen.findByRole("link", { name: "Raw" })).toHaveAttribute("href", "/platform/infra/raw/feature/x/main.go");
    expect(screen.getByRole("link", { name: "3" })).toHaveAttribute("href", "#L3");
  });
});

describe("new repository", () => {
  it("offers the user's namespace and groups they maintain, and opens the new repository", async () => {
    const fetch = fakeAPI({
      "/instance": instance,
      "/user/profile": alice,
      "/group": { groups: [
        { id: "g1", name: "platform", description: "", role: "maintainer" },
        { id: "g2", name: "readers", description: "", role: "member" },
      ] },
      "POST /repository": repo("owner"),
      "/repository/platform%2Finfra": repo("owner"),
    });
    renderRoutes(routes, "/new?namespace=platform");
    const owner = await screen.findByLabelText("Owner");
    await waitFor(() => expect(owner).toHaveValue("platform"));
    expect([...(owner as HTMLSelectElement).options].map((o) => o.value)).toEqual(["alice", "platform"]);

    await userEvent.type(screen.getByLabelText("Name"), "Infra");
    expect(screen.getByText("/platform/infra.git")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Create repository" }));
    expect(await screen.findByText("Terraform")).toBeInTheDocument();
    const post = fetch.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === "POST")!;
    expect(JSON.parse((post[1] as RequestInit).body as string)).toEqual({ namespace: "platform", name: "Infra", description: "", is_private: true });
  });
});

describe("admin", () => {
  it("is refused to non-admins", async () => {
    fakeAPI({ "/instance": instance, "/user/profile": alice });
    renderRoutes(routes, "/admin");
    expect(await screen.findByText("Site administrators only.")).toBeInTheDocument();
  });
});

describe("signing in", () => {
  it("returns to the full URL, query included", async () => {
    fakeAPI({ "/instance": instance, "/user/profile": [401, { code: 401, details: "authentication required", type: "x" }] });
    const router = renderRoutes(routes, "/new?namespace=platform");
    await screen.findByRole("heading", { name: "Sign in to oGit" });
    expect(router.state.location.search).toBe(`?next=${encodeURIComponent("/new?namespace=platform")}`);
  });
});
