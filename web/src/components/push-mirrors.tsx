import { useState } from "react";
import { relativeTime } from "@/lib/browse";
import { isSSHURL, usePushMirrorActions, usePushMirrors, type PushMirror, type PushMirrorFields } from "@/lib/mirrors";
import type { Repository } from "@/lib/api";
import { CopyField } from "./copy-field";
import { ErrorText, Section } from "./section";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Select } from "./ui/select";

const modes = {
  mirror: "Mirror: force-push, and delete the remote's other branches and tags",
  branches: "Branches: never force or delete (diverged branches are reported)",
};

// PushMirrors: remotes this repository's branches and tags are pushed to
// after every push and on a schedule (maintainers and owners).
export function PushMirrors({ repo }: { repo: Repository }) {
  const list = usePushMirrors(repo.full_path);
  const { create } = usePushMirrorActions(repo.full_path);
  const [adding, setAdding] = useState(false);
  return (
    <section id="push-mirrors">
      <Section
        title="Push mirrors"
        description="Branches and tags are pushed to these remotes within seconds of every push, and on a schedule. Over SSH, add the mirror's public key on the remote with write access."
      >
        {list.data?.map((m) => <PushMirrorRow key={m.id} repo={repo} mirror={m} />)}
        {list.data?.length === 0 && !adding && <p className="text-sm text-muted-foreground">No push mirrors.</p>}
        {adding ? (
          <PushMirrorForm
            submitLabel="Add push mirror"
            pending={create.isPending}
            error={create.error}
            onCancel={() => setAdding(false)}
            onSubmit={(f) => create.mutate(f, { onSuccess: () => setAdding(false) })}
          />
        ) : (
          <Button variant="outline" onClick={() => setAdding(true)}>
            Add a push mirror
          </Button>
        )}
      </Section>
    </section>
  );
}

function PushMirrorRow({ repo, mirror: m }: { repo: Repository; mirror: PushMirror }) {
  const { update, sync, remove } = usePushMirrorActions(repo.full_path);
  const [editing, setEditing] = useState(false);
  const busy = m.status === "pending" || m.status === "syncing";
  return (
    <div className="space-y-2 rounded-md border p-3" data-testid="push-mirror">
      <div className="flex flex-wrap items-center gap-2">
        <code className="min-w-0 break-all text-sm">{m.url}</code>
        <Badge>{m.mode === "mirror" ? "mirror" : "branches"}</Badge>
        {m.lfs && <Badge>LFS</Badge>}
      </div>
      <p className="text-sm">
        Status: <strong>{m.status}</strong>
        {m.last_success_at && <> · last pushed {relativeTime(m.last_success_at)}</>}
      </p>
      {m.last_error && <p className="text-sm text-destructive">{m.last_error}</p>}
      {m.ssh_public_key && (
        <div className="space-y-1.5">
          <Label>Public key (add it on the remote with write access)</Label>
          <CopyField value={m.ssh_public_key} label="Push mirror SSH public key" />
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" size="sm" onClick={() => sync.mutate(m.id)} disabled={busy || sync.isPending}>
          {busy ? "Pushing…" : "Push now"}
        </Button>
        <Button variant="ghost" size="sm" onClick={() => setEditing(!editing)}>
          {editing ? "Close" : "Edit"}
        </Button>
        <Button variant="ghost" size="sm" onClick={() => confirm(`Remove the push mirror to ${m.url}? The remote keeps what it got.`) && remove.mutate(m.id)} disabled={remove.isPending}>
          Remove
        </Button>
      </div>
      <ErrorText error={sync.error ?? remove.error} />
      {editing && (
        <PushMirrorForm
          mirror={m}
          submitLabel="Save"
          pending={update.isPending}
          error={update.error}
          onCancel={() => setEditing(false)}
          onSubmit={(f) => update.mutate({ id: m.id, ...f }, { onSuccess: () => setEditing(false) })}
          onNewKey={() => confirm("Make a new key? The old one stops working.") && update.mutate({ id: m.id, regenerate_key: true })}
        />
      )}
    </div>
  );
}

function PushMirrorForm(props: {
  mirror?: PushMirror;
  submitLabel: string;
  pending: boolean;
  error: Error | null;
  onSubmit: (f: PushMirrorFields) => void;
  onCancel: () => void;
  onNewKey?: () => void;
}) {
  const m = props.mirror;
  const id = m ? m.id : "new";
  const [url, setURL] = useState(m?.url ?? "");
  const [username, setUsername] = useState(m?.username ?? "");
  const [password, setPassword] = useState("");
  const [mode, setMode] = useState<PushMirror["mode"]>(m?.mode ?? "mirror");
  const [hours, setHours] = useState(m ? m.interval_seconds / 3600 : 8);
  const [lfs, setLFS] = useState(m?.lfs ?? false);
  const ssh = isSSHURL(url);
  return (
    <form
      className="space-y-3"
      onSubmit={(e) => {
        e.preventDefault();
        props.onSubmit({
          url, mode, lfs, interval_seconds: Math.round(hours * 3600),
          ...(ssh ? {} : { username, ...(password ? { password } : {}) }),
        });
        setPassword("");
      }}
    >
      <div className="space-y-1.5">
        <Label htmlFor={`push-url-${id}`}>Remote URL</Label>
        <Input id={`push-url-${id}`} placeholder="https://github.com/owner/repo.git or git@host:owner/repo.git" value={url} onChange={(e) => setURL(e.target.value.trim())} required />
      </div>
      {ssh ? (
        m?.auth === "ssh" ? (
          props.onNewKey && (
            <Button type="button" variant="ghost" size="sm" onClick={props.onNewKey}>
              New key
            </Button>
          )
        ) : (
          <p className="text-xs text-muted-foreground">A key pair is made for this mirror: add its public key on the remote once saved.</p>
        )
      ) : (
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor={`push-user-${id}`}>Username</Label>
            <Input id={`push-user-${id}`} autoComplete="off" value={username} onChange={(e) => setUsername(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor={`push-password-${id}`}>Password or token</Label>
            <Input
              id={`push-password-${id}`}
              type="password"
              autoComplete="new-password"
              placeholder={m?.has_password ? "(unchanged)" : ""}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
        </div>
      )}
      <div className="space-y-1.5">
        <Label htmlFor={`push-mode-${id}`}>Mode</Label>
        <Select id={`push-mode-${id}`} className="w-full" value={mode} onChange={(e) => setMode(e.target.value as PushMirror["mode"])}>
          <option value="mirror">{modes.mirror}</option>
          <option value="branches">{modes.branches}</option>
        </Select>
      </div>
      <div className="flex items-center gap-2 text-sm">
        <Label htmlFor={`push-hours-${id}`}>Also push every</Label>
        <Input id={`push-hours-${id}`} type="number" min={0.25} step={0.25} className="w-24" value={hours} onChange={(e) => setHours(Number(e.target.value))} />
        hours
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={lfs} onChange={(e) => setLFS(e.target.checked)} /> Push Git LFS objects too
      </label>
      <div className="flex gap-2">
        <Button type="submit" disabled={props.pending}>
          {props.submitLabel}
        </Button>
        <Button type="button" variant="ghost" onClick={props.onCancel}>
          Cancel
        </Button>
      </div>
      <ErrorText error={props.error} />
    </form>
  );
}
