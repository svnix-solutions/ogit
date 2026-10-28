import DOMPurify from "dompurify";
import { marked } from "marked";
import { encodePath, rawURL } from "./browse";

// renderMarkdown turns a README (or other .md file) into sanitized HTML.
// Relative links point at the repository's pages, relative images at its
// raw files; dir is the file's directory and ref the branch shown.
export function renderMarkdown(source: string, opts: { fullPath: string; ref: string; dir: string; links?: boolean }): string {
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
  if (opts.links) linkify(tpl.content, opts.fullPath);
  tpl.content.querySelectorAll("img[src]").forEach((img) => {
    const src = img.getAttribute("src")!;
    if (relative(src)) img.setAttribute("src", rawURL(opts.fullPath, opts.ref, resolve(src)));
    img.setAttribute("loading", "lazy");
  });
  return tpl.innerHTML;
}

// linkify turns @user into a link to their page and #123 into a link to the
// repository's issue page (which forwards to the pull request when #123 is
// one: they share numbers), outside code and existing links.
function linkify(root: DocumentFragment, fullPath: string) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode: (n) => (n.parentElement?.closest("a, code, pre") ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT),
  });
  const texts: Text[] = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) texts.push(n as Text);
  const pattern = /(^|[^\w@/])(@([a-z0-9][a-z0-9._-]{0,63})|#(\d+))\b/g;
  for (const text of texts) {
    const value = text.data;
    if (!pattern.test(value)) continue;
    pattern.lastIndex = 0;
    const frag = document.createDocumentFragment();
    let last = 0;
    for (let m = pattern.exec(value); m; m = pattern.exec(value)) {
      const start = m.index + m[1].length;
      frag.append(value.slice(last, start));
      const a = document.createElement("a");
      const user = m[3]?.replace(/\.+$/, "");
      a.href = user ? `/${user}` : `/${fullPath}/issues/${m[4]}`;
      a.textContent = user ? `@${user}` : `#${m[4]}`;
      frag.append(a);
      last = start + a.textContent.length;
    }
    frag.append(value.slice(last));
    text.replaceWith(frag);
  }
}
