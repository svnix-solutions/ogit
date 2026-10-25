import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { fakeAPI, renderRoutes } from "@/test/render";

afterEach(() => vi.unstubAllGlobals());

const instance = { version: "dev", registration_enabled: true, ssh_enabled: false, max_asset_size: 1000 };
const me = (name: string) => ({ id: "u", username: name, email: `${name}@x`, avatar: "", role: "user" });
const repo = (role: string) => ({ id: "r1", namespace: "acme", slug: "app", full_path: "acme/app", name: "App", description: "",
  is_private: true, archived: false, owner_type: "group", role, has_issues: true });
const commit = { hash: "a".repeat(40), parents: [], message: "Init\n", author: { name: "A", email: "a@x", when: "2026-01-01T00:00:00Z" },
  committer: { name: "A", email: "a@x", when: "2026-01-01T00:00:00Z" } };
const refs = { default_branch: "main", branches: [{ name: "main", commit }, { name: "next", commit }], tags: [{ name: "v1.0.0", commit }] };
const asset = { id: "a1", name: "app.tar.gz", label: "", content_type: "application/gzip", size: 2048, download_count: 7, uploader: "dev",
  created_at: "2026-01-02T00:00:00Z", url: "/api/v1/repository/r1/releases/rel1/assets/a1/download",
  browser_download_url: "/acme/app/releases/download/v1.0.0/app.tar.gz" };
const release = (over: object = {}) => ({
  id: "rel1", tag_name: "v1.0.0", target_commitish: "main", name: "First", body: "Fixes #3", draft: false, prerelease: false, author: "dev",
  created_at: "2026-01-02T00:00:00Z", published_at: "2026-01-02T00:00:00Z", can_edit: true, assets: [asset], ...over,
});
const draft = release({ id: "rel2", tag_name: "v2.0.0", name: "", body: "", draft: true, published_at: null, assets: [] });
const pre = release({ id: "rel3", tag_name: "v1.1.0-rc1", name: "RC", prerelease: true, assets: [] });

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

const base = (role: string) => ({
  "/instance": instance, "/user/profile": me("dev"), "/repository/acme%2Fapp": repo(role),
  "/repository/acme%2Fapp/releases?limit=1": { releases: [], total: 3 },
  "/repository/acme%2Fapp/releases/latest": release(),
  "/repository/acme%2Fapp/refs": refs,
});

describe("releases", () => {
  it("lists releases with badges, assets and source code", async () => {
    fakeAPI({ ...base("developer"), "/repository/acme%2Fapp/releases?offset=0&limit=10": { releases: [draft, pre, release()], total: 3 } });
    renderRoutes(routes, "/acme/app/releases");
    expect(await screen.findByRole("link", { name: "First" })).toHaveAttribute("href", "/acme/app/releases/tag/v1.0.0");
    expect(screen.getByLabelText("3 releases")).toBeInTheDocument();
    expect(screen.getByText("Draft")).toBeInTheDocument();
    expect(screen.getByText("Pre-release")).toBeInTheDocument();
    expect(screen.getAllByText("Latest")).toHaveLength(1);
    expect(screen.getByRole("link", { name: "v2.0.0" })).toBeInTheDocument(); // an untitled draft shows its tag
    expect(screen.getByRole("link", { name: "app.tar.gz" })).toHaveAttribute("href", "/acme/app/releases/download/v1.0.0/app.tar.gz");
    expect(screen.getByText("2.0 KB")).toBeInTheDocument();
    expect(screen.getByTitle("Downloads")).toHaveTextContent("7");
    expect(screen.getAllByRole("link", { name: "Source code (zip)" }).map((a) => a.getAttribute("href"))).toContain("/acme/app/archive/v1.0.0.zip");
    expect(screen.getByRole("link", { name: "Draft a new release" })).toHaveAttribute("href", "/acme/app/releases/new");
    expect(screen.getAllByRole("link", { name: "#3" })[0]).toHaveAttribute("href", "/acme/app/issues/3");
  });

  it("hides writing from readers", async () => {
    fakeAPI({ ...base("reporter"), "/repository/acme%2Fapp/releases?offset=0&limit=10": { releases: [release({ can_edit: false })], total: 1 } });
    renderRoutes(routes, "/acme/app/releases");
    expect(await screen.findByRole("link", { name: "First" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Draft a new release" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete app.tar.gz" })).not.toBeInTheDocument();
  });

  it("points to the latest release from the code page", async () => {
    fakeAPI({ ...base("reporter"), "/repository/acme%2Fapp/tree?spec=": { empty: false, ref: "main", path: "", commit, entries: [{ name: "a.txt", path: "a.txt", type: "file", size: 1 }] } });
    renderRoutes(routes, "/acme/app");
    expect(await screen.findByText("Latest release")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "First" })).toHaveAttribute("href", "/acme/app/releases/tag/v1.0.0");
    expect(screen.getByText("· 1 file")).toBeInTheDocument();
  });

  it("drafts a release on a new tag, with generated notes", async () => {
    const fetch = fakeAPI({
      ...base("developer"),
      "POST /repository/acme%2Fapp/releases/generate-notes": { name: "v2.0.0", body: "## What's Changed\n\n* Add A by @dev in #2\n" },
      "POST /repository/acme%2Fapp/releases": draft,
      "/repository/acme%2Fapp/releases/by-tag?tag=v2.0.0": draft,
    });
    const router = renderRoutes(routes, "/acme/app/releases/new");
    const tag = await screen.findByLabelText("Tag");
    await userEvent.type(tag, "v1.0.0");
    expect(screen.getByText("An existing tag.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Create the tag from")).not.toBeInTheDocument();
    await userEvent.clear(tag);
    await userEvent.type(tag, "v2.0.0");
    expect(screen.getByText(/A new tag, created when the release is published/)).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Create the tag from"), "next");
    await userEvent.type(screen.getByLabelText("Release notes"), "Highlights");
    await userEvent.click(screen.getByRole("button", { name: "Generate notes" }));
    await waitFor(() => expect(screen.getByLabelText("Title")).toHaveValue("v2.0.0"));
    expect(sent(fetch, "POST", "/repository/acme%2Fapp/releases/generate-notes")).toEqual([{ tag_name: "v2.0.0", target_commitish: "next" }]);
    expect(screen.getByLabelText("Release notes")).toHaveValue("Highlights\n\n## What's Changed\n\n* Add A by @dev in #2\n");
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(screen.getByRole("heading", { name: "What's Changed" })).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText(/This is a pre-release/));

    await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/acme/app/releases/tag/v2.0.0"));
    expect(sent(fetch, "POST", "/repository/acme%2Fapp/releases")).toEqual([{
      tag_name: "v2.0.0", target_commitish: "next", name: "v2.0.0", body: "Highlights\n\n## What's Changed\n\n* Add A by @dev in #2\n", prerelease: true, draft: true,
    }]);
  });

  it("edits a published release without touching its tag", async () => {
    const fetch = fakeAPI({
      ...base("developer"), "/repository/acme%2Fapp/releases/rel1": release(),
      "PATCH /repository/acme%2Fapp/releases/rel1": release({ name: "First!" }), "/repository/acme%2Fapp/releases/by-tag?tag=v1.0.0": release(),
    });
    renderRoutes(routes, "/acme/app/releases/edit/rel1");
    expect(await screen.findByLabelText("Tag")).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Title"), "!");
    expect(screen.queryByRole("button", { name: "Save draft" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Update release" }));
    await waitFor(() => expect(sent(fetch, "PATCH", "/repository/acme%2Fapp/releases/rel1")).toEqual([{ name: "First!", body: "Fixes #3", prerelease: false }]));
  });
});

// FakeXHR records uploads and answers them when told.
class FakeXHR {
  static all: FakeXHR[] = [];
  method = "";
  url = "";
  headers: Record<string, string> = {};
  body: unknown;
  status = 0;
  statusText = "";
  responseText = "";
  upload: { onprogress?: (e: { lengthComputable: boolean; loaded: number; total: number }) => void } = {};
  onload?: () => void;
  onerror?: () => void;
  constructor() {
    FakeXHR.all.push(this);
  }
  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }
  setRequestHeader(k: string, v: string) {
    this.headers[k] = v;
  }
  send(body: unknown) {
    this.body = body;
  }
  answer(status: number, body: unknown) {
    this.status = status;
    this.responseText = JSON.stringify(body);
    this.onload?.();
  }
}

describe("release page", () => {
  it("uploads files with progress, refuses oversized ones, deletes assets and the release", async () => {
    FakeXHR.all = [];
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
    vi.stubGlobal("confirm", vi.fn(() => true));
    document.cookie = "ogit_csrf=tok";
    const fetch = fakeAPI({
      ...base("developer"), "/repository/acme%2Fapp/releases/by-tag?tag=v1.0.0": release(),
      "DELETE /repository/acme%2Fapp/releases/rel1/assets/a1": { message: "asset deleted" },
      "DELETE /repository/acme%2Fapp/releases/rel1": { message: "release deleted" },
      "/repository/acme%2Fapp/releases?offset=0&limit=10": { releases: [], total: 0 },
    });
    const router = renderRoutes(routes, "/acme/app/releases/tag/v1.0.0");
    expect(await screen.findByText(/up to 1000 B each/)).toBeInTheDocument();

    const files = [new File(["x".repeat(10)], "small.zip", { type: "application/zip" }), new File(["x".repeat(2000)], "big.bin")];
    await userEvent.upload(screen.getByLabelText("Attach files"), files);
    expect(screen.getByText("larger than the limit (1000 B)")).toBeInTheDocument();
    expect(FakeXHR.all).toHaveLength(1); // the big one never left
    const xhr = FakeXHR.all[0];
    expect(xhr.method).toBe("POST");
    expect(xhr.url).toBe("/api/v1/repository/acme%2Fapp/releases/rel1/assets?name=small.zip");
    expect(xhr.headers).toMatchObject({ "Content-Type": "application/zip", "X-CSRF-Token": "tok" });
    xhr.upload.onprogress?.({ lengthComputable: true, loaded: 5, total: 10 });
    expect(await screen.findByRole("progressbar", { name: "Uploading small.zip" })).toHaveAttribute("aria-valuenow", "50");
    xhr.answer(200, { ...asset, id: "a2", name: "small.zip" });
    await waitFor(() => expect(screen.queryByRole("progressbar")).not.toBeInTheDocument());

    await userEvent.upload(screen.getByLabelText("Attach files"), new File(["y"], "dup.zip"));
    FakeXHR.all[1].answer(422, { code: 422, details: "this release already has an asset with this name", type: "ASSET_EXISTS" });
    expect(await screen.findByText("this release already has an asset with this name")).toBeInTheDocument();

    await userEvent.click(within(screen.getByRole("list")).getByRole("button", { name: "Delete app.tar.gz" }));
    await waitFor(() => expect(sent(fetch, "DELETE", "/repository/acme%2Fapp/releases/rel1/assets/a1")).toHaveLength(1));
    expect(screen.getByRole("link", { name: /Edit/ })).toHaveAttribute("href", "/acme/app/releases/edit/rel1");
    await userEvent.click(screen.getByRole("button", { name: /Delete release/ }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/acme/app/releases"));
  });

  it("is a 404 for an unknown tag", async () => {
    fakeAPI({ ...base("reporter"), "/repository/acme%2Fapp/releases/by-tag?tag=nope": [404, { code: 404, details: "release not found", type: "RELEASE_NOT_FOUND" }] });
    renderRoutes(routes, "/acme/app/releases/tag/nope");
    expect(await screen.findByText(/not found/i)).toBeInTheDocument();
  });
});
