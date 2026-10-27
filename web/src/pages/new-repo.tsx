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
import { isSSHURL } from "@/lib/mirrors";
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
  // Importing from a remote (or mirroring it).
  const [importing, setImporting] = useState(params.get("import") === "1");
  const [url, setURL] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [mirror, setMirror] = useState(false);
  const [lfs, setLFS] = useState(false);
  const [hours, setHours] = useState(8);
  const navigate = useNavigate();
  const qc = useQueryClient();

  // Groups where the user may create: owner or maintainer.
  const owners = [profile?.username ?? "", ...(groups.data ?? []).filter((g) => g.role === "owner" || g.role === "maintainer").map((g) => g.name)];
  const owner = owners.includes(namespace) ? namespace : owners[0];

  const create = useMutation({
    mutationFn: async () => {
      if (!importing) return api<Repository>("POST", "/repository", { namespace: owner, name, description, is_private: isPrivate });
      const ssh = isSSHURL(url);
      const r = await api<{ repository: Repository }>("POST", "/repository/import", {
        namespace: owner, name, description, is_private: isPrivate, url, mirror, lfs,
        ...(ssh ? {} : { username, password }),
        ...(mirror ? { interval_seconds: Math.round(hours * 3600) } : {}),
      });
      return r.repository;
    },
    onSuccess: (repo) => {
      qc.invalidateQueries({ queryKey: ["repositories"] });
      // Over SSH, the key must go on the remote first: settings show it.
      navigate(importing && isSSHURL(url) ? `/${repo.full_path}/settings#mirror` : `/${repo.full_path}`);
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    create.mutate();
  }

  const slug = slugPreview(name);
  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <h1 className="text-xl font-semibold">{importing ? "Import a repository" : "Create a repository"}</h1>
      <Card className="p-6">
        <form className="space-y-5" onSubmit={submit}>
          <fieldset className="flex gap-4 text-sm">
            <legend className="sr-only">Start</legend>
            <label className="flex items-center gap-2">
              <input type="radio" name="start" checked={!importing} onChange={() => setImporting(false)} /> Start empty
            </label>
            <label className="flex items-center gap-2">
              <input type="radio" name="start" checked={importing} onChange={() => setImporting(true)} /> Import from a URL
            </label>
          </fieldset>
          {importing && (
            <div className="space-y-3 rounded-md border p-4">
              <div className="space-y-1.5">
                <Label htmlFor="import-url">Remote URL</Label>
                <Input id="import-url" required placeholder="https://github.com/owner/repo.git or git@host:owner/repo.git" value={url} onChange={(e) => setURL(e.target.value.trim())} />
              </div>
              {isSSHURL(url) ? (
                <p className="text-xs text-muted-foreground">Over SSH, oGit makes a key for this repository: add its public key on the remote (as a deploy key), shown once it's created.</p>
              ) : (
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="space-y-1.5">
                    <Label htmlFor="import-user">Username (optional)</Label>
                    <Input id="import-user" autoComplete="off" value={username} onChange={(e) => setUsername(e.target.value)} />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="import-password">Password or token (optional)</Label>
                    <Input id="import-password" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
                  </div>
                </div>
              )}
              <label className="flex items-start gap-2 text-sm">
                <input type="checkbox" className="mt-1" checked={mirror} onChange={(e) => setMirror(e.target.checked)} />
                <span>
                  Keep it as a mirror
                  <span className="block text-xs text-muted-foreground">It keeps following the remote and is read-only (pushes refused) until you stop mirroring. Otherwise it's a one-time import.</span>
                </span>
              </label>
              {mirror && (
                <div className="flex items-center gap-2 text-sm">
                  <Label htmlFor="import-hours">Sync every</Label>
                  <Input id="import-hours" type="number" min={0.25} step={0.25} className="w-24" value={hours} onChange={(e) => setHours(Number(e.target.value))} />
                  hours
                </div>
              )}
              <label className="flex items-center gap-2 text-sm">
                <input type="checkbox" checked={lfs} onChange={(e) => setLFS(e.target.checked)} /> Fetch Git LFS objects too
              </label>
            </div>
          )}
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
            {create.isPending ? "Creating…" : importing ? "Import repository" : "Create repository"}
          </Button>
        </form>
      </Card>
    </div>
  );
}
