import { describe, expect, it } from "vitest";
import { safeLink } from "./statuses";

describe("status links", () => {
  it("only links http(s)", () => {
    expect(safeLink("https://ci.example.com/1")).toBe("https://ci.example.com/1");
    expect(safeLink("http://ci.local/2")).toBe("http://ci.local/2");
    expect(safeLink("javascript:alert(1)")).toBeUndefined();
    expect(safeLink("data:text/html,x")).toBeUndefined();
    expect(safeLink("")).toBeUndefined();
    expect(safeLink("not a url")).toBeUndefined();
  });
});
