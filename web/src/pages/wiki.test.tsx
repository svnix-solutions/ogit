import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { pageName, renderWiki, wikiLinks } from "@/lib/wiki";
import { fakeAPI, renderRoutes } from "@/test/render";

afterEach(() => vi.unstubAllGlobals());

const instance = { version: "dev", registration_enabled: true, ssh_enabled: false };
const me = { id: "u", username: "dev", email: "dev@x", avatar: "", role: "user" };
const repo = (role: string, over: object = {}) => ({ id: "r1", namespace: "acme", slug: "app", full_path: "acme/app", name: "App", description: "",
  is_private: true, archived: false, owner_type: "group", role, has_issues: true, wiki_access: "writers", ...over });
const commit = (hash: string, message: string) => ({ hash, parents: [], message, author: { name: "dev", email: "d@x", when: "2026-01-01T00:00:00Z" },
  committer: { name: "dev", email: "d@x", when: "2026-01-01T00:00:00Z" } });
const head = "h".repeat(40);
const pages = [
  { name: "Docs/Install", title: "Install", path: "Docs/Install.md" },
  { name: "Home", title: "Home", path: "Home.md" },
  { name: "_Sidebar", title: "_Sidebar", path: "_Sidebar.md" },
];
const info = (canEdit: boolean, over: object = {}) => ({ access: "writers", exists: true, can_edit: canEdit, head, pages, ...over });
const home = {
  page: { name: "Home", title: "Home", path: "Home.md", content: "# Welcome\n\nSee [[Install|Docs/Install]], [[Missing Page]] and ![logo](/uploads/ab/logo.png).", commit: commit(head, "Created Home") },
  sidebar: { name: "_Sidebar", title: "_Sidebar", path: "_Sidebar.md", content: "Side **links**" },
  head,
};

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

const base = (role: string, wiki: object, over: object = {}) => ({
  "/instance": instance, "/user/profile": me, "/repository/acme%2Fapp": repo(role, over), "/repository/acme%2Fapp/wiki": wiki,
});

describe("wiki rendering", () => {
  it("turns [[links]] into Markdown links, outside code", () => {
    expect(wikiLinks("[[Getting Started]] and [[the docs|Docs/Install]]")).toBe("[Getting Started](<Getting-Started>) and [the docs](<Docs/Install>)");
    expect(wikiLinks("`[[not a link]]`")).toBe("`[[not a link]]`");
    expect(pageName(" Guides / Getting Started.md ")).toBe("Guides/Getting-Started");
  });

  it("links pages, marks missing ones and serves files from the wiki", () => {
    const html = renderWiki("[a](Other) [b](../Home.md) [c](notes.pdf) ![i](img.png) [x](https://example.com)", {
      fullPath: "acme/app", ref: "HEAD", dir: "Docs", pages: new Set(["Home"]),
    });
    const tpl = document.createElement("template");
    tpl.innerHTML = html;
    const links = [...tpl.content.querySelectorAll("a")];
    expect(links[0].getAttribute("href")).toBe("/acme/app/wiki?new=Docs%2FOther");
    expect(links[0].dataset.missing).toBe("true");
    expect(links[1].getAttribute("href")).toBe("/acme/app/wiki/Home");
    expect(links[2].getAttribute("href")).toBe("/acme/app.wiki/raw/HEAD/Docs/notes.pdf");
    expect(links[3].getAttribute("rel")).toContain("noopener");
    expect(tpl.content.querySelector("img")!.getAttribute("src")).toBe("/acme/app.wiki/raw/HEAD/Docs/img.png");
  });

  it("drops scripts", () => {
    expect(renderWiki("<script>alert(1)</script><img src=x onerror=alert(1)>", { fullPath: "acme/app", ref: "HEAD", dir: "" })).not.toMatch(/script|onerror/);
  });
});

describe("wiki pages", () => {
  it("shows a page with its sidebar and the page list", async () => {
    fakeAPI({ ...base("reporter", info(false)), "/repository/acme%2Fapp/wiki/page?name=Home": home });
    renderRoutes(routes, "/acme/app/wiki");
    expect(await screen.findByRole("heading", { name: "Welcome" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Wiki/ })).toHaveAttribute("href", "/acme/app/wiki");
    const body = screen.getByRole("heading", { name: "Welcome" }).closest(".markdown") as HTMLElement;
    expect(within(body).getByRole("link", { name: "Install" })).toHaveAttribute("href", "/acme/app/wiki/Docs/Install");
    expect(within(body).getByRole("link", { name: "Missing Page" })).toHaveAttribute("href", "/acme/app/wiki?new=Missing-Page");
    expect(within(body).getByRole("img", { name: "logo" })).toHaveAttribute("src", "/acme/app.wiki/raw/HEAD/uploads/ab/logo.png");
    expect(screen.getByLabelText("Sidebar")).toHaveTextContent("Side links");
    const list = screen.getByRole("list", { name: "Pages" });
    expect(within(list).getAllByRole("link").map((a) => a.textContent)).toEqual(["Docs/Install", "Home"]);
    expect(screen.queryByRole("link", { name: /Edit/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /New page/ })).not.toBeInTheDocument();
    expect(screen.getByLabelText("Wiki clone URL")).toHaveValue(`${window.location.origin}/acme/app.wiki.git`);
  });

  it("invites writers to start an empty wiki", async () => {
    fakeAPI(base("developer", info(true, { exists: false, pages: [], head: undefined })));
    renderRoutes(routes, "/acme/app/wiki");
    expect(await screen.findByText("This wiki is empty")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Create the first page/ })).toHaveAttribute("href", "/acme/app/wiki?new=Home");
  });

  it("hides the tab when the wiki is off", async () => {
    fakeAPI({ ...base("reporter", info(false), { wiki_access: "off" }), "/repository/acme%2Fapp/tree?spec=": { empty: true, entries: [] },
      "/repository/acme%2Fapp/releases?limit=1": { releases: [], total: 0 } });
    renderRoutes(routes, "/acme/app");
    expect(await screen.findByRole("link", { name: /Code/ })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Wiki/ })).not.toBeInTheDocument();
  });

  it("creates a page", async () => {
    const fetch = fakeAPI({
      ...base("developer", info(true)),
      "POST /repository/acme%2Fapp/wiki/pages": { commit: head, name: "Getting-Started" },
      "/repository/acme%2Fapp/wiki/page?name=Getting-Started": { ...home, page: { ...home.page, name: "Getting-Started", title: "Getting Started", content: "Hi" } },
    });
    const router = renderRoutes(routes, "/acme/app/wiki?new=");
    await userEvent.type(await screen.findByLabelText("Title"), "Getting Started");
    await userEvent.type(screen.getByLabelText("Content"), "Hi");
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(screen.getByText("Hi")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Create page" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/acme/app/wiki/Getting-Started"));
    expect(sent(fetch, "POST", "/repository/acme%2Fapp/wiki/pages")).toEqual([{ name: "Getting Started", content: "Hi", message: "" }]);
  });

  it("edits from the head it read, renames, and explains conflicts", async () => {
    const fetch = fakeAPI({
      ...base("developer", info(true)),
      "/repository/acme%2Fapp/wiki/page?name=Home": home,
      "PUT /repository/acme%2Fapp/wiki/page": [409, { code: 409, details: "changed", type: "WIKI_CONFLICT" }],
    });
    renderRoutes(routes, "/acme/app/wiki/Home?edit");
    const title = await screen.findByLabelText("Title");
    await userEvent.clear(title);
    await userEvent.type(title, "Start Here");
    await userEvent.click(screen.getByRole("button", { name: "Save page" }));
    expect(await screen.findByText(/Someone changed this page while you were editing it/)).toBeInTheDocument();
    expect(sent(fetch, "PUT", "/repository/acme%2Fapp/wiki/page")).toEqual([
      { name: "Home", new_name: "Start Here", content: home.page.content, message: "", base: head },
    ]);
  });

  it("deletes a page", async () => {
    const fetch = fakeAPI({
      ...base("developer", info(true)),
      "/repository/acme%2Fapp/wiki/page?name=Home": home,
      [`DELETE /repository/acme%2Fapp/wiki/page?name=Home&base=${head}`]: { commit: head, name: "" },
    });
    const router = renderRoutes(routes, "/acme/app/wiki/Home?edit");
    await userEvent.click(await screen.findByRole("button", { name: /Delete page/ }));
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/acme/app/wiki"));
    expect(fetch.mock.calls.some(([url, init]) => String(url).includes("/wiki/page?name=Home") && init?.method === "DELETE")).toBe(true);
  });

  it("shows history, an old version, and reverts to it", async () => {
    const old = "o".repeat(40);
    const fetch = fakeAPI({
      ...base("developer", info(true)),
      "/repository/acme%2Fapp.wiki/commits?spec=HEAD%2FHome.md&offset=0&limit=30": { ref: "HEAD", path: "Home.md", commits: [commit(head, "Updated Home"), commit(old, "Created Home")], more: false },
      [`/repository/acme%2Fapp/wiki/page?name=Home&version=${old}`]: { ...home, page: { ...home.page, content: "Old text" } },
      "POST /repository/acme%2Fapp/wiki/revert": { commit: "n".repeat(40), name: "Home" },
      "/repository/acme%2Fapp/wiki/page?name=Home": home,
    });
    const router = renderRoutes(routes, "/acme/app/wiki/Home?history");
    expect(await screen.findByText("Updated Home")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: `View at ${old.slice(0, 7)}` }));
    expect(await screen.findByText("Old text")).toBeInTheDocument();
    expect(screen.getByText(/as it was at/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Revert to this version/ }));
    await waitFor(() => expect(router.state.location.search).toBe(""));
    expect(sent(fetch, "POST", "/repository/acme%2Fapp/wiki/revert")).toEqual([{ name: "Home", version: old, base: head }]);
  });

  it("shows a revision's changes, linking pages", async () => {
    fakeAPI({
      ...base("reporter", info(false)),
      [`/repository/acme%2Fapp.wiki/commits/${head}`]: {
        commit: commit(head, "Updated Home"),
        diff: { additions: 1, deletions: 0, truncated: false, files: [{ path: "Home.md", status: "modified", binary: false, additions: 1, deletions: 0, truncated: false,
          hunks: [{ old_start: 1, old_lines: 0, new_start: 1, new_lines: 1, lines: [{ kind: "+", text: "new line", new: 1 }] }] }] },
      },
    });
    renderRoutes(routes, `/acme/app/wiki/Home?diff=${head}`);
    expect(await screen.findByRole("link", { name: "Home.md" })).toHaveAttribute("href", `/acme/app/wiki/Home?version=${head}`);
    expect(screen.getByText("new line")).toBeInTheDocument();
  });

  it("searches", async () => {
    fakeAPI({
      ...base("reporter", info(false)),
      "/repository/acme%2Fapp/wiki/page?name=Home": home,
      "/repository/acme%2Fapp/wiki/search?q=install": { results: [{ name: "Docs/Install", title: "Install", path: "Docs/Install.md", snippet: "…how to install…" }] },
    });
    const router = renderRoutes(routes, "/acme/app/wiki");
    await userEvent.type(await screen.findByLabelText("Search the wiki"), "install{Enter}");
    await waitFor(() => expect(router.state.location.search).toBe("?q=install"));
    expect(await screen.findByText("…how to install…")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Install" })).toHaveAttribute("href", "/acme/app/wiki/Docs/Install");
  });

  it("lets maintainers choose who edits the wiki", async () => {
    const fetch = fakeAPI({ ...base("maintainer", info(true)), "PATCH /repository/r1": repo("maintainer", { wiki_access: "readers" }) });
    renderRoutes(routes, "/acme/app/settings");
    await userEvent.selectOptions(await screen.findByLabelText("Wiki"), "readers");
    await userEvent.click(screen.getAllByRole("button", { name: "Save" })[0]);
    await waitFor(() => expect(sent(fetch, "PATCH", "/repository/r1")).toEqual([{ wiki_access: "readers" }]));
  });
});
