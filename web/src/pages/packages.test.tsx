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

  it("lists a namespace's packages", async () => {
    fakeAPI({
      "/instance": instance, "/user/profile": me,
      "/namespace/alice?limit=30&offset=0": { name: "alice", type: "user", description: "", can_create: true, repositories: [], total: 0 },
      "/packages/alice": { packages: [pkg()] },
    });
    renderRoutes(routes, "/alice");
    expect(await screen.findByRole("link", { name: "tools/app" })).toHaveAttribute("href", "/packages/alice/container/tools/app");
  });
});
