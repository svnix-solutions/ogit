import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Trash2 } from "lucide-react";
import { useState } from "react";
import { CopyField } from "@/components/copy-field";
import { ErrorText, Section } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, type Repository } from "@/lib/api";
import { formatDate } from "@/lib/queries";

// Tokens bound to this repository, for CI (maintainers and owners).

type RepoToken = {
  id: string;
  name: string;
  description: string;
  scopes: string[];
  expires_at: string | null;
  last_used_at: string | null;
  created_at: string;
  token?: string;
};

export const tokenScopes = [
  { scope: "read:repository", label: "Clone, fetch and read the API" },
  { scope: "write:repository", label: "Push (branch protection applies)" },
  { scope: "write:status", label: "Post commit statuses" },
  { scope: "read:packages", label: "Pull the repository's packages" },
  { scope: "write:packages", label: "Publish packages linked to the repository" },
];

export function RepoTokens({ repo }: { repo: Repository }) {
  const qc = useQueryClient();
  const key = ["repo-tokens", repo.id];
  const tokens = useQuery({
    queryKey: key,
    queryFn: async () => (await api<{ tokens: RepoToken[] }>("GET", `/repository/${repo.id}/tokens`)).tokens,
  });
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>(["read:repository"]);
  const [expires, setExpires] = useState("");
  const [created, setCreated] = useState<RepoToken | null>(null);
  const create = useMutation({
    mutationFn: () =>
      api<RepoToken>("POST", `/repository/${repo.id}/tokens`, {
        name, scopes, ...(expires ? { expires_at: new Date(expires + "T23:59:59Z").toISOString() } : {}),
      }),
    onSuccess: (t) => {
      setCreated(t);
      setName("");
      setExpires("");
      qc.invalidateQueries({ queryKey: key });
    },
  });
  const revoke = useMutation({
    mutationFn: (id: string) => api("DELETE", `/repository/${repo.id}/tokens/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  const host = window.location.host;

  return (
    <Section title="Access tokens" description="Tokens for this repository only, e.g. for CI: no personal account needed. They can't comment, review or act as a person.">
      {created && (
        <Alert>
          <p className="mb-2 text-sm font-medium">Copy the token {created.name} now: it won't be shown again.</p>
          <CopyField value={created.token!} label="New token" />
          <p className="mt-2 text-xs text-muted-foreground">
            git: <code>https://{created.name}:&lt;token&gt;@{host}/{repo.full_path}.git</code> · API: <code>Authorization: token &lt;token&gt;</code>
          </p>
        </Alert>
      )}
      <ErrorText error={tokens.error ?? revoke.error} />
      {tokens.data && tokens.data.length > 0 && (
        <ul className="divide-y text-sm">
          {tokens.data.map((t) => (
            <li key={t.id} className="flex flex-wrap items-center gap-3 py-2">
              <KeyRound className="h-4 w-4 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <p className="font-medium">{t.name}</p>
                <p className="text-xs text-muted-foreground">{t.scopes.join(", ")}</p>
              </div>
              <span className="text-xs text-muted-foreground">
                last used {formatDate(t.last_used_at)} · {t.expires_at ? `expires ${formatDate(t.expires_at)}` : "no expiry"}
              </span>
              <Button
                variant="ghost"
                size="icon"
                aria-label={`Revoke token ${t.name}`}
                onClick={() => {
                  if (window.confirm(`Revoke ${t.name}? Anything using it stops working.`)) revoke.mutate(t.id);
                }}
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <div className="grid gap-2 sm:grid-cols-2">
          <div className="space-y-1">
            <Label htmlFor="token-name">Name</Label>
            <Input id="token-name" placeholder="ci" value={name} onChange={(e) => setName(e.target.value)} required />
          </div>
          <div className="space-y-1">
            <Label htmlFor="token-expires">Expires (optional)</Label>
            <Input id="token-expires" type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
          </div>
        </div>
        <fieldset className="space-y-1 text-sm">
          <legend className="mb-1 font-medium">Allowed to</legend>
          {tokenScopes.map((s) => (
            <label key={s.scope} className="flex items-center gap-2">
              <input
                type="checkbox"
                checked={scopes.includes(s.scope)}
                onChange={(e) => setScopes(e.target.checked ? [...scopes, s.scope] : scopes.filter((x) => x !== s.scope))}
              />
              {s.label}
            </label>
          ))}
        </fieldset>
        <ErrorText error={create.error} />
        <Button type="submit" disabled={!name.trim() || scopes.length === 0 || create.isPending}>
          Create token
        </Button>
      </form>
    </Section>
  );
}
