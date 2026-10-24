import { describe, expect, it } from "vitest";
import { splitCompare } from "@/pages/repo-history";
import { encodePath, joinSpec, relativeTime } from "./browse";
import { language, splitLines } from "./highlight";
import { renderMarkdown } from "./markdown";

describe("highlighting", () => {
  it("keeps spans balanced across lines", () => {
    const html = '<span class="c">/* a\nb */</span>\nx <span class="s">"y"</span>';
    expect(splitLines(html)).toEqual(['<span class="c">/* a</span>', '<span class="c">b */</span>', 'x <span class="s">"y"</span>']);
  });
  it("guesses languages from names", () => {
    const has = (n: string) => ["go", "dockerfile", "ts"].includes(n);
    expect(language("src/main.go", has)).toBe("go");
    expect(language("Dockerfile", has)).toBe("dockerfile");
    expect(language("notes.unknown", has)).toBeUndefined();
    expect(language("LICENSE", has)).toBeUndefined();
  });
});

describe("markdown", () => {
  const opts = { fullPath: "a/b", ref: "feature/x", dir: "docs" };
  it("removes scripts and event handlers", () => {
    const html = renderMarkdown('<script>alert(1)</script><img src="x.png" onerror="alert(2)"><a href="javascript:alert(3)">x</a>', opts);
    expect(html).not.toMatch(/script|onerror|javascript:/);
  });
  it("points relative links at the repository and images at raw files", () => {
    const html = renderMarkdown("[guide](guide.md#setup) [up](../README.md) ![logo](img/logo.png) [web](https://example.com)", opts);
    expect(html).toContain('href="/a/b/blob/feature/x/docs/guide.md#setup"');
    expect(html).toContain('href="/a/b/blob/feature/x/README.md"');
    expect(html).toContain('src="/a/b/raw/feature/x/docs/img/logo.png"');
    expect(html).toMatch(/href="https:\/\/example.com"[^>]*rel="noopener noreferrer nofollow"/);
  });
});

describe("paths", () => {
  it("encodes each segment", () => {
    expect(encodePath("a b/c#d")).toBe("a%20b/c%23d");
    expect(joinSpec("main", "")).toBe("main");
    expect(joinSpec("feature/x", "src/a.go")).toBe("feature/x/src/a.go");
  });
  it("splits compare specs", () => {
    expect(splitCompare("main...feature/x")).toEqual(["main", "feature/x"]);
    expect(splitCompare("main")).toEqual(["", ""]);
  });
  it("formats relative times", () => {
    expect(relativeTime("2026-01-01T00:00:00Z", Date.parse("2026-01-01T00:05:00Z"))).toMatch(/5 minutes ago/);
  });
});
