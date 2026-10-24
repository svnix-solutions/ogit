import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { ConfirmDelete } from "@/components/confirm-delete";
import { ErrorText, Section } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api, ApiError, atLeast, roles, type Member, type Repository, type Role, type SSHKey } from "@/lib/api";
import { useProfile } from "@/lib/auth";
import { formatDate, useMyGroups } from "@/lib/queries";
import { NotFoundPage } from "./not-found";
import { useRepository } from "./repo";

export function RepoSettingsPage() {
  const { owner = "", repo = "" } = useParams();
  const r = useRepository(owner, repo);

  if (r.error instanceof ApiError && r.error.status === 404) return <NotFoundPage />;
  if (r.isError) return <Alert variant="destructive">{r.error.message}</Alert>;
  if (!r.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (!atLeast(r.data.role, "maintainer")) return <Alert variant="destructive">Only maintainers and owners can change this repository.</Alert>;

  const isOwner = r.data.role === "owner";
  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <h1 className="text-xl">
        <Link to={`/${r.data.full_path}`} className="text-primary hover:underline">
          {r.data.full_path}
        </Link>
        <span className="text-muted-foreground"> · settings</span>
      </h1>
      <General repo={r.data} isOwner={isOwner} />
      <Members repo={r.data} isOwner={isOwner} />
      <DeployKeys repo={r.data} />
      {isOwner && <Transfer repo={r.data} />}
      {isOwner && <Delete repo={r.data} />}
    </div>
  );
}

// After a rename or a move, the page's URL changes too.
function useRepoChanged() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  return (updated: Repository, before: Repository) => {
    qc.invalidateQueries({ queryKey: ["repository"] });
    qc.invalidateQueries({ queryKey: ["repositories"] });
    if (updated.full_path !== before.full_path) navigate(`/${updated.full_path}/settings`, { replace: true });
  };
}

function General({ repo, isOwner }: { repo: Repository; isOwner: boolean }) {
  const [name, setName] = useState(repo.name);
  const [description, setDescription] = useState(repo.description);
  const [isPrivate, setPrivate] = useState(repo.is_private);
  const [archived, setArchived] = useState(repo.archived);
  const changed = useRepoChanged();
  const save = useMutation({
    mutationFn: () => {
      const body: Record<string, unknown> = {};
      if (description !== repo.description) body.description = description;
      if (isOwner) {
        if (name !== repo.name) body.name = name;
        if (isPrivate !== repo.is_private) body.is_private = isPrivate;
        if (archived !== repo.archived) body.archived = archived;
      }
      return api<Repository>("PATCH", `/repository/${repo.id}`, body);
    },
    onSuccess: (updated) => changed(updated, repo),
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate();
  }

  return (
    <Section title="General" description={isOwner ? undefined : "Owners change the name, visibility and archiving."}>
      <form className="space-y-4" onSubmit={submit}>
        <div className="space-y-1.5">
          <Label htmlFor="repo-name">Name</Label>
          <Input id="repo-name" value={name} disabled={!isOwner} onChange={(e) => setName(e.target.value)} />
          {isOwner && <p className="text-xs text-muted-foreground">Renaming changes the URL; the old one keeps working until another repository takes it.</p>}
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="repo-description">Description</Label>
          <Textarea id="repo-description" value={description} onChange={(e) => setDescription(e.target.value)} />
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={isPrivate} disabled={!isOwner} onChange={(e) => setPrivate(e.target.checked)} /> Private
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={archived} disabled={!isOwner} onChange={(e) => setArchived(e.target.checked)} /> Archived (read-only: pushes are refused)
        </label>
        <ErrorText error={save.error} />
        {save.isSuccess && <p className="text-sm text-muted-foreground">Saved.</p>}
        <Button type="submit" disabled={save.isPending}>
          Save
        </Button>
      </form>
    </Section>
  );
}

function Members({ repo, isOwner }: { repo: Repository; isOwner: boolean }) {
  const qc = useQueryClient();
  const key = ["members", repo.id];
  const members = useQuery({
    queryKey: key,
    queryFn: async () => (await api<{ members: Member[] | null }>("GET", `/repository/${repo.id}/members`)).members ?? [],
  });
  const [kind, setKind] = useState<"user" | "group">("user");
  const [who, setWho] = useState("");
  const [role, setRole] = useState<Role>("developer");
  // Owners grant anything; maintainers up to developer; groups at most maintainer.
  const grantable = roles.filter((r) => (isOwner || atLeast("developer", r)) && (kind === "user" || r !== "owner"));

  const done = () => qc.invalidateQueries({ queryKey: key });
  const add = useMutation({
    mutationFn: () => api("POST", `/repository/${repo.id}/members`, kind === "user" ? { username: who, role } : { group: who, role }),
    onSuccess: () => {
      setWho("");
      done();
    },
  });
  const update = useMutation({
    mutationFn: (v: { id: string; role: Role }) => api("PATCH", `/repository/${repo.id}/members/${v.id}`, { role: v.role }),
    onSettled: done,
  });
  const remove = useMutation({
    mutationFn: (id: string) => api("DELETE", `/repository/${repo.id}/members/${id}`),
    onSettled: done,
  });

  return (
    <Section
      title="Members"
      description={
        repo.owner_type === "group"
          ? `Members of ${repo.namespace} also have their group role here. Add people or groups from outside it below.`
          : "People and groups with access to this repository."
      }
    >
      <table className="w-full text-sm">
        <tbody className="divide-y">
          {members.data?.map((m) => (
            <tr key={m.id}>
              <td className="py-2">{m.type === "user" ? m.username : <span>{m.group} <span className="text-muted-foreground">(group)</span></span>}</td>
              <td className="py-2">
                <Select
                  aria-label={`Role of ${m.username ?? m.group}`}
                  value={m.role}
                  onChange={(e) => update.mutate({ id: m.id, role: e.target.value as Role })}
                >
                  {roles.map((r) => (
                    <option key={r} value={r}>
                      {r}
                    </option>
                  ))}
                </Select>
              </td>
              <td className="py-2 text-right">
                <Button variant="ghost" size="icon" aria-label={`Remove ${m.username ?? m.group}`} onClick={() => remove.mutate(m.id)}>
                  <Trash2 className="h-4 w-4" />
                </Button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <ErrorText error={update.error ?? remove.error} />
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          add.mutate();
        }}
      >
        <Select aria-label="Member type" value={kind} onChange={(e) => setKind(e.target.value as "user" | "group")}>
          <option value="user">User</option>
          <option value="group">Group</option>
        </Select>
        <Input className="w-48" aria-label={kind === "user" ? "Username" : "Group name"} placeholder={kind === "user" ? "username" : "group"} value={who} onChange={(e) => setWho(e.target.value)} required />
        <Select aria-label="New member role" value={role} onChange={(e) => setRole(e.target.value as Role)}>
          {grantable.map((r) => (
            <option key={r} value={r}>
              {r}
            </option>
          ))}
        </Select>
        <Button type="submit" disabled={add.isPending}>
          Add
        </Button>
      </form>
      <ErrorText error={add.error} />
    </Section>
  );
}

function DeployKeys({ repo }: { repo: Repository }) {
  const qc = useQueryClient();
  const key = ["deploy-keys", repo.id];
  const keys = useQuery({
    queryKey: key,
    queryFn: async () => (await api<{ keys: SSHKey[] | null }>("GET", `/repository/${repo.id}/deploy-keys`)).keys ?? [],
  });
  const [name, setName] = useState("");
  const [pub, setPub] = useState("");
  const [write, setWrite] = useState(false);
  const add = useMutation({
    mutationFn: () => api("POST", `/repository/${repo.id}/deploy-keys`, { name, key: pub, read_only: !write }),
    onSuccess: () => {
      setName("");
      setPub("");
      setWrite(false);
      qc.invalidateQueries({ queryKey: key });
    },
  });
  const remove = useMutation({
    mutationFn: (id: string) => api("DELETE", `/repository/${repo.id}/deploy-keys/${id}`),
    onSettled: () => qc.invalidateQueries({ queryKey: key }),
  });

  return (
    <Section title="Deploy keys" description="SSH keys that can access only this repository, e.g. for CI. Read-only unless you allow writes.">
      {keys.data && keys.data.length > 0 && (
        <ul className="divide-y text-sm">
          {keys.data.map((k) => (
            <li key={k.id} className="flex items-center gap-3 py-2">
              <div className="min-w-0 flex-1">
                <p className="font-medium">
                  {k.name} <span className="text-xs text-muted-foreground">{k.read_only === false ? "read-write" : "read-only"}</span>
                </p>
                <p className="truncate font-mono text-xs text-muted-foreground">{k.fingerprint}</p>
              </div>
              <span className="text-xs text-muted-foreground">last used {formatDate(k.last_used_at)}</span>
              <Button variant="ghost" size="icon" aria-label={`Remove deploy key ${k.name}`} onClick={() => remove.mutate(k.id)}>
                <Trash2 className="h-4 w-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
      <form
        className="space-y-2"
        onSubmit={(e) => {
          e.preventDefault();
          add.mutate();
        }}
      >
        <Input aria-label="Deploy key name" placeholder="Name (optional)" value={name} onChange={(e) => setName(e.target.value)} />
        <Textarea aria-label="Deploy public key" placeholder="ssh-ed25519 AAAA…" value={pub} onChange={(e) => setPub(e.target.value)} required className="font-mono text-xs" />
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={write} onChange={(e) => setWrite(e.target.checked)} /> Allow pushes
        </label>
        <ErrorText error={add.error ?? remove.error} />
        <Button type="submit" disabled={add.isPending}>
          Add deploy key
        </Button>
      </form>
    </Section>
  );
}

function Transfer({ repo }: { repo: Repository }) {
  const { data: profile } = useProfile();
  const groups = useMyGroups();
  const targets = [profile?.username ?? "", ...(groups.data ?? []).filter((g) => g.role === "owner" || g.role === "maintainer").map((g) => g.name)].filter(
    (n) => n && n !== repo.namespace,
  );
  const [target, setTarget] = useState("");
  const changed = useRepoChanged();
  const move = useMutation({
    mutationFn: () => api<Repository>("POST", `/repository/${repo.id}/transfer`, { namespace: target || targets[0] }),
    onSuccess: (updated) => changed(updated, repo),
  });

  return (
    <Section title="Transfer" description="Move this repository to your namespace or a group you maintain. The old URL keeps working until another repository takes it.">
      {targets.length === 0 ? (
        <p className="text-sm text-muted-foreground">There is nowhere else you can move it.</p>
      ) : (
        <div className="flex gap-2">
          <Select aria-label="New owner" value={target || targets[0]} onChange={(e) => setTarget(e.target.value)}>
            {targets.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </Select>
          <Button variant="outline" disabled={move.isPending} onClick={() => move.mutate()}>
            Transfer
          </Button>
        </div>
      )}
      <ErrorText error={move.error} />
    </Section>
  );
}

function Delete({ repo }: { repo: Repository }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const del = useMutation({
    mutationFn: () => api("DELETE", `/repository/${repo.id}?confirm=${encodeURIComponent(repo.slug)}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["repositories"] });
      navigate("/", { replace: true });
    },
  });
  return (
    <Section title="Delete this repository" description="This deletes its git data and cannot be undone." danger>
      <ConfirmDelete word={repo.slug} action="Delete repository" pending={del.isPending} onConfirm={() => del.mutate()} />
      <ErrorText error={del.error} />
    </Section>
  );
}
