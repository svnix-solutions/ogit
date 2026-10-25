import { useQuery } from "@tanstack/react-query";
import { api } from "./api";

// Branch protection rules (see application/branch_protection.go). The
// matching mirrors domain/branch_protection.go: * within a path segment,
// ** across segments, ? one character; the most specific rule wins.

export type BranchRule = {
  id: string;
  pattern: string;
  allow_force_push: boolean;
  allow_deletion: boolean;
  require_pull_request: boolean;
  required_approvals: number | null; // null: the repository's
  dismiss_stale_approvals: boolean;
  allowed_users: string[];
  allowed_groups: string[];
  owners_bypass: boolean;
};

const hasWildcard = (p: string) => /[*?]/.test(p);

function patternRegExp(p: string): RegExp {
  let re = "^";
  for (let i = 0; i < p.length; i++) {
    if (p.startsWith("**", i)) {
      re += ".*";
      i++;
    } else if (p[i] === "*") re += "[^/]*";
    else if (p[i] === "?") re += "[^/]";
    else re += p[i].replace(/[.+^${}()|[\]\\]/g, "\\$&");
  }
  return new RegExp(re + "$");
}

export function matchBranch(pattern: string, branch: string): boolean {
  return hasWildcard(pattern) ? patternRegExp(pattern).test(branch) : pattern === branch;
}

const literals = (p: string) => p.length - (p.match(/[*?]/g)?.length ?? 0);

function moreSpecific(a: string, b: string): boolean {
  if (hasWildcard(a) !== hasWildcard(b)) return !hasWildcard(a);
  if (literals(a) !== literals(b)) return literals(a) > literals(b);
  if (a.length !== b.length) return a.length < b.length;
  return a < b;
}

export function ruleFor(rules: BranchRule[], branch: string): BranchRule | undefined {
  let best: BranchRule | undefined;
  for (const r of rules) if (matchBranch(r.pattern, branch) && (!best || moreSpecific(r.pattern, best.pattern))) best = r;
  return best;
}

// ruleSummary lists what a rule enforces, for lists and badges.
export function ruleSummary(r: BranchRule): string[] {
  const out: string[] = [];
  if (!r.allow_force_push) out.push("no force-push");
  if (!r.allow_deletion) out.push("no deletion");
  if (r.require_pull_request) out.push("pull requests only");
  if (r.required_approvals !== null) out.push(`${r.required_approvals} ${r.required_approvals === 1 ? "approval" : "approvals"}`);
  if (r.dismiss_stale_approvals) out.push("stale approvals dismissed");
  const who = [...r.allowed_users, ...r.allowed_groups];
  if (who.length) out.push(`only ${who.join(", ")}`);
  if (r.owners_bypass) out.push("owners can bypass");
  return out;
}

export const rulesKey = (repoId: string) => ["branch-protection", repoId];

// useBranchRules reads the rules (developers and up; pass enabled=false
// for others, who get 403).
export function useBranchRules(repoId: string, enabled = true) {
  return useQuery({
    queryKey: rulesKey(repoId),
    queryFn: async () => (await api<{ rules: BranchRule[] | null }>("GET", `/repository/${repoId}/branch-protection`)).rules ?? [],
    enabled,
  });
}
