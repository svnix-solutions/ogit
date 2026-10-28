import { useState } from "react";
import { relativeTime } from "@/lib/browse";
import { forgeNames, importCounts, isSSHURL, useAPIImport, useAPIImportActions, useMirror, useMirrorActions, type Mirror } from "@/lib/mirrors";
import type { Repository } from "@/lib/api";
import { CopyField } from "./copy-field";
import { ErrorText, Section } from "./section";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

// MirrorSettings: a mirror's (or running import's) remote, credentials,
// SSH key, schedule and status; nothing for other repositories.
export function MirrorSettings({ repo, isOwner }: { repo: Repository; isOwner: boolean }) {
  const m = useMirror(repo.full_path);
  if (!m.data) return null;
  return <MirrorForm key={m.data.url + m.data.ssh_public_key} repo={repo} mirror={m.data} isOwner={isOwner} />;
}

function MirrorForm({ repo, mirror: m, isOwner }: { repo: Repository; mirror: Mirror; isOwner: boolean }) {
  const { update, sync, stop } = useMirrorActions(repo.full_path);
  const [url, setURL] = useState(m.url);
  const [username, setUsername] = useState(m.username ?? "");
  const [password, setPassword] = useState("");
  const [hours, setHours] = useState(m.interval_seconds / 3600);
  const [lfs, setLFS] = useState(m.lfs);
  const ssh = isSSHURL(url);
  const busy = m.status === "pending" || m.status === "syncing";
  return (
    <section id="mirror">
      <Section
        title={m.mirror ? "Mirror" : "Import"}
        description={
          m.mirror
            ? "This repository follows its remote: branches and tags are synced (deleted ones too), and pushes are refused until you stop mirroring."
            : "This repository is being imported from its remote."
        }
      >
        <p className="text-sm">
          Status: <strong>{m.status}</strong>
          {m.last_success_at && <> · last synced {relativeTime(m.last_success_at)}</>}
          {m.mirror && !busy && <> · next sync {relativeTime(m.next_sync_at)}</>}
        </p>
        {m.last_error && <p className="text-sm text-destructive">{m.last_error}</p>}
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => sync.mutate()} disabled={busy || sync.isPending}>
            {busy ? "Syncing…" : "Sync now"}
          </Button>
        </div>
        <ErrorText error={sync.error} />

        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            update.mutate({
              url, lfs, interval_seconds: Math.round(hours * 3600),
              ...(ssh ? {} : { username, ...(password ? { password } : {}) }),
            });
            setPassword("");
          }}
        >
          <div className="space-y-1.5">
            <Label htmlFor="mirror-url">Remote URL</Label>
            <Input id="mirror-url" value={url} onChange={(e) => setURL(e.target.value.trim())} required />
          </div>
          {ssh ? (
            m.ssh_public_key && (
              <div className="space-y-1.5">
                <Label>Public key (add it on the remote as a deploy key)</Label>
                <CopyField value={m.ssh_public_key} label="Mirror SSH public key" />
                {m.ssh_host_key && <p className="text-xs text-muted-foreground">Remote host key (pinned): {m.ssh_host_key.split(" ").slice(0, 2).join(" ").slice(0, 60)}…</p>}
                <Button type="button" variant="ghost" size="sm" onClick={() => confirm("Make a new key? The old one stops working.") && update.mutate({ regenerate_key: true })}>
                  New key
                </Button>
              </div>
            )
          ) : (
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label htmlFor="mirror-user">Username</Label>
                <Input id="mirror-user" autoComplete="off" value={username} onChange={(e) => setUsername(e.target.value)} />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="mirror-password">Password or token</Label>
                <Input id="mirror-password" type="password" autoComplete="new-password" placeholder={m.has_password ? "(unchanged)" : ""} value={password} onChange={(e) => setPassword(e.target.value)} />
              </div>
            </div>
          )}
          {m.mirror && (
            <div className="flex items-center gap-2 text-sm">
              <Label htmlFor="mirror-hours">Sync every</Label>
              <Input id="mirror-hours" type="number" min={0.25} step={0.25} className="w-24" value={hours} onChange={(e) => setHours(Number(e.target.value))} />
              hours
            </div>
          )}
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={lfs} onChange={(e) => setLFS(e.target.checked)} /> Fetch Git LFS objects too
          </label>
          <Button type="submit" disabled={update.isPending}>
            Save
          </Button>
          <ErrorText error={update.error} />
        </form>

        {isOwner && (
          <div className="border-t pt-3">
            <Button variant="outline" onClick={() => confirm("Stop mirroring? The repository becomes a normal one and the remote is forgotten.") && stop.mutate()} disabled={stop.isPending}>
              {m.mirror ? "Stop mirroring" : "Cancel the import"}
            </Button>
            <ErrorText error={stop.error} />
          </div>
        )}
      </Section>
    </section>
  );
}

// APIImportSettings: an import of issues, pull requests and releases: its
// outcome, warnings (skipped assets) and, for owners, cancelling it.
export function APIImportSettings({ repo, isOwner }: { repo: Repository; isOwner: boolean }) {
  const i = useAPIImport(repo.full_path);
  const { retry, cancel } = useAPIImportActions(repo.full_path);
  if (!i.data) return null;
  const d = i.data;
  const from = forgeNames[d.forge] ?? d.forge;
  return (
    <section id="import">
      <Section title="Import of issues and pull requests" description={`From ${from} (${d.project}).`}>
        <p className="text-sm">
          Status: <strong>{d.status}</strong> · {importCounts(d)}
          {d.finished_at && <> · finished {relativeTime(d.finished_at)}</>}
        </p>
        {d.last_error && d.status !== "done" && <p className="text-sm text-destructive">{d.last_error}</p>}
        {d.warnings.length > 0 && (
          <ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground">
            {d.warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        )}
        <div className="flex gap-2">
          {d.status === "failed" && (
            <Button variant="outline" onClick={() => retry.mutate()} disabled={retry.isPending}>
              Resume
            </Button>
          )}
          {isOwner && d.status !== "done" && (
            <Button variant="outline" onClick={() => confirm("Cancel the import? What was imported stays.") && cancel.mutate()} disabled={cancel.isPending}>
              Cancel the import
            </Button>
          )}
        </div>
        <ErrorText error={retry.error ?? cancel.error} />
      </Section>
    </section>
  );
}
