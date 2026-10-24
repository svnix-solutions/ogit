import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, isUnauthenticated } from "./api";

function mockFetch(status: number, body: unknown) {
  const fn = vi.fn(async () => new Response(body === undefined ? "" : JSON.stringify(body), { status }));
  vi.stubGlobal("fetch", fn);
  return fn;
}

afterEach(() => {
  vi.unstubAllGlobals();
  document.cookie = "ogit_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT";
});

describe("api", () => {
  it("sends the CSRF cookie's value on writes only", async () => {
    document.cookie = "ogit_csrf=abc123";
    const fetch = mockFetch(200, { ok: true });
    await api("POST", "/repository", { name: "x" });
    await api("GET", "/user/profile");
    const [, post] = fetch.mock.calls[0] as unknown as [string, RequestInit];
    const [, get] = fetch.mock.calls[1] as unknown as [string, RequestInit];
    expect((post.headers as Record<string, string>)["X-CSRF-Token"]).toBe("abc123");
    expect((get.headers as Record<string, string>)["X-CSRF-Token"]).toBeUndefined();
    expect(post.credentials).toBe("same-origin");
    expect((fetch.mock.calls[0] as unknown as [string])[0]).toBe("/api/v1/repository");
  });

  it("turns API errors into ApiError", async () => {
    mockFetch(401, { code: 401, details: "invalid credentials", type: "INVALID_CREDENTIALS" });
    const err = await api("POST", "/auth/session", {}).catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 401, type: "INVALID_CREDENTIALS", message: "invalid credentials" });
  });

  it("survives non-JSON error bodies", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("<html>bad gateway</html>", { status: 502, statusText: "Bad Gateway" })));
    const err = await api("GET", "/x").catch((e) => e);
    expect(err).toMatchObject({ status: 502, type: "HTTP_502" });
  });

  it("recognises unauthenticated answers", () => {
    expect(isUnauthenticated(new ApiError(400, "validation_error", "authentication required"))).toBe(true);
    expect(isUnauthenticated(new ApiError(401, "x", "expired"))).toBe(true);
    expect(isUnauthenticated(new ApiError(400, "validation_error", "name is required"))).toBe(false);
    expect(isUnauthenticated(new ApiError(403, "FORBIDDEN", "no"))).toBe(false);
  });
});
