import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ErrorText, Section } from "@/components/section";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, type Repository } from "@/lib/api";
import { tagRulesKey, useTagRules, type TagRule } from "@/lib/releases";

// Protected tags in repository settings (application/protected_tag.go):
// owners edit the rules, maintainers see them.

export function ProtectedTags({ repo, isOwner }: { repo: Repository; isOwner: boolean }) {
  const rules = useTagRules(repo.id);
  const [editing, setEditing] = useState<string | null>(null); // rule id, or "new"
  return (
    <Section
      title="Protected tags"
      description={
        isOwner
          ? "Only maintainers and owners, and the users and groups a rule lists, can create matching tags, by a push or a release. Nobody can move or delete them, except owners (logged)."
          : "Owners manage these rules."
      }
    >
      <ErrorText error={rules.error} />
      {rules.data?.length === 0 && !isOwner && <p className="text-sm text-muted-foreground">No tag is protected.</p>}
      {rules.data && rules.data.length > 0 && (
        <ul className="divide-y rounded-md border text-sm">
          {rules.data.map((r) =>
            editing === r.id ? (
              <li key={r.id} className="p-3">
                <TagRuleForm repo={repo} rule={r} onDone={() => setEditing(null)} />
              </li>
            ) : (
              <li key={r.id} className="flex flex-wrap items-center gap-2 px-3 py-2">
                <code className="font-mono text-xs font-medium">{r.pattern}</code>
                <span className="flex min-w-0 flex-1 flex-wrap gap-1">
                  {[...r.allowed_users, ...r.allowed_groups].length > 0 ? (
                    <Badge>also {[...r.allowed_users, ...r.allowed_groups].join(", ")}</Badge>
                  ) : (
                    <Badge>maintainers only</Badge>
                  )}
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
          <TagRuleForm repo={repo} onDone={() => setEditing(null)} />
        ) : (
          <Button variant="outline" onClick={() => setEditing("new")}>
            Protect tags
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

function TagRuleForm({ repo, rule, onDone }: { repo: Repository; rule?: TagRule; onDone: () => void }) {
  const qc = useQueryClient();
  const id = rule?.id ?? "new";
  const [pattern, setPattern] = useState(rule?.pattern ?? "");
  const [users, setUsers] = useState(rule?.allowed_users.join(", ") ?? "");
  const [groups, setGroups] = useState(rule?.allowed_groups.join(", ") ?? "");
  const done = () => {
    qc.invalidateQueries({ queryKey: tagRulesKey(repo.id) });
    onDone();
  };
  const base = `/repository/${repo.id}/protected-tags`;
  const save = useMutation({
    mutationFn: () => {
      const body = { pattern, allowed_users: list(users), allowed_groups: list(groups) };
      return rule ? api("PATCH", `${base}/${rule.id}`, body) : api("POST", base, body);
    },
    onSuccess: done,
  });
  const remove = useMutation({ mutationFn: () => api("DELETE", `${base}/${rule!.id}`), onSuccess: done });

  return (
    <form
      className="space-y-3 text-sm"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="space-y-1">
        <Label htmlFor={`tag-pattern-${id}`}>Tag name or pattern</Label>
        <Input id={`tag-pattern-${id}`} placeholder="v*, release/**" value={pattern} onChange={(e) => setPattern(e.target.value)} required className="font-mono" />
      </div>
      <div className="grid gap-2 sm:grid-cols-2">
        <div className="space-y-1">
          <Label htmlFor={`tag-users-${id}`}>These users can create them too</Label>
          <Input id={`tag-users-${id}`} placeholder="release-bot" value={users} onChange={(e) => setUsers(e.target.value)} />
        </div>
        <div className="space-y-1">
          <Label htmlFor={`tag-groups-${id}`}>…and members of these groups</Label>
          <Input id={`tag-groups-${id}`} placeholder="release-team" value={groups} onChange={(e) => setGroups(e.target.value)} />
        </div>
        <p className="text-xs text-muted-foreground sm:col-span-2">Maintainers and owners always can. Deploy keys and repository tokens never can.</p>
      </div>
      <ErrorText error={save.error ?? remove.error} />
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={save.isPending}>
          {rule ? "Save rule" : "Protect tags"}
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
            onClick={() => window.confirm(`Stop protecting ${rule.pattern}?`) && remove.mutate()}
          >
            Delete rule
          </Button>
        )}
      </div>
    </form>
  );
}
