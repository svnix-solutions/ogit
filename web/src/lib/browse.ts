import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, repoId } from "./api";

export type Signature = { name: string; email: string; when: string };
export type Commit = { hash: string; parents: string[]; message: string; author: Signature; committer: Signature };
export type Ref = { name: string; commit: Commit };
export type Entry = { name: string; path: string; type: "dir" | "file" | "symlink" | "submodule"; size?: number };
// lfs: the file is a Git LFS pointer; present when the server has its content (served by the raw URL).
export type LFSPointer = { oid: string; size: number; present: boolean };
export type FileContent = { path: string; size: number; binary: boolean; truncated: boolean; symlink?: boolean; content?: string; lfs?: LFSPointer };
export type Line = { kind: " " | "+" | "-"; text: string; old?: number; new?: number };
export type Hunk = { old_start: number; old_lines: number; new_start: number; new_lines: number; lines: Line[] };
export type FileDiff = {
  path: string;
  old_path?: string;
  status: "added" | "deleted" | "modified" | "renamed";
  binary: boolean;
  additions: number;
  deletions: number;
  hunks: Hunk[];
  truncated: boolean;
};
export type Diff = { files: FileDiff[]; additions: number; deletions: number; truncated: boolean };
export type Comparison = { merge_base: string; commits: Commit[]; commits_truncated: boolean; diff: Diff };
export type BlameLine = { text: string; hash: string; author: string; when: string };

// The repository part of page URLs and API paths.
export const repoBase = (fullPath: string) => `/${fullPath}`;
const apiBase = (fullPath: string) => `/repository/${repoId(fullPath)}`;

// spec is "<ref>/<path>" as in page URLs; refs may contain slashes, the
// server splits them.
export function joinSpec(ref: string, path: string): string {
  return path ? `${ref}/${path}` : ref;
}

// encodePath keeps slashes but escapes everything else in a path segment.
export function encodePath(path: string): string {
  return path.split("/").map(encodeURIComponent).join("/");
}

export function useRefs(fullPath: string) {
  return useQuery({
    queryKey: ["refs", fullPath],
    queryFn: () => api<{ default_branch: string; branches: Ref[]; tags: Ref[] }>("GET", `${apiBase(fullPath)}/refs`),
  });
}

export function useTree(fullPath: string, spec: string) {
  return useQuery({
    queryKey: ["tree", fullPath, spec],
    queryFn: () =>
      api<{ empty: boolean; ref: string; path: string; commit?: Commit; entries: Entry[]; readme?: FileContent }>(
        "GET",
        `${apiBase(fullPath)}/tree?spec=${encodeURIComponent(spec)}`,
      ),
    retry: false,
  });
}

export function useBlob(fullPath: string, spec: string) {
  return useQuery({
    queryKey: ["blob", fullPath, spec],
    queryFn: () => api<{ ref: string; commit: Commit; file: FileContent }>("GET", `${apiBase(fullPath)}/blob?spec=${encodeURIComponent(spec)}`),
    retry: false,
  });
}

export function useCommits(fullPath: string, spec: string, page: number, perPage = 30) {
  return useQuery({
    queryKey: ["commits", fullPath, spec, page],
    queryFn: () =>
      api<{ ref: string; path: string; commits: Commit[]; more: boolean }>(
        "GET",
        `${apiBase(fullPath)}/commits?spec=${encodeURIComponent(spec)}&offset=${page * perPage}&limit=${perPage}`,
      ),
    placeholderData: keepPreviousData,
    retry: false,
  });
}

export function useCommit(fullPath: string, sha: string) {
  return useQuery({
    queryKey: ["commit", fullPath, sha],
    queryFn: () => api<{ commit: Commit; diff: Diff }>("GET", `${apiBase(fullPath)}/commits/${encodeURIComponent(sha)}`),
    retry: false,
  });
}

export function useCompare(fullPath: string, base: string, head: string) {
  return useQuery({
    queryKey: ["compare", fullPath, base, head],
    queryFn: () => api<Comparison>("GET", `${apiBase(fullPath)}/compare?base=${encodeURIComponent(base)}&head=${encodeURIComponent(head)}`),
    enabled: !!base && !!head,
    retry: false,
  });
}

export function useBlame(fullPath: string, spec: string) {
  return useQuery({
    queryKey: ["blame", fullPath, spec],
    queryFn: () => api<{ ref: string; path: string; lines: BlameLine[] }>("GET", `${apiBase(fullPath)}/blame?spec=${encodeURIComponent(spec)}`),
    retry: false,
  });
}

export function rawURL(fullPath: string, ref: string, path: string): string {
  return `/${fullPath}/raw/${encodePath(ref)}/${encodePath(path)}`;
}

export function shortHash(hash: string): string {
  return hash.slice(0, 7);
}

// The first line of a commit message.
export function summary(message: string): string {
  return message.split("\n", 1)[0];
}

export function relativeTime(iso: string, now = Date.now()): string {
  const s = Math.round((now - new Date(iso).getTime()) / 1000);
  const units: [number, Intl.RelativeTimeFormatUnit][] = [
    [60, "second"],
    [60, "minute"],
    [24, "hour"],
    [30, "day"],
    [12, "month"],
    [Infinity, "year"],
  ];
  let value = s;
  for (const [size, unit] of units) {
    if (Math.abs(value) < size) return new Intl.RelativeTimeFormat(undefined, { numeric: "auto" }).format(-value, unit);
    value = Math.round(value / size);
  }
  return "";
}

export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GB`;
}
