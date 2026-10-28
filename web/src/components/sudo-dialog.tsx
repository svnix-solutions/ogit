import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, ApiError } from "@/lib/api";
import { useProfile } from "@/lib/auth";
import { ssoStart, useIdentities } from "@/lib/sso";
import { setSudoHandler } from "@/lib/sudo";
import { keyError, sudoKey, webauthnSupported } from "@/lib/webauthn";
import { Alert } from "./ui/alert";
import { Button } from "./ui/button";
import { Card } from "./ui/card";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

// SudoDialog asks to confirm it's you before a sensitive action: a code
// with two-factor authentication, else the password. Mounted once, in the
// layout; api() opens it on SUDO_REQUIRED.
export function SudoDialog() {
  const { data: profile } = useProfile();
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const pending = useRef<((ok: boolean) => void) | null>(null);

  useEffect(() => {
    setSudoHandler(
      () =>
        new Promise<boolean>((resolve) => {
          pending.current?.(false);
          pending.current = resolve;
          setValue("");
          setError("");
          setOpen(true);
        }),
    );
    return () => setSudoHandler(null);
  }, []);

  const finish = (ok: boolean) => {
    pending.current?.(ok);
    pending.current = null;
    setOpen(false);
  };
  const twoFactor = !!profile?.two_factor_enabled;
  const status = useQuery({
    queryKey: ["two-factor"],
    queryFn: () => api<{ totp: boolean; keys: unknown[] }>("GET", "/user/2fa"),
    enabled: open && twoFactor,
  });
  const hasKeys = (status.data?.keys?.length ?? 0) > 0 && webauthnSupported();
  // Accounts without a password (and without 2FA) confirm through a
  // provider they sign in with; they come back here and try again.
  const viaProvider = open && (profile?.password_login === false || profile?.has_password === false) && !twoFactor;
  const identities = useIdentities(viaProvider);

  async function useKey() {
    setBusy(true);
    setError("");
    try {
      await api("POST", "/auth/sudo", await sudoKey());
      finish(true);
    } catch (err) {
      setError(err instanceof ApiError && err.type === "INVALID_KEY" ? "The security key wasn't accepted." : keyError(err));
    } finally {
      setBusy(false);
    }
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api("POST", "/auth/sudo", twoFactor ? { code: value } : { password: value });
      finish(true);
    } catch (err) {
      setError(err instanceof ApiError && (err.type === "INVALID_CODE" || err.type === "INVALID_PASSWORD") ? (twoFactor ? "That code didn't work." : "Incorrect password.") : (err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (!open) return null;
  if (viaProvider) {
    const here = window.location.pathname + window.location.search;
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" role="dialog" aria-modal="true" aria-labelledby="sudo-title">
        <Card className="w-full max-w-sm space-y-4 p-6">
          <div>
            <h2 id="sudo-title" className="text-lg font-semibold">
              Confirm it's you
            </h2>
            <p className="text-sm text-muted-foreground">This action is sensitive. Sign in again with your provider; you'll come back here to try again.</p>
          </div>
          <div className="flex flex-wrap gap-2">
            {identities.data?.map((i) => (
              <Button key={i.id} onClick={() => window.location.assign(ssoStart(i.provider, here, "sudo"))}>
                Confirm with {i.provider_name}
              </Button>
            ))}
            <Button type="button" variant="ghost" onClick={() => finish(false)}>
              Cancel
            </Button>
          </div>
        </Card>
      </div>
    );
  }
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" role="dialog" aria-modal="true" aria-labelledby="sudo-title">
      <Card className="w-full max-w-sm space-y-4 p-6">
        <div>
          <h2 id="sudo-title" className="text-lg font-semibold">
            Confirm it's you
          </h2>
          <p className="text-sm text-muted-foreground">This action is sensitive. You won't be asked again for 10 minutes.</p>
        </div>
        <form className="space-y-3" onSubmit={submit}>
          {error && <Alert variant="destructive">{error}</Alert>}
          <div className="space-y-1.5">
            <Label htmlFor="sudo-value">{twoFactor ? "Authentication code" : "Password"}</Label>
            <Input
              id="sudo-value"
              autoFocus
              type={twoFactor ? "text" : "password"}
              inputMode={twoFactor ? "numeric" : undefined}
              autoComplete={twoFactor ? "one-time-code" : "current-password"}
              value={value}
              onChange={(e) => setValue(e.target.value)}
              required
            />
            {twoFactor && <p className="text-xs text-muted-foreground">From your authenticator app, or a recovery code.</p>}
          </div>
          <div className="flex gap-2">
            <Button type="submit" disabled={busy}>
              Confirm
            </Button>
            {hasKeys && (
              <Button type="button" variant="outline" onClick={useKey} disabled={busy}>
                Use security key
              </Button>
            )}
            <Button type="button" variant="ghost" onClick={() => finish(false)}>
              Cancel
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
