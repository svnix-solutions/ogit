import { describe, expect, it } from "vitest";
import { matchBranch, ruleFor, ruleSummary, type BranchRule } from "./protection";

const rule = (pattern: string, over: Partial<BranchRule> = {}): BranchRule => ({
  id: pattern, pattern, allow_force_push: false, allow_deletion: false, require_pull_request: false, required_approvals: null,
  dismiss_stale_approvals: false, allowed_users: [], allowed_groups: [], required_contexts: [], owners_bypass: false, ...over,
});

describe("branch protection matching (as domain/branch_protection.go)", () => {
  it("matches globs", () => {
    const cases: [string, string, boolean][] = [
      ["main", "main", true], ["main", "main2", false],
      ["release/*", "release/v1", true], ["release/*", "release/v1/hotfix", false], ["release/**", "release/v1/hotfix", true],
      ["v?", "v1", true], ["v?", "v10", false], ["*", "feature/x", false], ["**", "feature/x", true],
      ["feat.x", "featyx", false], ["a+b", "a+b", true],
    ];
    for (const [p, b, want] of cases) expect(matchBranch(p, b), `${p} vs ${b}`).toBe(want);
  });
  it("picks the most specific rule", () => {
    const rules = [rule("**"), rule("release/*"), rule("release/v1"), rule("*")];
    expect(ruleFor(rules, "release/v1")?.pattern).toBe("release/v1");
    expect(ruleFor(rules, "release/v2")?.pattern).toBe("release/*");
    expect(ruleFor(rules, "main")?.pattern).toBe("*");
    expect(ruleFor(rules.slice(1, 3), "main")).toBeUndefined();
  });
  it("summarises rules", () => {
    expect(ruleSummary(rule("main"))).toEqual(["no force-push", "no deletion"]);
    expect(ruleSummary(rule("main", { allow_force_push: true, allow_deletion: true, require_pull_request: true, required_approvals: 1, allowed_users: ["bob"], allowed_groups: ["ops"], owners_bypass: true })))
      .toEqual(["pull requests only", "1 approval", "only bob, ops", "owners can bypass"]);
  });
});
