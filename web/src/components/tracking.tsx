import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { ErrorText } from "@/components/section";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { api } from "@/lib/api";
import { useAssignable, useMilestones } from "@/lib/issues";
import type { MilestoneRef } from "@/lib/pulls";

// Sidebar boxes shared by issues and pull requests: base is the item's API
// path (".../issues/12" or ".../pulls/12").

export function AssigneesBox({ fullPath, base, assignees, canSet, onChange }: { fullPath: string; base: string; assignees: string[]; canSet: boolean; onChange: () => void }) {
  const candidates = useAssignable(fullPath, canSet);
  const [other, setOther] = useState("");
  const set = useMutation({
    mutationFn: (names: string[]) => api("PUT", `${base}/assignees`, { assignees: names }),
    onSuccess: () => (setOther(""), onChange()),
  });
  const options = [...new Set([...(candidates.data ?? []), ...assignees])].sort();
  return (
    <div className="space-y-2">
      <h3 className="font-medium">Assignees</h3>
      {!canSet && (assignees.length === 0 ? <p className="text-muted-foreground">No one</p> : <p>{assignees.join(", ")}</p>)}
      {canSet && (
        <>
          {options.length === 0 && <p className="text-muted-foreground">No one</p>}
          <div className="space-y-1">
            {options.map((name) => (
              <label key={name} className="flex items-center gap-2">
                <input
                  type="checkbox"
                  checked={assignees.includes(name)}
                  onChange={(e) => set.mutate(e.target.checked ? [...assignees, name] : assignees.filter((a) => a !== name))}
                />
                {name}
              </label>
            ))}
          </div>
          <form
            className="flex gap-1"
            onSubmit={(e) => {
              e.preventDefault();
              if (other.trim()) set.mutate([...assignees, other.trim()]);
            }}
          >
            <Input aria-label="Assign someone else" placeholder="username" value={other} onChange={(e) => setOther(e.target.value)} className="h-8" />
            <Button type="submit" size="sm" variant="outline" disabled={!other.trim() || set.isPending}>
              Add
            </Button>
          </form>
        </>
      )}
      <ErrorText error={set.error} />
    </div>
  );
}

export function MilestoneBox({ fullPath, base, milestone, canSet, onChange }: { fullPath: string; base: string; milestone?: MilestoneRef; canSet: boolean; onChange: () => void }) {
  const milestones = useMilestones(fullPath, "open");
  const set = useMutation({ mutationFn: (id: string) => api("PUT", `${base}/milestone`, { milestone_id: id }), onSuccess: onChange });
  const link = milestone && (
    <Link to={`/${fullPath}/milestones/${milestone.id}`} className="text-primary hover:underline">
      {milestone.title}
    </Link>
  );
  return (
    <div className="space-y-2">
      <h3 className="font-medium">Milestone</h3>
      {!canSet && (link ?? <p className="text-muted-foreground">None</p>)}
      {canSet && (
        <Select aria-label="Milestone" value={milestone?.id ?? ""} onChange={(e) => set.mutate(e.target.value)} disabled={set.isPending}>
          <option value="">None</option>
          {milestone && !milestones.data?.some((m) => m.id === milestone.id) && <option value={milestone.id}>{milestone.title}</option>}
          {milestones.data?.map((m) => (
            <option key={m.id} value={m.id}>
              {m.title}
            </option>
          ))}
        </Select>
      )}
      <ErrorText error={set.error} />
    </div>
  );
}
