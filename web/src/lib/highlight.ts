// Syntax highlighting with highlight.js, loaded on demand (it is large).
// Its output is escaped HTML with <span class="hljs-…"> only.

let loading: Promise<typeof import("highlight.js").default> | undefined;
function load() {
  loading ??= import("highlight.js/lib/common").then((m) => m.default);
  return loading;
}

const byName: Record<string, string> = { dockerfile: "dockerfile", makefile: "makefile", "cmakelists.txt": "cmake" };

// language guesses highlight.js's language from a file name, or undefined.
export function language(path: string, has: (name: string) => boolean): string | undefined {
  const base = path.split("/").pop()!.toLowerCase();
  if (byName[base] && has(byName[base])) return byName[base];
  const ext = base.includes(".") ? base.split(".").pop()! : "";
  return ext && has(ext) ? ext : undefined;
}

// highlightLines returns one HTML string per line, or undefined when the
// language is unknown (show plain text then).
export async function highlightLines(path: string, code: string): Promise<string[] | undefined> {
  const hljs = await load();
  const lang = language(path, (n) => !!hljs.getLanguage(n));
  if (!lang) return undefined;
  return splitLines(hljs.highlight(code, { language: lang, ignoreIllegals: true }).value);
}

// splitLines cuts highlighted HTML at newlines, closing the spans open at
// the end of a line and reopening them on the next, so each line is valid
// HTML on its own (multi-line comments and strings keep their colour).
export function splitLines(html: string): string[] {
  const lines: string[] = [];
  const open: string[] = [];
  let current = "";
  const tag = /<span[^>]*>|<\/span>|\n/g;
  let last = 0;
  for (let m = tag.exec(html); m; m = tag.exec(html)) {
    current += html.slice(last, m.index);
    last = m.index + m[0].length;
    if (m[0] === "\n") {
      lines.push(current + "</span>".repeat(open.length));
      current = open.join("");
    } else if (m[0] === "</span>") {
      open.pop();
      current += m[0];
    } else {
      open.push(m[0]);
      current += m[0];
    }
  }
  current += html.slice(last);
  if (current !== "" || lines.length === 0) lines.push(current);
  return lines;
}
