import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { ConfirmDelete } from "@/components/confirm-delete";
import { Pager } from "@/components/pager";
import { ErrorText, Section } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, repoId, type AdminUser } from "@/lib/api";
import { useProfile } from "@/lib/auth";
import { formatDate } from "@/lib/queries";

type AdminRepo = { id: string; full_path: string; name: string; is_private: boolean; created_at: string };

export function AdminPage() {
  const { data: profile } = useProfile();
  if (profile?.role !== "admin") return <Alert variant="destructive">Site administrators only.</Alert>;
  return (
    <div className="space-y-6">
      <h1 className="text-xl font-semibold">Administration</h1>
      <p className="text-sm text-muted-foreground">Admins manage accounts and can recover repositories and groups. They don't see private repositories unless they make themselves an owner.</p>
      <Users />
      <Repositories />
      <GroupTransfer />
    </div>
  );
}

function Users() {
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const [page, setPage] = useState(0);
  const perPage = 50;
  const users = useQuery({
    queryKey: ["admin-users", q, page],
    queryFn: () => api<{ users: AdminUser[]; total: number }>("GET", `/admin/users?limit=${perPage}&offset=${page * perPage}&q=${encodeURIComponent(q)}`),
    placeholderData: keepPreviousData,
  });
  const refresh = () => qc.invalidateQueries({ queryKey: ["admin-users"] });
  const update = useMutation({
    mutationFn: (v: { username: string; body: Record<string, unknown> }) => api("PATCH", `/admin/users/${encodeURIComponent(v.username)}`, v.body),
    onSettled: refresh,
  });
  const [deleting, setDeleting] = useState<string | null>(null);
  const del = useMutation({
    mutationFn: (username: string) => api("DELETE", `/admin/users/${encodeURIComponent(username)}`),
    onSuccess: () => (setDeleting(null), refresh()),
  });
  const [form, setForm] = useState({ username: "", email: "", password: "" });
  const create = useMutation({
    mutationFn: () => api("POST", "/admin/users", form),
    onSuccess: () => (setForm({ username: "", email: "", password: "" }), refresh()),
  });

  return (
    <Section title="Users" description="Disabling an account signs it out everywhere. A user who still owns repositories or groups can't be deleted until they are handed over.">
      <Input aria-label="Search users" placeholder="Search by username or email" value={q} onChange={(e) => (setQ(e.target.value), setPage(0))} />
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="text-left text-xs text-muted-foreground">
            <tr>
              <th className="py-2">User</th>
              <th>Status</th>
              <th>Joined</th>
              <th />
            </tr>
          </thead>
          <tbody className="divide-y">
            {users.data?.users.map((u) => (
              <tr key={u.id}>
                <td className="py-2">
                  <Link to={`/${u.username}`} className="font-medium hover:underline">
                    {u.username}
                  </Link>
                  <p className="text-xs text-muted-foreground">{u.email}</p>
                </td>
                <td className="space-x-1">
                  {u.role === "admin" && <Badge>admin</Badge>}
                  {!u.active && <Badge>disabled</Badge>}
                  {!u.email_verified_at && <Badge>unverified</Badge>}
                </td>
                <td className="text-xs text-muted-foreground">{formatDate(u.created_at)}</td>
                <td className="space-x-1 py-2 text-right whitespace-nowrap">
                  {!u.email_verified_at && (
                    <Button size="sm" variant="ghost" onClick={() => update.mutate({ username: u.username, body: { email_verified: true } })}>
                      Verify
                    </Button>
                  )}
                  <Button size="sm" variant="ghost" onClick={() => update.mutate({ username: u.username, body: { active: !u.active } })}>
                    {u.active ? "Disable" : "Enable"}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => update.mutate({ username: u.username, body: { role: u.role === "admin" ? "user" : "admin" } })}>
                    {u.role === "admin" ? "Remove admin" : "Make admin"}
                  </Button>
                  <Button size="sm" variant="ghost" className="text-destructive" onClick={() => setDeleting(u.username)}>
                    Delete
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {users.data && <Pager page={page} perPage={perPage} count={users.data.users.length} total={users.data.total} onPage={setPage} />}
      <ErrorText error={update.error} />
      {deleting && (
        <div className="space-y-2 rounded-md border border-destructive/40 p-3">
          <p className="text-sm">Delete {deleting} and their sessions, tokens and memberships?</p>
          <ConfirmDelete word={deleting} action="Delete user" pending={del.isPending} onConfirm={() => del.mutate(deleting)} />
          <ErrorText error={del.error} />
          <Button variant="ghost" size="sm" onClick={() => setDeleting(null)}>
            Cancel
          </Button>
        </div>
      )}
      <form
        className="grid gap-2 border-t pt-4 sm:grid-cols-[1fr_1fr_1fr_auto]"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <Input aria-label="New username" placeholder="username" value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} required />
        <Input aria-label="New user email" type="email" placeholder="email" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} required />
        <Input aria-label="Initial password" type="password" autoComplete="new-password" placeholder="initial password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} required />
        <Button type="submit" disabled={create.isPending}>
          Create user
        </Button>
      </form>
      <ErrorText error={create.error} />
    </Section>
  );
}

function Repositories() {
  const qc = useQueryClient();
  const [page, setPage] = useState(0);
  const perPage = 50;
  const repos = useQuery({
    queryKey: ["admin-repos", page],
    queryFn: () => api<{ repositories: AdminRepo[]; total: number }>("GET", `/admin/repositories?limit=${perPage}&offset=${page * perPage}`),
    placeholderData: keepPreviousData,
  });
  const [selected, setSelected] = useState<AdminRepo | null>(null);
  const [target, setTarget] = useState("");
  const done = () => {
    setTarget("");
    qc.invalidateQueries({ queryKey: ["admin-repos"] });
  };
  const owner = useMutation({ mutationFn: (r: AdminRepo) => api("POST", `/admin/repositories/${repoId(r.full_path)}/owner`, { username: target }), onSuccess: done });
  const move = useMutation({
    mutationFn: (r: AdminRepo) => api<AdminRepo>("POST", `/admin/repositories/${repoId(r.full_path)}/transfer`, { namespace: target }),
    onSuccess: (r) => (setSelected(r), done()),
  });
  const del = useMutation({
    mutationFn: (r: AdminRepo) => api("DELETE", `/admin/repositories/${repoId(r.full_path)}?confirm=${encodeURIComponent(r.full_path.split("/")[1])}`),
    onSuccess: () => (setSelected(null), done()),
  });

  return (
    <Section title="Repositories" description={repos.data ? `${repos.data.total} in total.` : undefined}>
      <ul className="divide-y text-sm">
        {repos.data?.repositories.map((r) => (
          <li key={r.id}>
            <button className={`flex w-full items-center gap-2 px-2 py-2 text-left hover:bg-muted ${selected?.id === r.id ? "bg-muted" : ""}`} onClick={() => setSelected(r)}>
              <span className="flex-1 font-mono text-xs">{r.full_path}</span>
              {r.is_private && <Badge>private</Badge>}
              <span className="text-xs text-muted-foreground">{formatDate(r.created_at)}</span>
            </button>
          </li>
        ))}
      </ul>
      {repos.data && <Pager page={page} perPage={perPage} count={repos.data.repositories.length} total={repos.data.total} onPage={setPage} />}
      {selected && (
        <div className="space-y-3 rounded-md border p-3">
          <p className="text-sm font-medium">{selected.full_path}</p>
          <div className="flex flex-wrap gap-2">
            <Input className="w-56" aria-label="Username or group" placeholder="username or group" value={target} onChange={(e) => setTarget(e.target.value)} />
            <Button variant="outline" disabled={!target || move.isPending} onClick={() => move.mutate(selected)}>
              Move to this namespace
            </Button>
            <Button variant="outline" disabled={!target || owner.isPending} onClick={() => owner.mutate(selected)}>
              Make this user an owner
            </Button>
          </div>
          <ErrorText error={move.error ?? owner.error} />
          {owner.isSuccess && <p className="text-sm text-muted-foreground">Owner added.</p>}
          <ConfirmDelete word={selected.full_path.split("/")[1]} action="Delete repository" pending={del.isPending} onConfirm={() => del.mutate(selected)} />
          <ErrorText error={del.error} />
        </div>
      )}
    </Section>
  );
}

function GroupTransfer() {
  const [group, setGroup] = useState("");
  const [username, setUsername] = useState("");
  const transfer = useMutation({ mutationFn: () => api("POST", `/admin/groups/${encodeURIComponent(group)}/transfer`, { username }) });
  return (
    <Section title="Group ownership" description="Make a user the owner of a group, e.g. when its owner left. The previous owner becomes a maintainer.">
      <form
        className="flex flex-wrap gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          transfer.mutate();
        }}
      >
        <Input className="w-48" aria-label="Group" placeholder="group" value={group} onChange={(e) => setGroup(e.target.value)} required />
        <Input className="w-48" aria-label="New owner" placeholder="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
        <Button type="submit" variant="outline" disabled={transfer.isPending}>
          Transfer
        </Button>
      </form>
      <ErrorText error={transfer.error} />
      {transfer.isSuccess && <p className="text-sm text-muted-foreground">Done.</p>}
    </Section>
  );
}
