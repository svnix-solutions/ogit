import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ShieldCheck } from "lucide-react";
import { useMemo, useState } from "react";
import { CopyField } from "@/components/copy-field";
import { ErrorText, Section } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";

// Two-factor authentication in the user's settings (application/two_factor.go).

type Status = { enabled: boolean; recovery_codes_left: number; totp_available: boolean };
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
      setCodes(r.recovery_codes);
      refresh();
    },
  });
  const regenerate = useMutation({
    mutationFn: () => api<{ recovery_codes: string[] }>("POST", "/user/2fa/recovery-codes"),
    onSuccess: (r) => (setCodes(r.recovery_codes), refresh()),
  });
  const disable = useMutation({ mutationFn: () => api("DELETE", "/user/2fa"), onSuccess: refresh });

  const s = status.data;
  return (
    <Section
      title="Two-factor authentication"
      description="Signing in asks for a code from an authenticator app after your password. Access tokens and SSH keys keep working for git."
    >
      <ErrorText error={status.error ?? start.error ?? regenerate.error ?? disable.error} />
      {codes && <RecoveryCodes codes={codes} onDone={() => setCodes(null)} />}
      {s && !codes && s.enabled && (
        <div className="space-y-3 text-sm">
          <p className="flex flex-wrap items-center gap-2">
            <Badge className="border-green-600 text-green-700 dark:text-green-400">
              <ShieldCheck className="h-3 w-3" /> On
            </Badge>
            <span className="text-muted-foreground">
              Authenticator app · {s.recovery_codes_left} recovery {s.recovery_codes_left === 1 ? "code" : "codes"} left
            </span>
          </p>
          {s.recovery_codes_left <= 3 && <Alert>You're running out of recovery codes: generate new ones.</Alert>}
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={() => regenerate.mutate()} disabled={regenerate.isPending}>
              New recovery codes
            </Button>
            <Button
              variant="destructive"
              onClick={() => window.confirm("Turn off two-factor authentication? Signing in will only need your password.") && disable.mutate()}
              disabled={disable.isPending}
            >
              Turn off
            </Button>
          </div>
        </div>
      )}
      {s && !s.enabled && !setup && (
        <div className="space-y-2 text-sm">
          <p className="text-muted-foreground">Off.</p>
          {s.totp_available ? (
            <Button onClick={() => start.mutate()} disabled={start.isPending}>
              Set up an authenticator app
            </Button>
          ) : (
            <Alert>Authenticator apps aren't available on this server yet: an administrator must set security.encryption_key.</Alert>
          )}
        </div>
      )}
      {setup && (
        <form
          className="space-y-3 text-sm"
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
    </Section>
  );
}
