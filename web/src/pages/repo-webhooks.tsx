import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Clock, RotateCw, Send, Trash2, XCircle } from "lucide-react";
import { useState } from "react";
import { ErrorText, Section } from "@/components/section";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";

// Webhooks of a repository (maintainers and owners): add, edit, ping,
// delete, and each hook's delivery log with redelivery.

export type Webhook = {
  id: string;
  url: string;
  events: string[];
  active: boolean;
  has_secret: boolean;
  created_at: string;
  updated_at: string;
};

export type Delivery = {
  id: string;
  event: string;
  action?: string;
  status: "pending" | "delivered" | "failed";
  attempts: number;
  response_status?: number;
  duration_ms: number;
  error?: string;
  created_at: string;
  delivered_at?: string;
  next_attempt_at?: string;
  payload?: string;
  response_body?: string;
};

export const webhookEvents: { name: string; label: string }[] = [
  { name: "push", label: "Pushes" },
  { name: "pull_request", label: "Pull requests" },
  { name: "pull_request_review", label: "Reviews" },
  { name: "pull_request_review_comment", label: "Review comments" },
  { name: "issue_comment", label: "Conversation comments" },
  { name: "repository", label: "Repository changes" },
  { name: "status", label: "Commit statuses" },
];

// eventsSummary describes a hook's events for its list entry.
export function eventsSummary(events: string[]): string {
  if (events.includes("*")) return "all events";
  return events.join(", ");
}

const hooksKey = (owner: string) => ["webhooks", owner];

// Webhooks manages the hooks of owner: a repository's or a group's API
// path (/repository/<id>, /group/<path>); a group's fire for every
// repository below it.
export function Webhooks({ owner, group }: { owner: string; group?: boolean }) {
  const hooks = useQuery({
    queryKey: hooksKey(owner),
    queryFn: async () => (await api<{ webhooks: Webhook[] | null }>("GET", `${owner}/hooks`)).webhooks ?? [],
  });
  const [open, setOpen] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  return (
    <Section
      title="Webhooks"
      description={
        (group ? "Events of every repository in this group and its sub-groups, sent " : "Send events to other services ") +
        "as GitHub-compatible JSON, signed with your secret in X-Hub-Signature-256. Failed deliveries are retried for about 40 minutes."
      }
    >
      <ErrorText error={hooks.error} />
      {hooks.data && hooks.data.length > 0 && (
        <ul className="divide-y rounded-md border text-sm">
          {hooks.data.map((h) => (
            <li key={h.id}>
              <button
                type="button"
                className="flex w-full items-center gap-3 px-3 py-2 text-left hover:bg-muted/50"
                aria-expanded={open === h.id}
                onClick={() => setOpen(open === h.id ? null : h.id)}
              >
                <span className="min-w-0 flex-1 truncate font-mono text-xs">{h.url}</span>
                <span className="hidden text-xs text-muted-foreground sm:inline">{eventsSummary(h.events)}</span>
                {!h.active && <Badge>inactive</Badge>}
              </button>
              {open === h.id && <WebhookDetails owner={owner} hook={h} onDeleted={() => setOpen(null)} />}
            </li>
          ))}
        </ul>
      )}
      {adding ? (
        <WebhookForm owner={owner} onDone={() => setAdding(false)} />
      ) : (
        <Button variant="outline" onClick={() => setAdding(true)}>
          Add webhook
        </Button>
      )}
    </Section>
  );
}

// WebhookForm adds a hook, or edits one (then an empty secret keeps it).
function WebhookForm({ owner, hook, onDone }: { owner: string; hook?: Webhook; onDone: () => void }) {
  const qc = useQueryClient();
  const [url, setURL] = useState(hook?.url ?? "");
  const [secret, setSecret] = useState("");
  const [removeSecret, setRemoveSecret] = useState(false);
  const [mode, setMode] = useState<"push" | "all" | "pick">(
    !hook || (hook.events.length === 1 && hook.events[0] === "push") ? "push" : hook.events.includes("*") ? "all" : "pick",
  );
  const [picked, setPicked] = useState<string[]>(hook && !hook.events.includes("*") ? hook.events : ["push"]);
  const [active, setActive] = useState(hook?.active ?? true);

  const events = mode === "push" ? ["push"] : mode === "all" ? ["*"] : picked;
  const save = useMutation({
    mutationFn: () => {
      const body: Record<string, unknown> = { url, events, active };
      if (!hook) body.secret = secret;
      else if (removeSecret) body.secret = "";
      else if (secret) body.secret = secret;
      return hook ? api("PATCH", `${owner}/hooks/${hook.id}`, body) : api("POST", `${owner}/hooks`, body);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: hooksKey(owner) });
      qc.invalidateQueries({ queryKey: ["deliveries"] });
      onDone();
    },
  });
  const toggle = (name: string, on: boolean) => setPicked(on ? [...picked, name] : picked.filter((e) => e !== name));
  const id = hook?.id ?? "new";

  return (
    <form
      className="space-y-3"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="space-y-1">
        <Label htmlFor={`hook-url-${id}`}>Payload URL</Label>
        <Input id={`hook-url-${id}`} type="url" placeholder="https://ci.example.com/hooks/ogit" value={url} onChange={(e) => setURL(e.target.value)} required />
      </div>
      <div className="space-y-1">
        <Label htmlFor={`hook-secret-${id}`}>Secret</Label>
        <Input
          id={`hook-secret-${id}`}
          type="password"
          autoComplete="new-password"
          placeholder={hook?.has_secret ? "Unchanged" : "Optional"}
          value={secret}
          disabled={removeSecret}
          onChange={(e) => setSecret(e.target.value)}
        />
        {hook?.has_secret && (
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={removeSecret} onChange={(e) => setRemoveSecret(e.target.checked)} /> Remove the secret
          </label>
        )}
      </div>
      <fieldset className="space-y-1 text-sm">
        <legend className="mb-1 font-medium">Events</legend>
        {(
          [
            ["push", "Just pushes"],
            ["all", "Everything"],
            ["pick", "Let me choose"],
          ] as const
        ).map(([value, label]) => (
          <label key={value} className="flex items-center gap-2">
            <input type="radio" name={`hook-events-${id}`} checked={mode === value} onChange={() => setMode(value)} /> {label}
          </label>
        ))}
        {mode === "pick" && (
          <div className="grid gap-1 pl-6 sm:grid-cols-2">
            {webhookEvents.map((e) => (
              <label key={e.name} className="flex items-center gap-2">
                <input type="checkbox" checked={picked.includes(e.name)} onChange={(ev) => toggle(e.name, ev.target.checked)} /> {e.label}
              </label>
            ))}
          </div>
        )}
      </fieldset>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={active} onChange={(e) => setActive(e.target.checked)} /> Active
      </label>
      <ErrorText error={save.error} />
      <div className="flex gap-2">
        <Button type="submit" disabled={save.isPending || events.length === 0}>
          {hook ? "Save webhook" : "Add webhook"}
        </Button>
        <Button type="button" variant="ghost" onClick={onDone}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

function WebhookDetails({ owner, hook, onDeleted }: { owner: string; hook: Webhook; onDeleted: () => void }) {
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const base = `${owner}/hooks/${hook.id}`;
  const deliveriesKey = ["deliveries", hook.id];
  const deliveries = useQuery({
    queryKey: deliveriesKey,
    queryFn: async () => (await api<{ deliveries: Delivery[] | null }>("GET", `${base}/deliveries`)).deliveries ?? [],
    // Pending deliveries change on their own; look again while there are some.
    refetchInterval: (q) => (q.state.data?.some((d) => d.status === "pending") ? 5000 : false),
  });
  const ping = useMutation({
    mutationFn: () => api("POST", `${base}/pings`),
    onSuccess: () => qc.invalidateQueries({ queryKey: deliveriesKey }),
  });
  const remove = useMutation({
    mutationFn: () => api("DELETE", base),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: hooksKey(owner) });
      onDeleted();
    },
  });

  return (
    <div className="space-y-4 border-t bg-muted/20 px-3 py-3">
      {editing ? (
        <WebhookForm owner={owner} hook={hook} onDone={() => setEditing(false)} />
      ) : (
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="outline" onClick={() => setEditing(true)}>
            Edit
          </Button>
          <Button size="sm" variant="outline" onClick={() => ping.mutate()} disabled={ping.isPending}>
            <Send className="h-4 w-4" /> Ping
          </Button>
          <Button
            size="sm"
            variant="destructive"
            onClick={() => {
              if (window.confirm(`Delete the webhook to ${hook.url}?`)) remove.mutate();
            }}
            disabled={remove.isPending}
          >
            <Trash2 className="h-4 w-4" /> Delete
          </Button>
        </div>
      )}
      <ErrorText error={ping.error ?? remove.error} />
      <div className="space-y-2">
        <h3 className="text-sm font-medium">Recent deliveries</h3>
        <ErrorText error={deliveries.error} />
        {deliveries.data?.length === 0 && <p className="text-sm text-muted-foreground">None yet.</p>}
        {deliveries.data && deliveries.data.length > 0 && (
          <ul className="divide-y rounded-md border bg-background">
            {deliveries.data.map((d) => (
              <DeliveryRow key={d.id} base={base} delivery={d} onRedelivered={() => qc.invalidateQueries({ queryKey: deliveriesKey })} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function StatusIcon({ status }: { status: Delivery["status"] }) {
  if (status === "delivered") return <CheckCircle2 aria-label="delivered" className="h-4 w-4 shrink-0 text-green-600" />;
  if (status === "failed") return <XCircle aria-label="failed" className="h-4 w-4 shrink-0 text-destructive" />;
  return <Clock aria-label="pending" className="h-4 w-4 shrink-0 text-muted-foreground" />;
}

// deliveryOutcome is the short result shown next to a delivery.
export function deliveryOutcome(d: Delivery): string {
  const answer = d.response_status ? `HTTP ${d.response_status}` : d.error ? "no answer" : "";
  switch (d.status) {
    case "delivered":
      return `${answer} in ${d.duration_ms} ms`;
    case "failed":
      return `${answer || "failed"} after ${d.attempts} ${d.attempts === 1 ? "attempt" : "attempts"}`;
    default:
      return d.attempts === 0 ? "queued" : `${answer}, retrying${d.next_attempt_at ? " at " + new Date(d.next_attempt_at).toLocaleTimeString() : ""}`;
  }
}

function DeliveryRow({ base, delivery: d, onRedelivered }: { base: string; delivery: Delivery; onRedelivered: () => void }) {
  const [open, setOpen] = useState(false);
  const details = useQuery({
    queryKey: ["delivery", d.id],
    queryFn: () => api<Delivery>("GET", `${base}/deliveries/${d.id}`),
    enabled: open,
  });
  const redeliver = useMutation({
    mutationFn: () => api("POST", `${base}/deliveries/${d.id}/attempts`),
    onSuccess: onRedelivered,
  });

  return (
    <li className="text-sm">
      <button type="button" className="flex w-full items-center gap-2 px-3 py-2 text-left hover:bg-muted/50" aria-expanded={open} onClick={() => setOpen(!open)}>
        <StatusIcon status={d.status} />
        <span className="font-mono text-xs">
          {d.event}
          {d.action ? `.${d.action}` : ""}
        </span>
        <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{deliveryOutcome(d)}</span>
        <span className="hidden text-xs text-muted-foreground sm:inline">{new Date(d.created_at).toLocaleString()}</span>
      </button>
      {open && (
        <div className="space-y-2 border-t px-3 py-2">
          {d.error && <p className="text-xs text-destructive">{d.error}</p>}
          <ErrorText error={details.error ?? redeliver.error} />
          {details.data && (
            <>
              <p className="text-xs font-medium">Payload</p>
              <pre className="max-h-72 overflow-auto rounded bg-muted p-2 text-xs">{pretty(details.data.payload)}</pre>
              {details.data.response_body && (
                <>
                  <p className="text-xs font-medium">Response</p>
                  <pre className="max-h-40 overflow-auto rounded bg-muted p-2 text-xs">{details.data.response_body}</pre>
                </>
              )}
            </>
          )}
          <Button size="sm" variant="outline" onClick={() => redeliver.mutate()} disabled={redeliver.isPending}>
            <RotateCw className="h-4 w-4" /> Redeliver
          </Button>
        </div>
      )}
    </li>
  );
}

function pretty(json: string | undefined): string {
  if (!json) return "";
  try {
    return JSON.stringify(JSON.parse(json), null, 2);
  } catch {
    return json;
  }
}
