import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2, Users } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { ConfirmDelete } from "@/components/confirm-delete";
import { ActionsSettings } from "@/components/actions-settings";
import { ErrorText, Section } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { api, ApiError, type Group, type GroupMember } from "@/lib/api";
import { useProfile } from "@/lib/auth";
import { useMyGroups } from "@/lib/queries";
import { NotFoundPage } from "./not-found";

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
  if (!isOwner && !group.require_two_factor) return null;
  return (
    <Section
      title="Two-factor authentication"
      description="When required, members without two-factor authentication lose access to the group's repositories until they set it up. They stay members."
    >
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
  const { name = "" } = useParams();
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
          <Link to={`/${name}`} className="text-primary hover:underline">
            {name}
          </Link>{" "}
          <Badge>{myRole}</Badge>
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

      <Section title="Members" description="Owner: everything. Maintainers: add and remove members, create repositories. Members: developers on the group's repositories.">
        <table className="w-full text-sm">
          <tbody className="divide-y">
            {members.data?.map((m) => (
              <tr key={m.username}>
                <td className="py-2">
                  {m.username}
                  {m.two_factor_enabled === false && <Badge className="ml-2 border-amber-500 text-amber-700 dark:text-amber-400">no 2FA</Badge>}
                </td>
                <td className="py-2">
                  {isOwner && m.role !== "owner" ? (
                    <Select aria-label={`Role of ${m.username}`} value={m.role} onChange={(e) => update.mutate({ username: m.username, role: e.target.value })}>
                      <option value="member">member</option>
                      <option value="maintainer">maintainer</option>
                    </Select>
                  ) : (
                    <Badge>{m.role}</Badge>
                  )}
                </td>
                <td className="py-2 text-right">
                  {m.role !== "owner" && (canManage || m.username === profile?.username) && (
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

      {isOwner && (
        <Section title="Transfer ownership" description="The new owner must already be a member; you become a maintainer.">
          <div className="flex gap-2">
            <Select aria-label="New group owner" value={newOwner} onChange={(e) => setNewOwner(e.target.value)}>
              <option value="">Choose a member</option>
              {members.data
                ?.filter((m) => m.role !== "owner")
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
        <Section title="Delete this group" description="Only possible once its repositories are moved or deleted." danger>
          <ConfirmDelete word={name} action="Delete group" pending={del.isPending} onConfirm={() => del.mutate()} />
          <ErrorText error={del.error} />
        </Section>
      )}
    </div>
  );
}
