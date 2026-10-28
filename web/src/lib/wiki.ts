import { useQuery } from "@tanstack/react-query";
import DOMPurify from "dompurify";
import { marked } from "marked";
import { api, apiError, csrfToken, repoId, safeJSON } from "./api";
import { encodePath, type Commit } from "./browse";

// Wikis (see application/wiki_app.go). History, diffs and raw files are the
// browse API and raw routes on "<full path>.wiki".

export type WikiAccess = "writers" | "readers" | "off";
export type WikiPageRef = { name: string; title: string; path: string };
export type WikiInfo = { access: WikiAccess; exists: boolean; can_edit: boolean; head?: string; pages: WikiPageRef[]; truncated?: boolean };
export type WikiPage = WikiPageRef & { content: string; commit?: Commit };
export type WikiView = { page: WikiPage; sidebar?: WikiPage; footer?: WikiPage; head: string };
export type WikiSearchResult = WikiPageRef & { snippet: string };

export const wikiBase = (fullPath: string) => `/repository/${repoId(fullPath)}/wiki`;
export const wikiKey = (fullPath: string) => ["wiki", fullPath];
// wikiRepo is the wiki's path for the browse API and raw routes.
export const wikiRepo = (fullPath: string) => `${fullPath}.wiki`;
export const wikiPagePath = (fullPath: string, name: string) => `/${fullPath}/wiki/${encodePath(name)}`;

export function useWiki(fullPath: string) {
  return useQuery({ queryKey: [...wikiKey(fullPath), "info"], queryFn: () => api<WikiInfo>("GET", wikiBase(fullPath)), retry: false });
}

export function useWikiPage(fullPath: string, name: string, version?: string) {
  const q = new URLSearchParams({ name });
  if (version) q.set("version", version);
  return useQuery({
    queryKey: [...wikiKey(fullPath), "page", name, version ?? ""],
    queryFn: () => api<WikiView>("GET", `${wikiBase(fullPath)}/page?${q}`),
    retry: false,
  });
}

export function useWikiSearch(fullPath: string, query: string) {
  return useQuery({
    queryKey: [...wikiKey(fullPath), "search", query],
    queryFn: async () => (await api<{ results: WikiSearchResult[] }>("GET", `${wikiBase(fullPath)}/search?q=${encodeURIComponent(query)}`)).results,
    enabled: query.trim() !== "",
  });
}

// pageName is the page a link target names: "Getting Started" and
// "Getting-Started.md" are both Getting-Started.
export function pageName(target: string): string {
  return target
    .trim()
    .replace(/\.(md|markdown)$/i, "")
    .split("/")
    .map((s) => s.trim().split(/\s+/).join("-"))
    .join("/");
}

// wikiLinks turns [[Page]] and [[Text|Page]] into Markdown links, outside
// code.
export function wikiLinks(source: string): string {
  return source
    .split(/(```[\s\S]*?```|`[^`\n]*`)/)
    .map((part, i) =>
      i % 2 === 1
        ? part
        : part.replace(/\[\[([^\]|\n]+?)(?:\|([^\]\n]+?))?\]\]/g, (_, a: string, b?: string) => {
            const target = b ?? a; // gollum's order: [[text|page]]
            return `[${a.trim()}](<${pageName(target)}>)`;
          }),
    )
    .join("");
}

// renderWiki renders a page: links to other pages go to their wiki pages
// (red, to create them, when missing), relative images and files to the
// wiki's raw files at ref. dir is the page's folder.
export function renderWiki(source: string, opts: { fullPath: string; ref: string; dir: string; pages?: Set<string> }): string {
  const html = DOMPurify.sanitize(marked.parse(wikiLinks(source), { async: false, gfm: true }) as string, { USE_PROFILES: { html: true } });
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  const resolve = (target: string) => {
    const parts = target.startsWith("/") ? target.split("/") : (opts.dir ? opts.dir.split("/") : []).concat(target.split("/"));
    const out: string[] = [];
    for (const p of parts) {
      if (p === "..") out.pop();
      else if (p && p !== ".") out.push(p);
    }
    return out.join("/");
  };
  const external = (url: string) => /^([a-z][a-z0-9+.-]*:|\/\/|#)/i.test(url);
  const raw = (path: string) => `/${wikiRepo(opts.fullPath)}/raw/${encodePath(opts.ref)}/${encodePath(path)}`;
  tpl.content.querySelectorAll("a[href]").forEach((a) => {
    const href = decodeURI(a.getAttribute("href")!);
    if (external(href)) {
      if (/^https?:/i.test(href)) {
        a.setAttribute("rel", "noopener noreferrer nofollow");
        a.setAttribute("target", "_blank");
      }
      return;
    }
    const [path, hash] = href.split("#");
    const target = resolve(path);
    const isPage = !/\.[a-z0-9]+$/i.test(target) || /\.(md|markdown)$/i.test(target);
    if (!isPage) {
      a.setAttribute("href", raw(target));
      return;
    }
    const name = pageName(target);
    a.setAttribute("href", wikiPagePath(opts.fullPath, name) + (hash ? "#" + hash : ""));
    if (opts.pages && !opts.pages.has(name)) {
      a.setAttribute("href", `/${opts.fullPath}/wiki?new=${encodeURIComponent(name)}`);
      a.setAttribute("data-missing", "true");
      a.setAttribute("class", "text-destructive");
      a.setAttribute("title", "This page doesn't exist yet");
    }
  });
  tpl.content.querySelectorAll("img[src]").forEach((img) => {
    const src = img.getAttribute("src")!;
    if (!external(src)) img.setAttribute("src", raw(resolve(src)));
    img.setAttribute("loading", "lazy");
  });
  return tpl.innerHTML;
}

// uploadAttachment sends a file (streamed by the server; 25 MiB at most)
// and returns its path in the wiki, under uploads/.
export function uploadAttachment(fullPath: string, file: File): Promise<{ path: string; commit: string }> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", `/api/v1${wikiBase(fullPath)}/uploads?name=${encodeURIComponent(file.name)}`);
    xhr.setRequestHeader("Accept", "application/json");
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    const csrf = csrfToken();
    if (csrf) xhr.setRequestHeader("X-CSRF-Token", csrf);
    xhr.onload = () => {
      const data = xhr.responseText ? safeJSON(xhr.responseText) : undefined;
      if (xhr.status >= 200 && xhr.status < 300) resolve(data as { path: string; commit: string });
      else reject(apiError(xhr.status, xhr.statusText, data));
    };
    xhr.onerror = () => reject(new Error("The upload was interrupted."));
    xhr.send(file);
  });
}

export const maxAttachment = 25 << 20;
