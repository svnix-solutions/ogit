import DOMPurify from "dompurify";
import { marked } from "marked";
import { encodePath, rawURL } from "./browse";

// renderMarkdown turns a README (or other .md file) into sanitized HTML.
// Relative links point at the repository's pages, relative images at its
// raw files; dir is the file's directory and ref the branch shown.
export function renderMarkdown(source: string, opts: { fullPath: string; ref: string; dir: string }): string {
  const html = DOMPurify.sanitize(marked.parse(source, { async: false, gfm: true }) as string, {
    USE_PROFILES: { html: true },
  });
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  const resolve = (target: string) => {
    const parts = (opts.dir ? opts.dir.split("/") : []).concat(target.split("/"));
    const out: string[] = [];
    for (const p of parts) {
      if (p === "..") out.pop();
      else if (p && p !== ".") out.push(p);
    }
    return out.join("/");
  };
  const relative = (url: string) => !/^([a-z][a-z0-9+.-]*:|\/\/|#|\/)/i.test(url);
  tpl.content.querySelectorAll("a[href]").forEach((a) => {
    const href = a.getAttribute("href")!;
    if (relative(href)) {
      const [path, hash] = href.split("#");
      a.setAttribute("href", `/${opts.fullPath}/blob/${encodePath(opts.ref)}/${encodePath(resolve(path))}${hash ? "#" + hash : ""}`);
    } else if (/^https?:/i.test(href)) {
      a.setAttribute("rel", "noopener noreferrer nofollow");
      a.setAttribute("target", "_blank");
    }
  });
  tpl.content.querySelectorAll("img[src]").forEach((img) => {
    const src = img.getAttribute("src")!;
    if (relative(src)) img.setAttribute("src", rawURL(opts.fullPath, opts.ref, resolve(src)));
    img.setAttribute("loading", "lazy");
  });
  return tpl.innerHTML;
}
