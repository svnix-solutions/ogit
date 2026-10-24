// Client for the oGit API. The session lives in an HttpOnly cookie that
// scripts can't read; writes echo the ogit_csrf cookie in X-CSRF-Token
// (see interfaces/http/websession).

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly type: string,
    message: string,
  ) {
    super(message);
  }
}

const CSRF_COOKIE = "ogit_csrf";

function readCookie(name: string): string | undefined {
  for (const part of document.cookie.split(";")) {
    const [k, ...v] = part.trim().split("=");
    if (k === name) return decodeURIComponent(v.join("="));
  }
  return undefined;
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET" && method !== "HEAD") {
    const csrf = readCookie(CSRF_COOKIE);
    if (csrf) headers["X-CSRF-Token"] = csrf;
  }
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "same-origin",
  });
  const text = await res.text();
  const data = text ? safeJSON(text) : undefined;
  if (!res.ok) {
    const err = (data ?? {}) as { type?: string; details?: string };
    throw new ApiError(res.status, err.type ?? "HTTP_" + res.status, err.details ?? res.statusText);
  }
  return data as T;
}

function safeJSON(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

// fiber-oapi answers unauthenticated requests with 400 "authentication
// required" (see CLAUDE.md), and expired sessions with 401.
export function isUnauthenticated(err: unknown): boolean {
  return (
    err instanceof ApiError &&
    (err.status === 401 || (err.status === 400 && /authentication required/i.test(err.message)))
  );
}

export type Profile = { id: string; username: string; email: string; avatar: string };
export type RepositorySummary = {
  id: string;
  slug: string;
  name: string;
  description: string;
  is_private: boolean;
  archived: boolean;
};
