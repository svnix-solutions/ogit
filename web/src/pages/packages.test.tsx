import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { fakeAPI, renderRoutes } from "@/test/render";

afterEach(() => vi.unstubAllGlobals());

const instance = { version: "dev", registration_enabled: true, ssh_enabled: false };
const me = { id: "u", username: "alice", email: "alice@x", avatar: "", role: "user" };
const pkg = (over: object = {}) => ({
  id: "p1", namespace: "alice", type: "container", name: "tools/app", visibility: "repository", public: false, repository: "alice/app",
  can_write: true, can_admin: true, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-02T00:00:00Z", ...over,
});
const image = { version: "sha256:" + "a".repeat(64), size: 3 * 1024 * 1024, metadata: {}, publisher: "alice", files: [], tags: ["v1", "latest"], created_at: "2026-01-02T00:00:00Z" };
const generic = {
  version: "1.0.0", size: 0, metadata: {}, publisher: "ci", tags: [], created_at: "2026-01-02T00:00:00Z",
  files: [{ name: "tool.tar.gz", size: 2048, sha256: "b".repeat(64), created_at: "2026-01-02T00:00:00Z" }],
};

describe("packages", () => {
  it("shows an image with its tags, install command and settings", async () => {
    const fetch = fakeAPI({
      "/instance": instance, "/user/profile": me,
      "/packages/alice/container/tools%2Fapp": { package: pkg(), versions: [image] },
      "PATCH /packages/alice/container/tools%2Fapp": pkg({ visibility: "public", public: true }),
    });
    renderRoutes(routes, "/packages/alice/container/tools/app");
    expect(await screen.findByRole("heading", { name: /tools\/app/ })).toBeInTheDocument();
    expect(screen.getByText("v1")).toBeInTheDocument();
    expect(screen.getByText("by alice · ", { exact: false })).toBeInTheDocument();
    expect(screen.getByLabelText("Install command")).toHaveValue(`docker pull ${location.host}/alice/tools/app:v1`);
    expect(screen.getByRole("link", { name: "alice/app" })).toHaveAttribute("href", "/alice/app");

    await userEvent.selectOptions(screen.getByLabelText("Visibility"), "public");
    await waitFor(() =>
      expect(fetch.mock.calls.some(([url, init]) => url === "/api/v1/packages/alice/container/tools%2Fapp" && init?.method === "PATCH" && init.body === '{"visibility":"public"}')).toBe(true),
    );
  });

  it("links generic files and hides management from readers", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me,
      "/packages/alice/generic/tool": { package: pkg({ type: "generic", name: "tool", can_write: false, can_admin: false, public: true }), versions: [generic] },
    });
    renderRoutes(routes, "/packages/alice/generic/tool");
    expect(await screen.findByRole("link", { name: "tool.tar.gz" })).toHaveAttribute("href", "/api/packages/alice/generic/tool/1.0.0/tool.tar.gz");
    expect(screen.queryByLabelText("Visibility")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Delete version/ })).not.toBeInTheDocument();
    expect(screen.queryByText("Delete this package")).not.toBeInTheDocument();
  });

  it("lists a namespace's packages by type", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me,
      "/namespace/alice?limit=30&offset=0": { name: "alice", type: "user", description: "", can_create: true, repositories: [], total: 0 },
      "/packages/alice": { packages: [pkg(), pkg({ id: "p2", type: "npm", name: "util" })] },
    });
    renderRoutes(routes, "/alice?tab=packages");
    expect(await screen.findByRole("link", { name: "tools/app" })).toHaveAttribute("href", "/packages/alice/container/tools/app");
    expect(screen.getByRole("link", { name: "util" })).toHaveAttribute("href", "/packages/alice/npm/util");
    await userEvent.selectOptions(screen.getByLabelText("Package type"), "npm");
    await waitFor(() => expect(screen.queryByRole("link", { name: "tools/app" })).not.toBeInTheDocument());
    expect(screen.getByRole("link", { name: "util" })).toBeInTheDocument();
  });

  it("shows an npm package's setup, README, dependencies and deprecations", async () => {
    const v = (version: string, over: object = {}) => ({
      version, size: 100, publisher: "alice", files: [{ name: `util-${version}.tgz`, size: 100, sha256: "c".repeat(64), created_at: "2026-01-02T00:00:00Z" }],
      tags: [], created_at: "2026-01-02T00:00:00Z", metadata: { manifest: { name: "@alice/util", version, dependencies: { "left-pad": "^1.3.0" } } }, ...over,
    });
    fakeAPI({
      "/instance": instance, "/user/profile": me,
      "/packages/alice/npm/util": {
        package: pkg({ type: "npm", name: "util" }),
        readme: "# Util\n\nSmall *helpers*.",
        versions: [v("2.0.0-beta.1", { tags: ["next"] }), v("1.0.0", { tags: ["latest"], metadata: { manifest: { name: "@alice/util", version: "1.0.0", deprecated: "use 2", peerDependencies: { react: ">=18" } } } })],
      },
    });
    renderRoutes(routes, "/packages/alice/npm/util");
    expect(await screen.findByRole("heading", { name: "Util" })).toBeInTheDocument();
    expect(screen.getByLabelText("Install command")).toHaveValue("npm install @alice/util@1.0.0");
    const npmrc = screen.getAllByLabelText("npmrc line").map((e) => (e as HTMLInputElement).value);
    expect(npmrc).toEqual([`@alice:registry=${location.origin}/api/packages/npm/`, `//${location.host}/api/packages/npm/:_authToken=<token>`]);
    expect(screen.getByText("Dependencies of 1.0.0")).toBeInTheDocument();
    expect(screen.getByText("react")).toBeInTheDocument();
    expect(screen.getByText("deprecated")).toHaveAttribute("title", "use 2");
    expect(screen.getByText("next")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "util-1.0.0.tgz" })).toHaveAttribute("href", "/api/packages/npm/@alice/util/-/util-1.0.0.tgz");
  });

  it("shows a repository's Go module", async () => {
    const repo = { id: "r1", namespace: "alice", slug: "lib", full_path: "alice/lib", name: "lib", description: "", is_private: true, archived: false, owner_type: "user", role: "owner", has_issues: true };
    fakeAPI({
      "/instance": instance, "/user/profile": me, "/repository/alice%2Flib": repo,
      "/repository/alice%2Flib/packages": { packages: [] },
      "/repository/alice%2Flib/go-module?summary=true": { path: "ogit.test/alice/lib", versions: [], requires: [] },
      "/repository/alice%2Flib/go-module": {
        path: "ogit.test/alice/lib",
        versions: [{ version: "v1.1.0-rc.1", time: "2026-01-03T00:00:00Z" }, { version: "v1.0.0", time: "2026-01-02T00:00:00Z" }],
        requires: [{ path: "golang.org/x/mod", version: "v0.34.0", indirect: false }, { path: "golang.org/x/sync", version: "v0.20.0", indirect: true }],
      },
    });
    renderRoutes(routes, "/alice/lib/packages");
    expect(await screen.findByLabelText("Go get command")).toHaveValue("go get ogit.test/alice/lib@v1.0.0");
    expect(screen.getByRole("link", { name: /Packages/ })).toHaveAttribute("href", "/alice/lib/packages");
    expect(screen.getByText("golang.org/x/mod")).toBeInTheDocument();
    expect(screen.queryByText("golang.org/x/sync")).not.toBeInTheDocument();
    expect(screen.getByText("go env -w GOPRIVATE=ogit.test")).toBeInTheDocument();
  });
});
