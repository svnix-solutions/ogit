import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { useState } from "react";
import { CopyField } from "@/components/copy-field";
import { ErrorText, Section } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { api, type SSHKey, type Token } from "@/lib/api";
import { useProfile } from "@/lib/auth";
import { formatDate } from "@/lib/queries";
import { TwoFactor } from "./settings-2fa";

export function SettingsPage() {
  const { data: profile } = useProfile();
  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <h1 className="text-xl font-semibold">Settings</h1>
      <Section title="Account">
        <dl className="grid grid-cols-[8rem_1fr] gap-y-1 text-sm">
          <dt className="text-muted-foreground">Username</dt>
          <dd>{profile?.username}</dd>
          <dt className="text-muted-foreground">Email</dt>
          <dd>{profile?.email}</dd>
        </dl>
        <ChangePassword />
      </Section>
      <TwoFactor />
      <NotificationEmails />
      <Tokens />
      <SSHKeys />
    </div>
  );
}

const emailLevels = [
  { value: "participating", label: "Threads I take part in", hint: "Mentions, assignments, review requests, your own issues and pull requests, and threads you commented on." },
  { value: "all", label: "Also activity on repositories I watch", hint: "New issues, pull requests and releases on watched repositories too." },
  { value: "none", label: "No emails", hint: "Notifications stay in your inbox." },
];

function NotificationEmails() {
  const qc = useQueryClient();
  const settings = useQuery({ queryKey: ["notification-settings"], queryFn: () => api<{ email: string }>("GET", "/notifications/settings") });
  const set = useMutation({
    mutationFn: (email: string) => api<{ email: string }>("PUT", "/notifications/settings", { email }),
    onSuccess: (r) => qc.setQueryData(["notification-settings"], r),
  });
  return (
    <Section title="Notification emails" description="Notifications always reach your inbox; choose which ones are emailed too. What you get for each repository depends on how you watch it.">
      <ErrorText error={settings.error ?? set.error} />
      {settings.data && (
        <div className="space-y-2 text-sm" role="radiogroup" aria-label="Notification emails">
          {emailLevels.map((l) => (
            <label key={l.value} className="flex items-start gap-2">
              <input type="radio" name="email-level" className="mt-1" checked={settings.data.email === l.value} disabled={set.isPending} onChange={() => set.mutate(l.value)} />
              <span>
                {l.label}
                <span className="block text-xs text-muted-foreground">{l.hint}</span>
              </span>
            </label>
          ))}
        </div>
      )}
    </Section>
  );
}

function ChangePassword() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const change = useMutation({
    mutationFn: () => api("POST", "/user/password", { current_password: current, new_password: next }),
    onSuccess: () => {
      setCurrent("");
      setNext("");
    },
  });
  return (
    <form
      className="space-y-3 border-t pt-4"
      onSubmit={(e) => {
        e.preventDefault();
        change.mutate();
      }}
    >
      <h3 className="text-sm font-medium">Change password</h3>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-1.5">
          <Label htmlFor="current-password">Current password</Label>
          <Input id="current-password" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="new-password">New password</Label>
          <Input id="new-password" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} required />
        </div>
      </div>
      <ErrorText error={change.error} />
      {change.isSuccess && <p className="text-sm text-muted-foreground">Password changed. Your other sessions were signed out.</p>}
      <Button type="submit" disabled={change.isPending}>
        Change password
      </Button>
    </form>
  );
}

const scopeChoices = [
  { scope: "read:repository", label: "Clone and fetch" },
  { scope: "write:repository", label: "Push" },
  { scope: "write:status", label: "Commit statuses (CI)" },
];

function Tokens() {
  const qc = useQueryClient();
  const tokens = useQuery({ queryKey: ["tokens"], queryFn: async () => (await api<{ tokens: Token[] }>("GET", "/user/tokens")).tokens });
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>(["read:repository", "write:repository"]);
  const [expires, setExpires] = useState("");
  const create = useMutation({
    mutationFn: () =>
      api<{ name: string; token: string }>("POST", "/user/token", {
        name,
        scopes,
        expires_at: expires ? new Date(`${expires}T23:59:59`).toISOString() : undefined,
      }),
    onSuccess: () => {
      setName("");
      qc.invalidateQueries({ queryKey: ["tokens"] });
    },
  });
  const revoke = useMutation({ mutationFn: (id: string) => api("DELETE", `/user/tokens/${id}`), onSettled: () => qc.invalidateQueries({ queryKey: ["tokens"] }) });

  return (
    <div id="tokens">
      <Section title="Access tokens" description="For git over HTTPS: the token's name is the username and its value the password.">
        {create.data && (
          <Alert>
            <p className="mb-2">
              Copy the token for <strong>{create.data.name}</strong> now: it won't be shown again.
            </p>
            <CopyField value={create.data.token} label="new token" />
          </Alert>
        )}
        {tokens.data && tokens.data.length > 0 && (
          <ul className="divide-y text-sm">
            {tokens.data.map((t) => (
              <li key={t.id} className="flex items-center gap-3 py-2">
                <div className="min-w-0 flex-1">
                  <p className="font-medium">{t.name}</p>
                  <p className="text-xs text-muted-foreground">
                    {t.scopes.join(", ")} · created {formatDate(t.created_at)}
                    {t.expires_at && ` · expires ${formatDate(t.expires_at)}`}
                  </p>
                </div>
                <Button variant="ghost" size="icon" aria-label={`Revoke token ${t.name}`} onClick={() => revoke.mutate(t.id)}>
                  <Trash2 className="h-4 w-4" />
                </Button>
              </li>
            ))}
          </ul>
        )}
        <form
          className="space-y-3 border-t pt-4"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="token-name">Name</Label>
              <Input id="token-name" placeholder="laptop" value={name} onChange={(e) => setName(e.target.value)} required />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="token-expires">Expires (optional)</Label>
              <Input id="token-expires" type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
            </div>
          </div>
          <div className="flex gap-4">
            {scopeChoices.map((s) => (
              <label key={s.scope} className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={scopes.includes(s.scope)}
                  onChange={(e) => setScopes(e.target.checked ? [...scopes, s.scope] : scopes.filter((x) => x !== s.scope))}
                />
                {s.label}
              </label>
            ))}
          </div>
          <ErrorText error={create.error ?? revoke.error} />
          <Button type="submit" disabled={create.isPending || scopes.length === 0}>
            Create token
          </Button>
        </form>
      </Section>
    </div>
  );
}

function SSHKeys() {
  const qc = useQueryClient();
  const keys = useQuery({ queryKey: ["ssh-keys"], queryFn: async () => (await api<{ keys: SSHKey[] | null }>("GET", "/user/ssh-keys")).keys ?? [] });
  const [name, setName] = useState("");
  const [pub, setPub] = useState("");
  const add = useMutation({
    mutationFn: () => api("POST", "/user/ssh-keys", { name, key: pub }),
    onSuccess: () => {
      setName("");
      setPub("");
      qc.invalidateQueries({ queryKey: ["ssh-keys"] });
    },
  });
  const remove = useMutation({ mutationFn: (id: string) => api("DELETE", `/user/ssh-keys/${id}`), onSettled: () => qc.invalidateQueries({ queryKey: ["ssh-keys"] }) });

  return (
    <div id="ssh-keys">
      <Section title="SSH keys" description="ed25519, ECDSA, or RSA with at least 3072 bits. Paste the public key (the .pub file).">
        {keys.data && keys.data.length > 0 && (
          <ul className="divide-y text-sm">
            {keys.data.map((k) => (
              <li key={k.id} className="flex items-center gap-3 py-2">
                <div className="min-w-0 flex-1">
                  <p className="font-medium">{k.name}</p>
                  <p className="truncate font-mono text-xs text-muted-foreground">{k.fingerprint}</p>
                </div>
                <span className="text-xs text-muted-foreground">last used {formatDate(k.last_used_at)}</span>
                <Button variant="ghost" size="icon" aria-label={`Remove SSH key ${k.name}`} onClick={() => remove.mutate(k.id)}>
                  <Trash2 className="h-4 w-4" />
                </Button>
              </li>
            ))}
          </ul>
        )}
        <form
          className="space-y-2 border-t pt-4"
          onSubmit={(e) => {
            e.preventDefault();
            add.mutate();
          }}
        >
          <Input aria-label="SSH key name" placeholder="Name (defaults to the key's comment)" value={name} onChange={(e) => setName(e.target.value)} />
          <Textarea aria-label="SSH public key" placeholder="ssh-ed25519 AAAA… you@laptop" value={pub} onChange={(e) => setPub(e.target.value)} required className="font-mono text-xs" />
          <ErrorText error={add.error ?? remove.error} />
          <Button type="submit" disabled={add.isPending}>
            Add SSH key
          </Button>
        </form>
      </Section>
    </div>
  );
}
