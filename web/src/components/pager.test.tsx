import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { fakeAPI, renderRoutes } from "@/test/render";
import { pageParam, Pager } from "./pager";

afterEach(() => vi.unstubAllGlobals());

describe("Pager", () => {
  it("shows the range and moves with a total", async () => {
    const onPage = vi.fn();
    render(<Pager page={1} perPage={30} count={30} total={75} onPage={onPage} />);
    expect(screen.getByText("31–60 of 75")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(onPage).toHaveBeenCalledWith(2);
    await userEvent.click(screen.getByRole("button", { name: "Previous" }));
    expect(onPage).toHaveBeenCalledWith(0);
  });

  it("stops at the end", () => {
    render(<Pager page={2} perPage={30} count={15} total={75} onPage={() => {}} />);
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
  });

  it("guesses from full pages without a total, and hides for one page", () => {
    const { rerender } = render(<Pager page={0} perPage={30} count={30} onPage={() => {}} />);
    expect(screen.getByRole("button", { name: "Next" })).toBeEnabled();
    rerender(<Pager page={0} perPage={30} count={12} onPage={() => {}} />);
    expect(screen.queryByRole("button", { name: "Next" })).not.toBeInTheDocument();
  });

  it("reads 1-based pages from the URL", () => {
    expect(pageParam(new URLSearchParams("page=3"))).toBe(2);
    expect(pageParam(new URLSearchParams(""))).toBe(0);
    expect(pageParam(new URLSearchParams("page=-4"))).toBe(0);
  });
});

describe("explore pages", () => {
  it("asks for the page in the URL", async () => {
    const repos = Array.from({ length: 30 }, (_, i) => ({ id: `r${i}`, namespace: "a", slug: `r${i}`, full_path: `a/r${i}`, name: `Repo ${i + 31}`, description: "", is_private: false, archived: false }));
    const fetch = fakeAPI({
      "/instance": { version: "dev", registration_enabled: true, ssh_enabled: false },
      "/user/profile": [401, { code: 401, details: "authentication required", type: "x" }],
      "/explore?limit=30&offset=30&q=": { repositories: repos, total: 65 },
    });
    renderRoutes(routes, "/explore?page=2");
    expect(await screen.findByText("31–60 of 65")).toBeInTheDocument();
    expect(fetch.mock.calls.some((c) => String(c[0]).includes("offset=30"))).toBe(true);
  });
});
