import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, ShieldCheck, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { CopyField } from "@/components/copy-field";
import { ErrorText, Section } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, ApiError } from "@/lib/api";
import { formatDate } from "@/lib/queries";
import { addKey, keyError, webauthnSupported } from "@/lib/webauthn";

// Two-factor authentication in the user's settings (application/two_factor.go).

type Status = { enabled: boolean; totp: boolean; keys: Key[]; recovery_codes_left: number; totp_available: boolean; webauthn_available: boolean };
type Setup = { secret: string; uri: string; qr: string[] };

const statusKey = ["two-factor"];

// QRCode draws the server's QR matrix ('1' dark) as one SVG path: no image
// URL, so the page's CSP stays strict.
export function QRCode({ rows, label }: { rows: string[]; label: string }) {
  const d = useMemo(() => {
    let out = "";
    rows.forEach((row, y) => {
      for (let x = 0; x < row.length; x++) if (row[x] === "1") out += `M${x} ${y}h1v1h-1z`;
    });
    return out;
  }, [rows]);
  const n = rows.length;
  return (
    <svg role="img" aria-label={label} viewBox={`0 0 ${n} ${n}`} className="h-48 w-48 rounded bg-white" shapeRendering="crispEdges">
      <path d={d} fill="#000" />
    </svg>
  );
}

export function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const text = codes.join("\n") + "\n";
  const href = useMemo(() => URL.createObjectURL(new Blob([text], { type: "text/plain" })), [text]);
  return (
    <div className="space-y-3 rounded-md border p-4">
      <Alert>
        Save these recovery codes somewhere safe. Each one signs you in once if you lose your authenticator app. They won't be shown again.
      </Alert>
      <ul aria-label="Recovery codes" className="grid grid-cols-2 gap-1 font-mono text-sm">
        {codes.map((c) => (
          <li key={c}>{c}</li>
        ))}
      </ul>
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" onClick={() => navigator.clipboard?.writeText(text)}>
          Copy
        </Button>
        <a href={href} download="ogit-recovery-codes.txt" className="inline-flex h-9 items-center rounded-md border px-3 text-sm hover:bg-muted">
          Download
        </a>
        <Button type="button" onClick={onDone}>
          I've saved them
        </Button>
      </div>
    </div>
  );
}

type Key = { id: string; name: string; passkey: boolean; last_used_at: string | null; created_at: string };

export function TwoFactor() {
  const qc = useQueryClient();
  const status = useQuery({ queryKey: statusKey, queryFn: () => api<Status>("GET", "/user/2fa") });
  const [setup, setSetup] = useState<Setup | null>(null);
  const [code, setCode] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const refresh = () => {
    qc.invalidateQueries({ queryKey: statusKey });
    qc.invalidateQueries({ queryKey: ["profile"] });
  };

  const start = useMutation({ mutationFn: () => api<Setup>("POST", "/user/2fa/totp"), onSuccess: (s) => (setSetup(s), setCode("")) });
  const confirm = useMutation({
    mutationFn: () => api<{ recovery_codes: string[] }>("POST", "/user/2fa/totp/confirm", { code }),
    onSuccess: (r) => {
      setSetup(null);
      if (r.recovery_codes?.length) setCodes(r.recovery_codes);
      refresh();
    },
  });
  const regenerate = useMutation({
    mutationFn: () => api<{ recovery_codes: string[] }>("POST", "/user/2fa/recovery-codes"),
    onSuccess: (r) => (setCodes(r.recovery_codes), refresh()),
  });
  const disable = useMutation({ mutationFn: () => api("DELETE", "/user/2fa"), onSuccess: refresh });

  const s = status.data && { ...status.data, keys: status.data.keys ?? [] };
  return (
    <Section
      title="Two-factor authentication"
      description="Signing in asks for a second factor after your password: a code from an authenticator app, or a security key. Access tokens and SSH keys keep working for git."
    >
      <ErrorText error={status.error ?? start.error ?? regenerate.error ?? disable.error} />
      {codes && <RecoveryCodes codes={codes} onDone={() => setCodes(null)} />}
      {s && !codes && (
        <div className="space-y-5 text-sm">
          <p className="flex flex-wrap items-center gap-2">
            {s.enabled ? (
              <Badge className="border-green-600 text-green-700 dark:text-green-400">
                <ShieldCheck className="h-3 w-3" /> On
              </Badge>
            ) : (
              <Badge>Off</Badge>
            )}
            {s.enabled && (
              <span className="text-muted-foreground">
                {s.recovery_codes_left} recovery {s.recovery_codes_left === 1 ? "code" : "codes"} left
              </span>
            )}
          </p>
          {s.enabled && s.recovery_codes_left <= 3 && <Alert>You're running out of recovery codes: generate new ones.</Alert>}

          <div className="space-y-2">
            <h3 className="font-medium">Authenticator app</h3>
            {s.totp ? (
              <p className="text-muted-foreground">Set up.</p>
            ) : setup ? null : s.totp_available ? (
              <Button variant="outline" onClick={() => start.mutate()} disabled={start.isPending}>
                Set up an authenticator app
              </Button>
            ) : (
              <p className="text-muted-foreground">Unavailable on this server: an administrator must set security.encryption_key.</p>
            )}
            {setup && (
              <form
                className="space-y-3"
                onSubmit={(e) => {
                  e.preventDefault();
                  confirm.mutate();
                }}
              >
                <p>Scan this code with your authenticator app (or type the key), then enter the 6-digit code it shows.</p>
                <QRCode rows={setup.qr} label="QR code for your authenticator app" />
                <CopyField value={setup.secret} label="setup key" />
                <div className="space-y-1.5">
                  <Label htmlFor="totp-code" className="block">
                    Code from the app
                  </Label>
                  <Input id="totp-code" className="block w-40" inputMode="numeric" autoComplete="one-time-code" required value={code} onChange={(e) => setCode(e.target.value)} />
                </div>
                <ErrorText error={confirm.error} />
                <div className="flex gap-2">
                  <Button type="submit" disabled={confirm.isPending}>
                    Turn on
                  </Button>
                  <Button type="button" variant="ghost" onClick={() => setSetup(null)}>
                    Cancel
                  </Button>
                </div>
              </form>
            )}
          </div>

          <SecurityKeys status={s} onCodes={setCodes} onChange={refresh} />

          {s.enabled && (
            <div className="flex flex-wrap gap-2 border-t pt-4">
              <Button variant="outline" onClick={() => regenerate.mutate()} disabled={regenerate.isPending}>
                New recovery codes
              </Button>
              <Button
                variant="destructive"
                onClick={() =>
                  window.confirm("Turn off two-factor authentication? This removes your authenticator app and security keys; signing in will only need your password.") &&
                  disable.mutate()
                }
                disabled={disable.isPending}
              >
                Turn off
              </Button>
            </div>
          )}
        </div>
      )}
    </Section>
  );
}

function SecurityKeys({ status: s, onCodes, onChange }: { status: Status; onCodes: (c: string[]) => void; onChange: () => void }) {
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const remove = useMutation({ mutationFn: (k: Key) => api("DELETE", `/user/2fa/webauthn/${k.id}`), onSuccess: onChange });

  async function add(passkey: boolean) {
    setBusy(true);
    setError("");
    try {
      const codes = await addKey(name.trim() || (passkey ? "Passkey" : "Security key"), passkey);
      setName("");
      if (codes.length) onCodes(codes);
      onChange();
    } catch (e) {
      setError(e instanceof ApiError && e.type === "KEY_NAME_TAKEN" ? "You already have a key with this name." : keyError(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-2">
      <h3 className="font-medium">Security keys and passkeys</h3>
      {!s.webauthn_available ? (
        <p className="text-muted-foreground">Unavailable on this server: an administrator must set http.public_url.</p>
      ) : (
        <>
          {s.keys.length > 0 && (
            <ul className="divide-y rounded-md border">
              {s.keys.map((k) => (
                <li key={k.id} className="flex flex-wrap items-center gap-2 px-3 py-2">
                  <KeyRound className="h-4 w-4 text-muted-foreground" />
                  <span className="font-medium">{k.name}</span>
                  {k.passkey && <Badge>passkey</Badge>}
                  <span className="text-xs text-muted-foreground">{k.last_used_at ? `used ${formatDate(k.last_used_at)}` : "never used"}</span>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="ml-auto"
                    aria-label={`Remove ${k.name}`}
                    disabled={remove.isPending}
                    onClick={() => window.confirm(`Remove ${k.name}?`) && remove.mutate(k)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </li>
              ))}
            </ul>
          )}
          {webauthnSupported() ? (
            <div className="flex flex-wrap items-end gap-2">
              <div className="space-y-1.5">
                <Label htmlFor="key-name" className="block">
                  Name
                </Label>
                <Input id="key-name" className="block w-48" placeholder="YubiKey, laptop…" value={name} onChange={(e) => setName(e.target.value)} />
              </div>
              <Button variant="outline" onClick={() => add(false)} disabled={busy}>
                Add security key
              </Button>
              <Button variant="outline" onClick={() => add(true)} disabled={busy}>
                Add passkey
              </Button>
            </div>
          ) : (
            <p className="text-muted-foreground">This browser doesn't support security keys.</p>
          )}
          <p className="text-xs text-muted-foreground">A passkey also lets you sign in without your password, with your device's PIN or biometrics.</p>
          {error && <Alert variant="destructive">{error}</Alert>}
          <ErrorText error={remove.error} />
        </>
      )}
    </div>
  );
}
