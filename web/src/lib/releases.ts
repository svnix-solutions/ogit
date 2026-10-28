import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, apiError, csrfToken, repoId, safeJSON } from "./api";

// Releases (see application/release_app.go and release_asset.go).

export type Asset = {
  id: string;
  name: string;
  label: string;
  content_type: string;
  size: number;
  download_count: number;
  uploader?: string;
  created_at: string;
  url: string; // through the API (drafts too)
  browser_download_url?: string; // the public link, published releases only
};

export type Release = {
  id: string;
  tag_name: string;
  target_commitish: string;
  name: string;
  body: string;
  draft: boolean;
  prerelease: boolean;
  author?: string;
  created_at: string;
  published_at: string | null;
  can_edit: boolean;
  assets: Asset[];
};

export type TagRule = { id: string; pattern: string; allowed_users: string[]; allowed_groups: string[] };

export const releasesBase = (fullPath: string) => `/repository/${repoId(fullPath)}/releases`;
export const releasesKey = (fullPath: string) => ["releases", fullPath];

export function useReleases(fullPath: string, page: number, perPage = 10) {
  return useQuery({
    queryKey: [...releasesKey(fullPath), "list", page],
    queryFn: () => api<{ releases: Release[]; total: number }>("GET", `${releasesBase(fullPath)}?offset=${page * perPage}&limit=${perPage}`),
    placeholderData: keepPreviousData,
  });
}

// useReleaseCount is the number on the Releases tab (drafts count for
// writers). Errors just hide it.
export function useReleaseCount(fullPath: string) {
  return useQuery({
    queryKey: [...releasesKey(fullPath), "count"],
    queryFn: async () => (await api<{ total: number }>("GET", `${releasesBase(fullPath)}?limit=1`)).total,
    retry: false,
  });
}

export function useRelease(fullPath: string, id: string) {
  return useQuery({
    queryKey: [...releasesKey(fullPath), "id", id],
    queryFn: () => api<Release>("GET", `${releasesBase(fullPath)}/${encodeURIComponent(id)}`),
    enabled: !!id,
    retry: false,
  });
}

export function useReleaseByTag(fullPath: string, tag: string) {
  return useQuery({
    queryKey: [...releasesKey(fullPath), "tag", tag],
    queryFn: () => api<Release>("GET", `${releasesBase(fullPath)}/by-tag?tag=${encodeURIComponent(tag)}`),
    enabled: !!tag,
    retry: false,
  });
}

// useLatestRelease: the newest published release that is not a
// pre-release; none is a 404, kept as null.
export function useLatestRelease(fullPath: string) {
  return useQuery({
    queryKey: [...releasesKey(fullPath), "latest"],
    queryFn: async () => {
      try {
        return await api<Release>("GET", `${releasesBase(fullPath)}/latest`);
      } catch (e) {
        if ((e as { status?: number }).status === 404) return null;
        throw e;
      }
    },
    retry: false,
  });
}

export const releasePath = (fullPath: string, tag: string) => `/${fullPath}/releases/tag/${tag.split("/").map(encodeURIComponent).join("/")}`;

// archiveURL is a tag's source code download.
export const archiveURL = (fullPath: string, tag: string, format: "zip" | "tar.gz") =>
  `/${fullPath}/archive/${tag.split("/").map(encodeURIComponent).join("/")}.${format}`;

// downloadURL: the public link once published, else the API's (writers).
export const downloadURL = (a: Asset) => a.browser_download_url || a.url;

// uploadAsset sends a file as the request body (the server streams it),
// reporting progress from 0 to 1.
export function uploadAsset(fullPath: string, releaseId: string, file: File, onProgress: (fraction: number) => void): Promise<Asset> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", `/api/v1${releasesBase(fullPath)}/${releaseId}/assets?name=${encodeURIComponent(file.name)}`);
    xhr.setRequestHeader("Accept", "application/json");
    xhr.setRequestHeader("Content-Type", file.type || "application/octet-stream");
    const csrf = csrfToken();
    if (csrf) xhr.setRequestHeader("X-CSRF-Token", csrf);
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded / e.total);
    xhr.onload = () => {
      const data = xhr.responseText ? safeJSON(xhr.responseText) : undefined;
      if (xhr.status >= 200 && xhr.status < 300) resolve(data as Asset);
      else reject(apiError(xhr.status, xhr.statusText, data));
    };
    xhr.onerror = () => reject(new Error("The upload was interrupted."));
    xhr.send(file);
  });
}

export const tagRulesKey = (repoId: string) => ["protected-tags", repoId];

export function useTagRules(repoId: string) {
  return useQuery({
    queryKey: tagRulesKey(repoId),
    queryFn: async () => (await api<{ rules: TagRule[] | null }>("GET", `/repository/${repoId}/protected-tags`)).rules ?? [],
  });
}
