import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Repository } from "@/lib/api";
import { fakeAPI } from "@/test/render";
import { deliveryOutcome, eventsSummary, Webhooks, type Delivery } from "./repo-webhooks";

afterEach(() => vi.unstubAllGlobals());

const repo = { id: "r1", namespace: "alice", slug: "site", full_path: "alice/site", name: "Site", role: "owner" } as Repository;
const hook = { id: "h1", url: "https://ci.example.com/hook", events: ["push"], active: true, has_secret: true, created_at: "", updated_at: "" };
const delivery = (over: Partial<Delivery>): Delivery => ({
  id: "d1", event: "push", status: "delivered", attempts: 1, response_status: 200, duration_ms: 12, created_at: "2026-10-25T10:00:00Z", ...over,
});

function show() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <Webhooks owner={`/repository/${repo.id}`} />
    </QueryClientProvider>,
  );
}

// sent returns the JSON bodies of the requests made with method to path.
function sent(fetch: ReturnType<typeof fakeAPI>, method: string, path: string) {
  return fetch.mock.calls
    .filter(([url, init]) => url === "/api/v1" + path && (init?.method ?? "GET") === method)
    .map(([, init]) => (init?.body ? JSON.parse(init.body as string) : undefined));
}

describe("helpers", () => {
  it("summarises events", () => {
    expect(eventsSummary(["*"])).toBe("all events");
    expect(eventsSummary(["push", "pull_request"])).toBe("push, pull_request");
  });
  it("describes outcomes", () => {
    expect(deliveryOutcome(delivery({}))).toBe("HTTP 200 in 12 ms");
    expect(deliveryOutcome(delivery({ status: "failed", attempts: 5, response_status: 500 }))).toBe("HTTP 500 after 5 attempts");
    expect(deliveryOutcome(delivery({ status: "pending", attempts: 0, response_status: 0 }))).toBe("queued");
    expect(deliveryOutcome(delivery({ status: "pending", attempts: 1, response_status: 0, error: "blocked", next_attempt_at: undefined }))).toBe("no answer, retrying");
  });
});

describe("webhooks section", () => {
  it("adds a webhook with chosen events", async () => {
    const fetch = fakeAPI({ "/repository/r1/hooks": { webhooks: [] }, "POST /repository/r1/hooks": hook });
    show();
    await userEvent.click(await screen.findByRole("button", { name: "Add webhook" }));
    await userEvent.type(screen.getByLabelText("Payload URL"), "https://ci.example.com/hook");
    await userEvent.type(screen.getByLabelText("Secret"), "s3cret");
    await userEvent.click(screen.getByLabelText("Let me choose"));
    await userEvent.click(screen.getByLabelText("Pull requests"));
    await userEvent.click(screen.getByLabelText("Pushes")); // off
    await userEvent.click(screen.getByRole("button", { name: "Add webhook" }));
    await waitFor(() => expect(sent(fetch, "POST", "/repository/r1/hooks")).toHaveLength(1));
    expect(sent(fetch, "POST", "/repository/r1/hooks")[0]).toEqual({
      url: "https://ci.example.com/hook", secret: "s3cret", events: ["pull_request"], active: true,
    });
  });

  it("keeps the secret when editing unless it is changed or removed", async () => {
    const fetch = fakeAPI({
      "/repository/r1/hooks": { webhooks: [hook] },
      "/repository/r1/hooks/h1/deliveries": { deliveries: [] },
      "PATCH /repository/r1/hooks/h1": hook,
    });
    show();
    await userEvent.click(await screen.findByRole("button", { name: /ci\.example\.com/ }));
    expect(await screen.findByText("None yet.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    expect(screen.getByLabelText("Secret")).toHaveAttribute("placeholder", "Unchanged");
    await userEvent.click(screen.getByLabelText("Everything"));
    await userEvent.click(screen.getByRole("button", { name: "Save webhook" }));
    await waitFor(() => expect(sent(fetch, "PATCH", "/repository/r1/hooks/h1")).toHaveLength(1));
    const body = sent(fetch, "PATCH", "/repository/r1/hooks/h1")[0];
    expect(body).toEqual({ url: hook.url, events: ["*"], active: true });
    expect(body).not.toHaveProperty("secret");

    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    await userEvent.click(screen.getByLabelText("Remove the secret"));
    await userEvent.click(screen.getByRole("button", { name: "Save webhook" }));
    await waitFor(() => expect(sent(fetch, "PATCH", "/repository/r1/hooks/h1")).toHaveLength(2));
    expect(sent(fetch, "PATCH", "/repository/r1/hooks/h1")[1].secret).toBe("");
  });

  it("shows deliveries with their payload and redelivers", async () => {
    const failed = delivery({ id: "d2", status: "failed", attempts: 5, response_status: 500, error: "" });
    const fetch = fakeAPI({
      "/repository/r1/hooks": { webhooks: [{ ...hook, active: false }] },
      "/repository/r1/hooks/h1/deliveries": { deliveries: [failed, delivery({ event: "ping" })] },
      "/repository/r1/hooks/h1/deliveries/d2": { ...failed, payload: '{"ref":"refs/heads/main"}', response_body: "boom" },
      "POST /repository/r1/hooks/h1/deliveries/d2/attempts": delivery({ id: "d3", status: "pending", attempts: 0 }),
      "POST /repository/r1/hooks/h1/pings": { message: "ping queued" },
    });
    show();
    expect(await screen.findByText("inactive")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /ci\.example\.com/ }));
    expect(await screen.findByText("HTTP 500 after 5 attempts")).toBeInTheDocument();
    expect(screen.getByLabelText("failed")).toBeInTheDocument();
    expect(screen.getByLabelText("delivered")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /HTTP 500 after 5 attempts/ }));
    expect(await screen.findByText(/"ref": "refs\/heads\/main"/)).toBeInTheDocument();
    expect(screen.getByText("boom")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Redeliver" }));
    await waitFor(() => expect(sent(fetch, "POST", "/repository/r1/hooks/h1/deliveries/d2/attempts")).toHaveLength(1));

    await userEvent.click(screen.getByRole("button", { name: "Ping" }));
    await waitFor(() => expect(sent(fetch, "POST", "/repository/r1/hooks/h1/pings")).toHaveLength(1));
  });

  it("deletes after confirmation", async () => {
    const fetch = fakeAPI({
      "/repository/r1/hooks": { webhooks: [hook] },
      "/repository/r1/hooks/h1/deliveries": { deliveries: [] },
      "DELETE /repository/r1/hooks/h1": { message: "webhook deleted" },
    });
    const confirm = vi.fn(() => false);
    vi.stubGlobal("confirm", confirm);
    show();
    await userEvent.click(await screen.findByRole("button", { name: /ci\.example\.com/ }));
    await userEvent.click(await screen.findByRole("button", { name: "Delete" }));
    expect(confirm).toHaveBeenCalled();
    expect(sent(fetch, "DELETE", "/repository/r1/hooks/h1")).toHaveLength(0);
    confirm.mockReturnValue(true);
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(sent(fetch, "DELETE", "/repository/r1/hooks/h1")).toHaveLength(1));
  });
});
