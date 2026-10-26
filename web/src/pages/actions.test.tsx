import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "@/app";
import { duration } from "@/lib/actions";
import { fakeAPI, renderRoutes } from "@/test/render";

afterEach(() => vi.unstubAllGlobals());

const instance = { version: "dev", registration_enabled: true, ssh_enabled: false };
const me = (name: string, role = "user") => ({ id: "u", username: name, email: `${name}@x`, avatar: "", role, two_factor_enabled: true });
const repo = (role: string) => ({ id: "r1", namespace: "acme", slug: "app", full_path: "acme/app", name: "App", description: "",
  is_private: true, archived: false, owner_type: "group", role, has_issues: true });
const tree = { empty: true, ref: "main", path: "", entries: [] };
const A = "/repository/acme%2Fapp/actions";

const deploy = {
  path: ".github/workflows/deploy.yml", name: "Deploy", dispatch: true,
  inputs: [
    { name: "env", description: "Where", required: true, default: "", type: "choice", options: ["staging", "prod"] },
    { name: "dry", description: "Dry run", required: false, default: "true", type: "boolean", options: [] },
  ],
};
const ci = { path: ".github/workflows/ci.yml", name: "CI", dispatch: false, inputs: [] };
const run = (over: object = {}) => ({
  id: "run1", number: 7, workflow_path: ci.path, workflow_name: "CI", event: "push", ref: "refs/heads/main", sha: "b".repeat(40),
  actor: "dev", status: "success", cancel_requested: false, from_fork: false, created_at: "2026-01-02T00:00:00Z",
  started_at: "2026-01-02T00:00:00Z", finished_at: "2026-01-02T00:01:05Z", ...over,
});

function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

const base = (role: string) => ({
  "/instance": instance, "/user/profile": me("dev"), "/repository/acme%2Fapp": repo(role),
  "/repository/acme%2Fapp/releases?limit=1": { releases: [], total: 0 },
  "/repository/acme%2Fapp/tree?spec=": tree,
  [`${A}/workflows`]: { uses_actions: true, workflows: [ci, deploy] },
});

describe("actions", () => {
  it("shows the tab only for repositories that use Actions", async () => {
    fakeAPI({ ...base("reporter"), [`${A}/workflows`]: { uses_actions: false, workflows: [] } });
    renderRoutes(routes, "/acme/app");
    expect(await screen.findByRole("link", { name: /Releases/ })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Actions/ })).not.toBeInTheDocument();
    vi.unstubAllGlobals();

    fakeAPI(base("reporter"));
    renderRoutes(routes, "/acme/app");
    expect(await screen.findByRole("link", { name: /Actions/ })).toHaveAttribute("href", "/acme/app/actions");
  });

  it("lists runs and filters them by workflow and status", async () => {
    const fetch = fakeAPI({
      ...base("reporter"),
      [`${A}/runs?page=1&per_page=25`]: { runs: [run(), run({ id: "run2", number: 8, status: "waiting", event: "pull_request", pull_number: 3, from_fork: true, ref: "refs/pull/3/merge" })], total: 2 },
      [`${A}/runs?page=1&per_page=25&workflow=.github%2Fworkflows%2Fdeploy.yml`]: { runs: [], total: 0 },
      [`${A}/runs?page=1&per_page=25&workflow=.github%2Fworkflows%2Fdeploy.yml&status=failure`]: { runs: [], total: 0 },
    });
    const router = renderRoutes(routes, "/acme/app/actions");
    expect(await screen.findByRole("link", { name: "CI #7" })).toHaveAttribute("href", "/acme/app/actions/runs/7");
    expect(screen.getByText(/pull request #3/)).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Waiting for approval" })).toBeInTheDocument();
    expect(screen.getByText("fork")).toBeInTheDocument();
    expect(screen.getAllByText("1m 5s").length).toBeGreaterThan(0);

    await userEvent.click(screen.getByRole("button", { name: "Deploy" }));
    await waitFor(() => expect(router.state.location.search).toBe("?workflow=.github%2Fworkflows%2Fdeploy.yml"));
    expect(await screen.findByRole("img", { name: "Deploy status" })).toHaveAttribute("src", "/acme/app/actions/workflows/deploy.yml/badge.svg");
    expect(screen.queryByRole("button", { name: /Run workflow/ })).not.toBeInTheDocument(); // readers don't run workflows
    await userEvent.selectOptions(screen.getByLabelText("Status"), "failure");
    await waitFor(() => expect(sent(fetch, "GET", `${A}/runs?page=1&per_page=25&workflow=.github%2Fworkflows%2Fdeploy.yml&status=failure`)).toHaveLength(1));
  });

  it("runs a workflow with its inputs", async () => {
    const fetch = fakeAPI({
      ...base("developer"),
      [`${A}/runs?page=1&per_page=25&workflow=.github%2Fworkflows%2Fdeploy.yml`]: { runs: [], total: 0 },
      [`POST ${A}/workflows/deploy.yml/dispatches`]: run({ number: 9, status: "queued", event: "workflow_dispatch", workflow_name: "Deploy" }),
      [`${A}/runs/9`]: { ...run({ number: 9, status: "queued", workflow_name: "Deploy" }), jobs: [] },
      [`${A}/runs/9/logs?from=1`]: { log: "", next: 1, done: false },
    });
    const router = renderRoutes(routes, "/acme/app/actions?workflow=.github%2Fworkflows%2Fdeploy.yml");
    await userEvent.click(await screen.findByRole("button", { name: /Run workflow/ }));
    // The required choice is left on the option it shows first.
    expect(screen.getByLabelText("Dry run")).toBeChecked(); // its default
    await userEvent.click(screen.getByLabelText("Dry run"));
    await userEvent.click(screen.getByRole("button", { name: "Run" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/acme/app/actions/runs/9"));
    expect(sent(fetch, "POST", `${A}/workflows/deploy.yml/dispatches`)).toEqual([{ ref: "", inputs: { env: "staging", dry: "false" } }]);
  });

  it("follows a running run's jobs and log, and cancels it", async () => {
    let logCalls = 0;
    const fetch = fakeAPI({
      ...base("developer"),
      [`${A}/runs/7`]: { ...run({ status: "running", finished_at: null }), jobs: [{ name: "build", status: "success", started_at: "2026-01-02T00:00:00Z", finished_at: "2026-01-02T00:00:30Z" }, { name: "test", status: "running", started_at: "2026-01-02T00:00:30Z", finished_at: null }] },
      [`${A}/runs/7/logs?from=1`]: () => (logCalls++, { log: "[build] hello\n", next: 2, done: false }),
      [`${A}/runs/7/logs?from=2`]: { log: "[test] world\n", next: 3, done: true },
      [`POST ${A}/runs/7/cancel`]: run({ status: "running", cancel_requested: true }),
    });
    renderRoutes(routes, "/acme/app/actions/runs/7");
    expect(await screen.findByRole("heading", { name: /CI #7/ })).toBeInTheDocument();
    expect(screen.getByText("build")).toBeInTheDocument();
    expect(screen.getAllByRole("img", { name: "In progress" })).toHaveLength(2); // the run and its running job
    const log = await screen.findByLabelText("Run log");
    await waitFor(() => expect(within(log).getByText("[build] hello")).toBeInTheDocument());
    await waitFor(() => expect(within(log).getByText("[test] world")).toBeInTheDocument(), { timeout: 4000 });
    expect(logCalls).toBe(1);
    await userEvent.click(screen.getByRole("button", { name: /Cancel/ }));
    await waitFor(() => expect(sent(fetch, "POST", `${A}/runs/7/cancel`)).toHaveLength(1));
  });

  it("approves a fork run and re-runs a finished one", async () => {
    const fetch = fakeAPI({
      ...base("maintainer"),
      [`${A}/runs/8`]: { ...run({ number: 8, status: "waiting", from_fork: true, started_at: null, finished_at: null }), jobs: [] },
      [`${A}/runs/8/logs?from=1`]: { log: "", next: 1, done: false },
      [`POST ${A}/runs/8/approve`]: run({ number: 8, status: "queued" }),
      [`${A}/runs/7`]: { ...run(), jobs: [] },
      [`${A}/runs/7/logs?from=1`]: { log: "", next: 1, done: true },
      [`POST ${A}/runs/7/rerun`]: run({ number: 10, status: "queued" }),
      [`${A}/runs/10`]: { ...run({ number: 10, status: "queued" }), jobs: [] },
      [`${A}/runs/10/logs?from=1`]: { log: "", next: 1, done: false },
    });
    const router = renderRoutes(routes, "/acme/app/actions/runs/8");
    expect(await screen.findByText(/first-time contributor's fork/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Approve and run" }));
    await waitFor(() => expect(sent(fetch, "POST", `${A}/runs/8/approve`)).toHaveLength(1));

    await router.navigate("/acme/app/actions/runs/7");
    expect(await screen.findByText("No output.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Re-run/ }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/acme/app/actions/runs/10"));
  });

  it("manages runners, secrets and variables in the repository settings", async () => {
    const fetch = fakeAPI({
      ...base("maintainer"),
      "/repository/r1/members": { members: [] },
      "/repository/r1/protection": { rules: [] },
      "/repository/r1/protected-tags": { rules: [] },
      "/repository/r1/labels": { labels: [] },
      "/repository/r1/ssh-keys": { keys: [] },
      "/repository/r1/tokens": { tokens: [] },
      "/repository/r1/hooks": { hooks: [] },
      "/repository/r1/actions/runners": { runners: [{ id: "x1", name: "builder", scope: "repository", labels: ["ubuntu-latest"], version: "1.0", online: true, last_seen_at: "2026-01-02T00:00:00Z" }] },
      "POST /repository/r1/actions/runners/registration-token": { token: "ogitreg_abc", expires_at: "2026-01-02T01:00:00Z" },
      "/repository/r1/actions/secrets": { secrets: [{ name: "DEPLOY_KEY", updated_at: "2026-01-02T00:00:00Z" }] },
      "PUT /repository/r1/actions/secrets/NPM_TOKEN": { name: "NPM_TOKEN" },
      "/repository/r1/actions/variables": { variables: [{ name: "REGION", value: "eu", updated_at: "2026-01-02T00:00:00Z" }] },
    });
    renderRoutes(routes, "/acme/app/settings");
    expect(await screen.findByText("builder")).toBeInTheDocument();
    expect(screen.getByLabelText("online")).toBeInTheDocument();
    expect(screen.getByText("DEPLOY_KEY")).toBeInTheDocument();
    expect(screen.getByText("eu")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "New registration token" }));
    expect(await screen.findByDisplayValue(/ogit runner register --url .* --token ogitreg_abc/)).toBeInTheDocument();

    const secrets = screen.getByRole("heading", { name: "Secrets" }).parentElement!.parentElement!;
    await userEvent.type(within(secrets).getByLabelText("Name"), "NPM_TOKEN");
    await userEvent.type(within(secrets).getByLabelText("Value"), "s3cret");
    await userEvent.click(within(secrets).getByRole("button", { name: "Save secret" }));
    await waitFor(() => expect(sent(fetch, "PUT", "/repository/r1/actions/secrets/NPM_TOKEN")).toEqual([{ value: "s3cret" }]));
    expect(within(secrets).queryByText("s3cret")).not.toBeInTheDocument();
  });

  it("formats durations", () => {
    expect(duration("2026-01-01T00:00:00Z", "2026-01-01T00:00:09Z")).toBe("9s");
    expect(duration("2026-01-01T00:00:00Z", "2026-01-01T01:02:00Z")).toBe("1h 2m");
    expect(duration(null, null)).toBe("");
  });
});
