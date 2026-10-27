import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Folder, Trash2, Users } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { ConfirmDelete } from "@/components/confirm-delete";
import { ActionsSettings } from "@/components/actions-settings";
import { NamespacePath } from "@/components/namespace-path";
import { ErrorText, Section } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { api, ApiError, groupAPI, type Group, type GroupMember } from "@/lib/api";
import { useProfile } from "@/lib/auth";
import { useMyGroups } from "@/lib/queries";
import { NotFoundPage } from "./not-found";
import { Webhooks } from "./repo-webhooks";

export function GroupsPage() {
  const groups = useMyGroups();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const create = useMutation({
    mutationFn: () => api<Group>("POST", "/group", { name, description }),
    onSuccess: (g) => {
      qc.invalidateQueries({ queryKey: ["groups"] });
      navigate(`/groups/${g.name}`);
    },
  });

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <h1 className="text-xl font-semibold">Groups</h1>
      <p className="text-sm text-muted-foreground">
        A group owns repositories under its name (<code>/group/repo</code>); its members get their group role on all of them.
      </p>
      {groups.data && groups.data.length > 0 && (
        <Card className="divide-y">
          {groups.data.map((g) => (
            <div key={g.id} className="flex items-center gap-3 px-4 py-3">
              <Users className="h-4 w-4 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <Link to={`/${g.name}`} className="font-medium text-primary hover:underline">
                  {g.name}
                </Link>
                {g.description && <p className="truncate text-sm text-muted-foreground">{g.description}</p>}
              </div>
              <Badge>{g.role}</Badge>
              <Link to={`/groups/${g.name}`} className="text-sm text-primary hover:underline">
                Manage
              </Link>
            </div>
          ))}
        </Card>
      )}
      <Section title="Create a group" description="Lowercase letters, digits, '.', '_' or '-'. The name can't be taken by a user or another group.">
        <form
          className="flex flex-wrap gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <Input className="w-48" aria-label="Group name" placeholder="name" value={name} onChange={(e) => setName(e.target.value)} required />
          <Input className="min-w-48 flex-1" aria-label="Group description" placeholder="Description (optional)" value={description} onChange={(e) => setDescription(e.target.value)} />
          <Button type="submit" disabled={create.isPending}>
            Create
          </Button>
        </form>
        <ErrorText error={create.error} />
      </Section>
    </div>
  );
}

// GroupTwoFactor shows (and, for the owner, switches) the group's 2FA
// requirement.
function GroupTwoFactor({ name, group, isOwner }: { name: string; group: Group; isOwner: boolean }) {
  const qc = useQueryClient();
  const set = useMutation({
    mutationFn: (on: boolean) => api<Group>("PATCH", `/group/${encodeURIComponent(name)}`, { require_two_factor: on }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["group", name] });
      qc.invalidateQueries({ queryKey: ["group-members", name] });
    },
  });
  const inherited = !!group.require_two_factor_inherited;
  if (!isOwner && !group.require_two_factor && !inherited) return null;
  return (
    <Section
      title="Two-factor authentication"
      description="When required, members without two-factor authentication lose access to the group's repositories until they set it up. They stay members."
    >
      {inherited && <p className="text-sm">A parent group requires two-factor authentication, so this group does too.</p>}
      {isOwner ? (
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={!!group.require_two_factor} disabled={set.isPending} onChange={(e) => set.mutate(e.target.checked)} />
          Require two-factor authentication for members
        </label>
      ) : (
        <p className="text-sm">This group requires two-factor authentication.</p>
      )}
      <ErrorText error={set.error} />
    </Section>
  );
}

export function GroupPage() {
  const params = useParams();
  const name = [params.name, params["*"]].filter(Boolean).join("/"); // a sub-group's path has slashes
  const { data: profile } = useProfile();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const group = useQuery({ queryKey: ["group", name], queryFn: () => api<Group>("GET", `/group/${encodeURIComponent(name)}`), retry: false });
  const membersKey = ["group-members", name];
  const members = useQuery({
    queryKey: membersKey,
    queryFn: async () => (await api<{ members: GroupMember[] | null }>("GET", `/group/${encodeURIComponent(name)}/members`)).members ?? [],
    enabled: group.isSuccess,
  });
  const [username, setUsername] = useState("");
  const [role, setRole] = useState<"member" | "maintainer">("member");
  const [newOwner, setNewOwner] = useState("");

  const refresh = () => {
    qc.invalidateQueries({ queryKey: membersKey });
    qc.invalidateQueries({ queryKey: ["group", name] });
    qc.invalidateQueries({ queryKey: ["groups"] });
  };
  const base = `/group/${encodeURIComponent(name)}/members`;
  const add = useMutation({ mutationFn: () => api("POST", base, { username, role }), onSuccess: () => (setUsername(""), refresh()) });
  const update = useMutation({ mutationFn: (v: { username: string; role: string }) => api("PATCH", `${base}/${v.username}`, { role: v.role }), onSettled: refresh });
  const remove = useMutation({
    mutationFn: (u: string) => api("DELETE", `${base}/${u}`),
    onSuccess: (_, u) => (u === profile?.username ? navigate("/groups") : refresh()),
  });
  const transfer = useMutation({ mutationFn: () => api("POST", `/group/${encodeURIComponent(name)}/transfer`, { username: newOwner }), onSuccess: refresh });
  const del = useMutation({
    mutationFn: () => api("DELETE", `/group/${encodeURIComponent(name)}`),
    onSuccess: () => (qc.invalidateQueries({ queryKey: ["groups"] }), navigate("/groups")),
  });

  if (group.error instanceof ApiError && group.error.status === 404) return <NotFoundPage />;
  if (group.isError) return <Alert variant="destructive">{group.error.message}</Alert>;
  if (!group.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const myRole = group.data.role;
  const canManage = myRole === "owner" || myRole === "maintainer";
  const isOwner = myRole === "owner";

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <div>
        <h1 className="text-xl font-semibold">
          <NamespacePath path={name} /> <Badge>{myRole}</Badge>
        </h1>
        {group.data.description && <p className="text-muted-foreground">{group.data.description}</p>}
      </div>

      <GroupTwoFactor name={name} group={group.data} isOwner={isOwner} />
      {isOwner && (
        <ActionsSettings
          scope="the group's repositories'"
          paths={{
            runners: `/group/${encodeURIComponent(name)}/runners`,
            secrets: `/group/${encodeURIComponent(name)}/actions/secrets`,
            variables: `/group/${encodeURIComponent(name)}/actions/variables`,
          }}
        />
      )}

      <Section
        title="Members"
        description="Owner: everything. Maintainers: add and remove members, create repositories and sub-groups. Members: developers on the group's repositories. Members of a parent group are members here too; adding them here can only raise their role."
      >
        <table className="w-full text-sm">
          <tbody className="divide-y">
            {members.data?.map((m) => (
              <tr key={m.username}>
                <td className="py-2">
                  {m.username}
                  {m.two_factor_enabled === false && <Badge className="ml-2 border-amber-500 text-amber-700 dark:text-amber-400">no 2FA</Badge>}
                  {m.inherited_from && <span className="ml-2 text-xs text-muted-foreground">from {m.inherited_from}</span>}
                </td>
                <td className="py-2">
                  {isOwner && m.role !== "owner" && !m.inherited_from ? (
                    <Select aria-label={`Role of ${m.username}`} value={m.role} onChange={(e) => update.mutate({ username: m.username, role: e.target.value })}>
                      <option value="member">member</option>
                      <option value="maintainer">maintainer</option>
                    </Select>
                  ) : (
                    <Badge>{m.role}</Badge>
                  )}
                </td>
                <td className="py-2 text-right">
                  {m.role !== "owner" && !m.inherited_from && (canManage || m.username === profile?.username) && (
                    <Button variant="ghost" size="sm" aria-label={`Remove ${m.username}`} onClick={() => remove.mutate(m.username)}>
                      {m.username === profile?.username ? "Leave" : <Trash2 className="h-4 w-4" />}
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <ErrorText error={update.error ?? remove.error} />
        {canManage && (
          <form
            className="flex flex-wrap gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              add.mutate();
            }}
          >
            <Input className="w-48" aria-label="Username to add" placeholder="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
            <Select aria-label="Role for new member" value={role} onChange={(e) => setRole(e.target.value as "member" | "maintainer")}>
              <option value="member">member</option>
              {isOwner && <option value="maintainer">maintainer</option>}
            </Select>
            <Button type="submit" disabled={add.isPending}>
              Add
            </Button>
          </form>
        )}
        <ErrorText error={add.error} />
      </Section>

      <Subgroups name={name} canCreate={canManage} />
      {canManage && <Webhooks owner={groupAPI(name)} group />}
      {isOwner && <MoveGroup group={group.data} />}

      {isOwner && (
        <Section title="Transfer ownership" description="The new owner must already be a member; you become a maintainer.">
          <div className="flex gap-2">
            <Select aria-label="New group owner" value={newOwner} onChange={(e) => setNewOwner(e.target.value)}>
              <option value="">Choose a member</option>
              {members.data
                ?.filter((m) => m.role !== "owner" && !m.inherited_from)
                .map((m) => (
                  <option key={m.username} value={m.username}>
                    {m.username}
                  </option>
                ))}
            </Select>
            <Button variant="outline" disabled={!newOwner || transfer.isPending} onClick={() => transfer.mutate()}>
              Transfer
            </Button>
          </div>
          <ErrorText error={transfer.error} />
        </Section>
      )}

      {isOwner && (
        <Section title="Delete this group" description="Only possible once its repositories and sub-groups are moved or deleted." danger>
          <ConfirmDelete word={name} action="Delete group" pending={del.isPending} onConfirm={() => del.mutate()} />
          <ErrorText error={del.error} />
        </Section>
      )}
    </div>
  );
}

// Subgroups lists a group's sub-groups and, for its maintainers and owners,
// creates one.
function Subgroups({ name, canCreate }: { name: string; canCreate: boolean }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const list = useQuery({
    queryKey: ["group-subgroups", name],
    queryFn: async () => (await api<{ groups: Group[] }>("GET", `${groupAPI(name)}/subgroups`)).groups,
  });
  const [sub, setSub] = useState("");
  const create = useMutation({
    mutationFn: () => api<Group>("POST", "/group", { name: sub, parent: name }),
    onSuccess: (g) => {
      qc.invalidateQueries({ queryKey: ["groups"] });
      navigate(`/groups/${g.name}`);
    },
  });
  if (!canCreate && !list.data?.length) return null;
  return (
    <section id="subgroups">
      <Section title="Sub-groups" description="Groups inside this one (up to 5 levels): their repositories are org/team/repo, and this group's members are members of them.">
        {list.data && list.data.length > 0 && (
          <ul className="divide-y rounded-md border text-sm">
            {list.data.map((g) => (
              <li key={g.id} className="flex items-center gap-2 px-3 py-2">
                <Folder className="h-4 w-4 text-muted-foreground" />
                <Link to={`/${g.name}`} className="flex-1 text-primary hover:underline">
                  {g.name.slice(name.length + 1)}
                </Link>
                <Link to={`/groups/${g.name}`} className="text-xs text-primary hover:underline">
                  Manage
                </Link>
              </li>
            ))}
          </ul>
        )}
        {canCreate && (
          <form
            className="flex flex-wrap gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              create.mutate();
            }}
          >
            <Input className="w-48" aria-label="Sub-group name" placeholder="name" value={sub} onChange={(e) => setSub(e.target.value)} required />
            <Button type="submit" disabled={create.isPending}>
              Create sub-group
            </Button>
          </form>
        )}
        <ErrorText error={create.error ?? list.error} />
      </Section>
    </section>
  );
}

// MoveGroup renames the group or moves it under another group (its owner).
function MoveGroup({ group }: { group: Group }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const segment = group.name.slice(group.name.lastIndexOf("/") + 1);
  const [parent, setParent] = useState(group.parent ?? "");
  const [newName, setNewName] = useState(segment);
  const move = useMutation({
    mutationFn: () => api<Group>("POST", `${groupAPI(group.name)}/move`, { parent, name: newName }),
    onSuccess: (g) => {
      qc.invalidateQueries();
      navigate(`/groups/${g.name}`);
    },
  });
  const target = (parent ? parent + "/" : "") + newName;
  return (
    <Section
      title="Move or rename"
      description="Everything inside moves along; the old paths (of the group, its sub-groups and their repositories, git URLs included) keep working as redirects. Top-level groups can move under a group but not be renamed."
    >
      <form
        className="space-y-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (confirm(`Move ${group.name} to ${target}?`)) move.mutate();
        }}
      >
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <Input className="w-56" aria-label="Parent group" placeholder="parent group (empty: top level)" value={parent} onChange={(e) => setParent(e.target.value.trim())} />
          <span className="text-muted-foreground">/</span>
          <Input className="w-40" aria-label="New name" value={newName} onChange={(e) => setNewName(e.target.value.trim())} required />
          <Button type="submit" variant="outline" disabled={move.isPending || target === group.name}>
            Move
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">New path: {target}</p>
      </form>
      <ErrorText error={move.error} />
    </Section>
  );
}
