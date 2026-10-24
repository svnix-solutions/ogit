import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Globe, Lock } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { ErrorText } from "@/components/section";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api, type Repository } from "@/lib/api";
import { useProfile } from "@/lib/auth";
import { useMyGroups } from "@/lib/queries";

// slugPreview mirrors gosimple/slug closely enough for a hint; the server's
// answer (full_path) is what counts.
export function slugPreview(name: string): string {
  return name
    .normalize("NFKD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

export function NewRepoPage() {
  const { data: profile } = useProfile();
  const groups = useMyGroups();
  const [params] = useSearchParams();
  const [namespace, setNamespace] = useState(params.get("namespace") ?? "");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [isPrivate, setPrivate] = useState(true);
  const navigate = useNavigate();
  const qc = useQueryClient();

  // Groups where the user may create: owner or maintainer.
  const owners = [profile?.username ?? "", ...(groups.data ?? []).filter((g) => g.role === "owner" || g.role === "maintainer").map((g) => g.name)];
  const owner = owners.includes(namespace) ? namespace : owners[0];

  const create = useMutation({
    mutationFn: () => api<Repository>("POST", "/repository", { namespace: owner, name, description, is_private: isPrivate }),
    onSuccess: (repo) => {
      qc.invalidateQueries({ queryKey: ["repositories"] });
      navigate(`/${repo.full_path}`);
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    create.mutate();
  }

  const slug = slugPreview(name);
  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <h1 className="text-xl font-semibold">Create a repository</h1>
      <Card className="p-6">
        <form className="space-y-5" onSubmit={submit}>
          <div className="grid grid-cols-[auto_auto_1fr] items-end gap-2">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="owner">Owner</Label>
              <Select id="owner" value={owner} onChange={(e) => setNamespace(e.target.value)}>
                {owners.map((o) => (
                  <option key={o} value={o}>
                    {o}
                  </option>
                ))}
              </Select>
            </div>
            <span className="pb-2 text-muted-foreground">/</span>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="name">Name</Label>
              <Input id="name" required value={name} onChange={(e) => setName(e.target.value)} autoFocus />
            </div>
          </div>
          {name && (
            <p className="text-sm text-muted-foreground">
              {slug ? (
                <>
                  Clone URL: <code>/{owner}/{slug}.git</code>
                </>
              ) : (
                "The name needs at least one letter or digit."
              )}
            </p>
          )}
          <div className="space-y-1.5">
            <Label htmlFor="description">Description (optional)</Label>
            <Textarea id="description" value={description} onChange={(e) => setDescription(e.target.value)} />
          </div>
          <fieldset className="space-y-2">
            <legend className="mb-1 text-sm font-medium">Visibility</legend>
            {[
              { value: true, icon: Lock, label: "Private", hint: "Only members can see it." },
              { value: false, icon: Globe, label: "Public", hint: "Anyone can see and clone it." },
            ].map((o) => (
              <label key={o.label} className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[:checked]:border-primary">
                <input type="radio" name="visibility" className="mt-1" checked={isPrivate === o.value} onChange={() => setPrivate(o.value)} />
                <o.icon className="mt-0.5 h-4 w-4 text-muted-foreground" />
                <span>
                  <span className="block text-sm font-medium">{o.label}</span>
                  <span className="block text-sm text-muted-foreground">{o.hint}</span>
                </span>
              </label>
            ))}
          </fieldset>
          <ErrorText error={create.error} />
          <Button type="submit" disabled={create.isPending || !slug}>
            {create.isPending ? "Creating…" : "Create repository"}
          </Button>
        </form>
      </Card>
    </div>
  );
}
