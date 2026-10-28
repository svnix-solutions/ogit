import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Server, Trash2 } from "lucide-react";
import { useState } from "react";
import { CopyField } from "@/components/copy-field";
import { ErrorText, Section } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import { formatSize } from "@/lib/browse";
import { formatDate } from "@/lib/queries";

// Runners, secrets and variables of a scope: a repository, a group or the
// site. The API paths differ per scope; the pages are the same.

export type ActionsPaths = {
  runners: string; // GET list, POST <runners>/registration-token, DELETE <runners>/<id>
  secrets: string; // GET list, PUT/DELETE <secrets>/<NAME>
  variables: string;
};

type Runner = { id: string; name: string; scope: string; labels: string[]; version: string; online: boolean; last_seen_at: string | null };
type Secret = { name: string; updated_at: string };
type Variable = { name: string; value: string; updated_at: string };

export function ActionsSettings({ paths, scope }: { paths: ActionsPaths; scope: string }) {
  return (
    <>
      <Runners path={paths.runners} scope={scope} />
      <Secrets path={paths.secrets} scope={scope} />
      <Variables path={paths.variables} scope={scope} />
    </>
  );
}

function Runners({ path, scope }: { path: string; scope: string }) {
  const qc = useQueryClient();
  const key = ["runners", path];
  const runners = useQuery({ queryKey: key, queryFn: async () => (await api<{ runners: Runner[] }>("GET", path)).runners });
  const [token, setToken] = useState<{ token: string; expires_at: string } | null>(null);
  const create = useMutation({
    mutationFn: () => api<{ token: string; expires_at: string }>("POST", `${path}/registration-token`, {}),
    onSuccess: setToken,
  });
  const remove = useMutation({
    mutationFn: (id: string) => api("DELETE", `${path}/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  const url = window.location.origin;
  return (
    <Section
      title="Runners"
      description={
        <>
          Machines that run {scope} workflows in Docker with <code>ogit runner</code>. A run goes to a runner with every runs-on label it needs.
        </>
      }
    >
      <ErrorText error={runners.error ?? remove.error} />
      {runners.data?.length === 0 && <p className="text-sm text-muted-foreground">No runners yet.</p>}
      {runners.data && runners.data.length > 0 && (
        <ul className="divide-y text-sm">
          {runners.data.map((r) => (
            <li key={r.id} className="flex flex-wrap items-center gap-3 py-2">
              <Server className="h-4 w-4 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <p className="font-medium">
                  {r.name}{" "}
                  <span className={r.online ? "text-green-600" : "text-muted-foreground"} aria-label={r.online ? "online" : "offline"}>
                    ● {r.online ? "online" : "offline"}
                  </span>
                </p>
                <p className="flex flex-wrap gap-1 pt-0.5">
                  {r.labels.map((l) => (
                    <Badge key={l}>{l}</Badge>
                  ))}
                </p>
              </div>
              <span className="text-xs text-muted-foreground">
                {r.version && `${r.version} · `}last seen {formatDate(r.last_seen_at)}
              </span>
              <Button
                variant="ghost"
                size="icon"
                aria-label={`Delete runner ${r.name}`}
                onClick={() => {
                  if (window.confirm(`Delete ${r.name}? Its token stops working.`)) remove.mutate(r.id);
                }}
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
      {token && (
        <Alert>
          <p className="mb-2 text-sm font-medium">Register a runner within the hour (the token works once):</p>
          <CopyField value={`ogit runner register --url ${url} --token ${token.token}`} label="Registration command" />
          <p className="mt-2 text-xs text-muted-foreground">
            Then start it with <code>ogit runner run</code> on a machine with Docker. Labels map runs-on names to images:{" "}
            <code>--labels ubuntu-latest:docker://node:20-bookworm</code> (the default).
          </p>
        </Alert>
      )}
      <ErrorText error={create.error} />
      <Button variant="outline" onClick={() => create.mutate()} disabled={create.isPending}>
        New registration token
      </Button>
    </Section>
  );
}

function NameValueForm({ label, secret, onSave, pending, error }: { label: string; secret: boolean; onSave: (name: string, value: string, reset: () => void) => void; pending: boolean; error: unknown }) {
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const id = label.toLowerCase().replace(/\s+/g, "-");
  return (
    <form
      className="space-y-2"
      onSubmit={(e) => {
        e.preventDefault();
        onSave(name, value, () => {
          setName("");
          setValue("");
        });
      }}
    >
      <div className="space-y-1">
        <Label htmlFor={`${id}-name`}>Name</Label>
        <Input id={`${id}-name`} value={name} onChange={(e) => setName(e.target.value)} placeholder="DEPLOY_KEY" required />
      </div>
      <div className="space-y-1">
        <Label htmlFor={`${id}-value`}>Value</Label>
        <Textarea id={`${id}-value`} value={value} onChange={(e) => setValue(e.target.value)} autoComplete="off" spellCheck={false} className={secret ? "font-mono" : undefined} />
      </div>
      <ErrorText error={error} />
      <Button type="submit" disabled={!name.trim() || pending}>
        {label}
      </Button>
    </form>
  );
}

function Secrets({ path, scope }: { path: string; scope: string }) {
  const qc = useQueryClient();
  const key = ["secrets", path];
  const secrets = useQuery({ queryKey: key, queryFn: async () => (await api<{ secrets: Secret[] }>("GET", path)).secrets });
  const save = useMutation({
    mutationFn: ({ name, value }: { name: string; value: string; reset: () => void }) => api("PUT", `${path}/${encodeURIComponent(name)}`, { value }),
    onSuccess: (_, v) => {
      v.reset();
      qc.invalidateQueries({ queryKey: key });
    },
  });
  const remove = useMutation({
    mutationFn: (name: string) => api("DELETE", `${path}/${encodeURIComponent(name)}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  return (
    <Section title="Secrets" description={`Encrypted, and never shown again: \${{ secrets.NAME }} in ${scope} workflows. Runs from forks don't get them.`}>
      <ErrorText error={secrets.error ?? remove.error} />
      {secrets.data && secrets.data.length > 0 && (
        <ul className="divide-y text-sm">
          {secrets.data.map((s) => (
            <li key={s.name} className="flex items-center gap-3 py-2">
              <code className="flex-1 font-medium">{s.name}</code>
              <span className="text-xs text-muted-foreground">updated {formatDate(s.updated_at)}</span>
              <Button variant="ghost" size="icon" aria-label={`Delete secret ${s.name}`} onClick={() => window.confirm(`Delete ${s.name}?`) && remove.mutate(s.name)}>
                <Trash2 className="h-4 w-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
      <NameValueForm label="Save secret" secret pending={save.isPending} error={save.error} onSave={(name, value, reset) => save.mutate({ name, value, reset })} />
    </Section>
  );
}

function Variables({ path, scope }: { path: string; scope: string }) {
  const qc = useQueryClient();
  const key = ["variables", path];
  const vars = useQuery({ queryKey: key, queryFn: async () => (await api<{ variables: Variable[] }>("GET", path)).variables });
  const save = useMutation({
    mutationFn: ({ name, value }: { name: string; value: string; reset: () => void }) => api("PUT", `${path}/${encodeURIComponent(name)}`, { value }),
    onSuccess: (_, v) => {
      v.reset();
      qc.invalidateQueries({ queryKey: key });
    },
  });
  const remove = useMutation({
    mutationFn: (name: string) => api("DELETE", `${path}/${encodeURIComponent(name)}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  return (
    <Section title="Variables" description={`Plain values: \${{ vars.NAME }} in ${scope} workflows.`}>
      <ErrorText error={vars.error ?? remove.error} />
      {vars.data && vars.data.length > 0 && (
        <ul className="divide-y text-sm">
          {vars.data.map((v) => (
            <li key={v.name} className="flex items-center gap-3 py-2">
              <code className="font-medium">{v.name}</code>
              <span className="min-w-0 flex-1 truncate text-muted-foreground">{v.value}</span>
              <Button variant="ghost" size="icon" aria-label={`Delete variable ${v.name}`} onClick={() => window.confirm(`Delete ${v.name}?`) && remove.mutate(v.name)}>
                <Trash2 className="h-4 w-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
      <NameValueForm label="Save variable" secret={false} pending={save.isPending} error={save.error} onSave={(name, value, reset) => save.mutate({ name, value, reset })} />
    </Section>
  );
}

type CacheEntry = { id: number; key: string; ref: string; size: number; created_at: string; last_used_at: string };

// RepoCaches lists the repository's actions/cache entries, with their use
// of the limit (maintainers).
export function RepoCaches({ path }: { path: string }) {
  const qc = useQueryClient();
  const key = ["caches", path];
  const caches = useQuery({ queryKey: key, queryFn: () => api<{ caches: CacheEntry[]; total_size: number; limit: number }>("GET", path) });
  const remove = useMutation({
    mutationFn: (id: number) => api("DELETE", `${path}/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  const used = caches.data ? Math.min(100, (caches.data.total_size / caches.data.limit) * 100) : 0;
  return (
    <Section title="Caches" description="Saved by actions/cache. The least recently used go first beyond the limit; entries unused for 7 days are deleted.">
      <ErrorText error={caches.error ?? remove.error} />
      {caches.data && (
        <div className="space-y-1">
          <p className="text-sm">
            {formatSize(caches.data.total_size)} of {formatSize(caches.data.limit)} used
          </p>
          <div className="h-2 overflow-hidden rounded-full bg-muted" role="progressbar" aria-label="Cache use" aria-valuenow={Math.round(used)} aria-valuemin={0} aria-valuemax={100}>
            <div className="h-full bg-primary" style={{ width: `${used}%` }} />
          </div>
        </div>
      )}
      {caches.data?.caches.length === 0 && <p className="text-sm text-muted-foreground">No cache entries.</p>}
      {caches.data && caches.data.caches.length > 0 && (
        <ul className="divide-y text-sm">
          {caches.data.caches.map((c) => (
            <li key={c.id} className="flex flex-wrap items-center gap-3 py-2">
              <div className="min-w-0 flex-1">
                <code className="block truncate font-medium" title={c.key}>
                  {c.key}
                </code>
                <span className="text-xs text-muted-foreground">{c.ref.replace(/^refs\/(heads|tags)\//, "")}</span>
              </div>
              <span className="text-xs text-muted-foreground">
                {formatSize(c.size)} · used {formatDate(c.last_used_at)}
              </span>
              <Button variant="ghost" size="icon" aria-label={`Delete cache ${c.key}`} onClick={() => window.confirm(`Delete ${c.key}?`) && remove.mutate(c.id)}>
                <Trash2 className="h-4 w-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}
