import { confirmSudo } from "./sudo";

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

// csrfToken is the value writes send in X-CSRF-Token (uploads use it too).
export function csrfToken(): string | undefined {
  return readCookie(CSRF_COOKIE);
}

// apiError turns an error answer into an ApiError.
export function apiError(status: number, statusText: string, data: unknown): ApiError {
  const err = (data ?? {}) as { type?: string; details?: string };
  return new ApiError(status, err.type ?? "HTTP_" + status, err.details ?? statusText);
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  try {
    return await request<T>(method, path, body);
  } catch (e) {
    // Sensitive actions may ask to confirm it's you first; then retry once.
    if (e instanceof ApiError && e.type === "SUDO_REQUIRED" && path !== "/auth/sudo" && (await confirmSudo())) {
      return request<T>(method, path, body);
    }
    throw e;
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
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
  if (!res.ok) throw apiError(res.status, res.statusText, data);
  return data as T;
}

export function safeJSON(text: string): unknown {
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

export type Profile = { id: string; username: string; email: string; avatar: string; role: "admin" | "user"; two_factor_enabled?: boolean };
export type RepositorySummary = {
  id: string;
  namespace: string;
  slug: string;
  full_path: string; // <namespace>/<slug>
  name: string;
  description: string;
  is_private: boolean;
  archived: boolean;
};

// A repository's API identifier: its id, or its path with the slash encoded.
export function repoId(fullPath: string): string {
  return encodeURIComponent(fullPath);
}

export type Role = "none" | "reporter" | "developer" | "maintainer" | "owner";
export const roles: Role[] = ["reporter", "developer", "maintainer", "owner"];
const roleLevel: Record<Role, number> = { none: 0, reporter: 1, developer: 2, maintainer: 3, owner: 4 };
export function atLeast(role: Role | undefined, min: Role): boolean {
  return roleLevel[role ?? "none"] >= roleLevel[min];
}

export type Repository = RepositorySummary & { owner_type: "user" | "group"; role?: Role; required_approvals?: number; has_issues?: boolean;
  is_fork?: boolean; fork_of?: { id: string; full_path: string } };

export type Member = { id: string; type: "user" | "group"; username?: string; group?: string; role: Role };

export type Group = { id: string; name: string; description: string; role?: "owner" | "maintainer" | "member" };
export type GroupMember = { username: string; role: "owner" | "maintainer" | "member" };

export type NamespaceView = {
  name: string;
  type: "user" | "group";
  description?: string;
  can_create: boolean;
  repositories: RepositorySummary[];
  total: number;
};

export type Instance = { version: string; registration_enabled: boolean; ssh_enabled: boolean; ssh_port?: number; max_asset_size?: number; passkeys?: boolean };

export type Token = { id: string; name: string; description: string; scopes: string[]; expires_at?: string; created_at: string };

export type SSHKey = {
  id: string;
  name: string;
  fingerprint: string;
  type: string;
  read_only?: boolean;
  last_used_at?: string | null;
  created_at: string;
};

export type AdminUser = {
  id: string;
  username: string;
  email: string;
  role: "admin" | "user";
  active: boolean;
  email_verified_at: string | null;
  created_at: string;
  two_factor_enabled?: boolean;
};
