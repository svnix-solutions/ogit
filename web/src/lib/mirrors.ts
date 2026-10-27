import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, repoId } from "./api";

// Pull mirrors and imports (see application/mirror_app.go).

export type Mirror = {
  url: string;
  mirror: boolean; // false: a one-time import in progress
  auth: "none" | "password" | "ssh";
  username?: string;
  has_password: boolean;
  ssh_public_key?: string;
  ssh_host_key?: string;
  lfs: boolean;
  interval_seconds: number;
  status: "pending" | "syncing" | "ok" | "failed";
  last_error?: string;
  last_sync_at: string | null;
  last_success_at: string | null;
  next_sync_at: string;
};

export type MirrorFields = {
  url?: string;
  username?: string;
  password?: string;
  regenerate_key?: boolean;
  lfs?: boolean;
  interval_seconds?: number;
};

const mirrorKey = (fullPath: string) => ["mirror", fullPath];

// isSSHURL: ssh:// or scp-like git@host:path remotes get a generated key.
export const isSSHURL = (url: string) => /^ssh:\/\//.test(url) || /^[^/:@]+@[^/:]+:/.test(url);

// useMirror: the repository's mirror for its maintainers (404: none; data
// stays undefined). It refreshes while a sync is pending or running.
export function useMirror(fullPath: string, enabled = true) {
  return useQuery({
    queryKey: mirrorKey(fullPath),
    queryFn: () => api<Mirror>("GET", `/repository/${repoId(fullPath)}/mirror`),
    enabled,
    retry: false,
    refetchInterval: (q) => (q.state.data && (q.state.data.status === "pending" || q.state.data.status === "syncing") ? 3000 : false),
  });
}

export function useMirrorActions(fullPath: string) {
  const qc = useQueryClient();
  const base = `/repository/${repoId(fullPath)}/mirror`;
  const refresh = () => {
    qc.invalidateQueries({ queryKey: mirrorKey(fullPath) });
    qc.invalidateQueries({ queryKey: ["repository", fullPath] });
  };
  return {
    update: useMutation({ mutationFn: (f: MirrorFields) => api<Mirror>("PATCH", base, f), onSuccess: refresh }),
    sync: useMutation({ mutationFn: () => api<Mirror>("POST", `${base}/sync`), onSuccess: refresh }),
    stop: useMutation({ mutationFn: () => api("DELETE", base), onSuccess: refresh }),
  };
}
