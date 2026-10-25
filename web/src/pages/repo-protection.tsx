import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ShieldCheck } from "lucide-react";
import { useState } from "react";
import { ErrorText, Section } from "@/components/section";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, type Repository } from "@/lib/api";
import { useRefs } from "@/lib/browse";
import { ruleFor, rulesKey, ruleSummary, useBranchRules, type BranchRule } from "@/lib/protection";
import { useRecentContexts } from "@/lib/statuses";

// Branch protection rules in repository settings: owners edit them,
// maintainers see them.

export function BranchProtection({ repo, isOwner }: { repo: Repository; isOwner: boolean }) {
  const qc = useQueryClient();
  const rules = useBranchRules(repo.id);
  const refs = useRefs(repo.full_path);
  const [editing, setEditing] = useState<string | null>(null); // rule id, or "new"
  const quick = useMutation({
    mutationFn: (pattern: string) => api("POST", `/repository/${repo.id}/branch-protection`, { pattern }),
    onSuccess: () => qc.invalidateQueries({ queryKey: rulesKey(repo.id) }),
  });
  const defaultBranch = refs.data?.default_branch;
  const unprotectedDefault = rules.data && defaultBranch && !ruleFor(rules.data, defaultBranch);

  return (
    <Section
      title="Branch protection"
      description={
        isOwner
          ? "Protected branches refuse force-pushes and deletion. Rules match branch names or patterns like release/* (one level) or hotfix/** (any depth); the most specific rule applies."
          : "Owners manage these rules."
      }
    >
      <ErrorText error={rules.error ?? quick.error} />
      {isOwner && unprotectedDefault && (
        <div className="flex flex-wrap items-center gap-3 rounded-md border border-dashed p-3 text-sm">
          <span className="flex-1">
            The default branch <code className="rounded bg-muted px-1">{defaultBranch}</code> is not protected.
          </span>
          <Button size="sm" variant="outline" onClick={() => quick.mutate(defaultBranch)} disabled={quick.isPending}>
            <ShieldCheck className="h-4 w-4" /> Protect {defaultBranch}
          </Button>
        </div>
      )}
      {rules.data?.length === 0 && !isOwner && <p className="text-sm text-muted-foreground">No branch is protected.</p>}
      {rules.data && rules.data.length > 0 && (
        <ul className="divide-y rounded-md border text-sm">
          {rules.data.map((r) =>
            editing === r.id ? (
              <li key={r.id} className="p-3">
                <RuleForm repo={repo} rule={r} onDone={() => setEditing(null)} />
              </li>
            ) : (
              <li key={r.id} className="flex flex-wrap items-center gap-2 px-3 py-2">
                <code className="font-mono text-xs font-medium">{r.pattern}</code>
                <span className="flex min-w-0 flex-1 flex-wrap gap-1">
                  {ruleSummary(r).map((s) => (
                    <Badge key={s}>{s}</Badge>
                  ))}
                </span>
                {isOwner && (
                  <Button size="sm" variant="ghost" onClick={() => setEditing(r.id)}>
                    Edit
                  </Button>
                )}
              </li>
            ),
          )}
        </ul>
      )}
      {isOwner &&
        (editing === "new" ? (
          <RuleForm repo={repo} onDone={() => setEditing(null)} />
        ) : (
          <Button variant="outline" onClick={() => setEditing("new")}>
            Add rule
          </Button>
        ))}
    </Section>
  );
}

const list = (s: string) =>
  s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean);

// RuleForm creates a rule, or edits one (then it can also delete it).
function RuleForm({ repo, rule, onDone }: { repo: Repository; rule?: BranchRule; onDone: () => void }) {
  const qc = useQueryClient();
  const id = rule?.id ?? "new";
  const [pattern, setPattern] = useState(rule?.pattern ?? "");
  const [forcePush, setForcePush] = useState(rule?.allow_force_push ?? false);
  const [deletion, setDeletion] = useState(rule?.allow_deletion ?? false);
  const [prOnly, setPROnly] = useState(rule?.require_pull_request ?? false);
  const [ownApprovals, setOwnApprovals] = useState(rule ? rule.required_approvals !== null : false);
  const [approvals, setApprovals] = useState(String(rule?.required_approvals ?? repo.required_approvals ?? 1));
  const [stale, setStale] = useState(rule?.dismiss_stale_approvals ?? false);
  const [users, setUsers] = useState(rule?.allowed_users.join(", ") ?? "");
  const [groups, setGroups] = useState(rule?.allowed_groups.join(", ") ?? "");
  const [bypass, setBypass] = useState(rule?.owners_bypass ?? false);
  const [contexts, setContexts] = useState(rule?.required_contexts?.join(", ") ?? "");
  const recent = useRecentContexts(repo.id);

  const done = () => {
    qc.invalidateQueries({ queryKey: rulesKey(repo.id) });
    qc.invalidateQueries({ queryKey: ["pull"] });
    onDone();
  };
  const save = useMutation({
    mutationFn: () => {
      const body = {
        pattern, allow_force_push: forcePush, allow_deletion: deletion, require_pull_request: prOnly,
        required_approvals: ownApprovals ? Number(approvals) : -1, dismiss_stale_approvals: stale,
        allowed_users: list(users), allowed_groups: list(groups), required_contexts: list(contexts), owners_bypass: bypass,
      };
      return rule ? api("PATCH", `/repository/${repo.id}/branch-protection/${rule.id}`, body) : api("POST", `/repository/${repo.id}/branch-protection`, body);
    },
    onSuccess: done,
  });
  const remove = useMutation({
    mutationFn: () => api("DELETE", `/repository/${repo.id}/branch-protection/${rule!.id}`),
    onSuccess: done,
  });
  const check = (label: string, value: boolean, set: (v: boolean) => void, hint?: string) => (
    <label className="flex items-start gap-2">
      <input type="checkbox" className="mt-1" checked={value} onChange={(e) => set(e.target.checked)} />
      <span>
        {label}
        {hint && <span className="block text-xs text-muted-foreground">{hint}</span>}
      </span>
    </label>
  );

  return (
    <form
      className="space-y-3 text-sm"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="space-y-1">
        <Label htmlFor={`rule-pattern-${id}`}>Branch name or pattern</Label>
        <Input id={`rule-pattern-${id}`} placeholder="main, release/*" value={pattern} onChange={(e) => setPattern(e.target.value)} required className="font-mono" />
      </div>
      {check("Require a pull request before merging", prOnly, setPROnly, "Direct pushes are refused; the branch only changes by merging pull requests.")}
      <div className="space-y-1">
        {check("Set the required approvals for this branch", ownApprovals, setOwnApprovals, `Otherwise the repository's setting applies (${repo.required_approvals ?? 0}).`)}
        {ownApprovals && (
          <Input
            aria-label="Required approvals"
            type="number"
            min={0}
            max={10}
            value={approvals}
            onChange={(e) => setApprovals(e.target.value)}
            className="ml-6 w-24"
          />
        )}
      </div>
      {check("Dismiss approvals when new commits are pushed", stale, setStale)}
      <div className="space-y-1">
        <Label htmlFor={`rule-checks-${id}`}>Required checks</Label>
        <Input
          id={`rule-checks-${id}`}
          placeholder="ci/build, lint"
          value={contexts}
          onChange={(e) => setContexts(e.target.value)}
          className="font-mono"
        />
        <p className="text-xs text-muted-foreground">Commit status contexts that must pass on a pull request's latest commit before it can be merged.</p>
        {recent.data && recent.data.length > 0 && (
          <div className="flex flex-wrap items-center gap-1 text-xs">
            <span className="text-muted-foreground">Recently reported:</span>
            {recent.data
              .filter((ctx) => !list(contexts).includes(ctx))
              .map((ctx) => (
                <button
                  key={ctx}
                  type="button"
                  className="rounded border px-1.5 py-0.5 font-mono hover:bg-muted"
                  aria-label={`Require ${ctx}`}
                  onClick={() => setContexts(list(contexts).concat(ctx).join(", "))}
                >
                  + {ctx}
                </button>
              ))}
          </div>
        )}
      </div>
      <div className="grid gap-2 sm:grid-cols-2">
        <div className="space-y-1">
          <Label htmlFor={`rule-users-${id}`}>Only these users can push and merge</Label>
          <Input id={`rule-users-${id}`} placeholder="alice, bob" value={users} onChange={(e) => setUsers(e.target.value)} />
        </div>
        <div className="space-y-1">
          <Label htmlFor={`rule-groups-${id}`}>…and members of these groups</Label>
          <Input id={`rule-groups-${id}`} placeholder="release-team" value={groups} onChange={(e) => setGroups(e.target.value)} />
        </div>
        <p className="text-xs text-muted-foreground sm:col-span-2">Leave both empty to let everyone with write access push and merge.</p>
      </div>
      {check("Allow force-pushes", forcePush, setForcePush)}
      {check("Allow deletion", deletion, setDeletion)}
      {check("Owners can bypass this rule", bypass, setBypass, "Each bypass is recorded in the server's audit log.")}
      <ErrorText error={save.error ?? remove.error} />
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={save.isPending}>
          {rule ? "Save rule" : "Add rule"}
        </Button>
        <Button type="button" variant="ghost" onClick={onDone}>
          Cancel
        </Button>
        {rule && (
          <Button
            type="button"
            variant="destructive"
            className="ml-auto"
            disabled={remove.isPending}
            onClick={() => {
              if (window.confirm(`Stop protecting ${rule.pattern}?`)) remove.mutate();
            }}
          >
            Delete rule
          </Button>
        )}
      </div>
    </form>
  );
}
