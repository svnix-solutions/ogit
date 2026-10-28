import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { useLocation, useNavigate } from "react-router";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, ApiError } from "@/lib/api";
import { safeNext } from "./login";

// LoginLinkPage: a provider signed in someone whose email belongs to an
// account here; its password (and 2FA code) links them (the token comes in
// the URL's fragment).
export function LoginLinkPage() {
  const token = new URLSearchParams(useLocation().hash.slice(1)).get("token") ?? "";
  const info = useQuery({
    queryKey: ["sso-link", token],
    queryFn: () => api<{ provider: string; email: string; username: string }>("GET", `/auth/sso/link?token=${encodeURIComponent(token)}`),
    enabled: !!token,
    retry: false,
  });
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [needsCode, setNeedsCode] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const navigate = useNavigate();
  const qc = useQueryClient();

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const r = await api<{ redirect: string }>("POST", "/auth/sso/link", { token, password, ...(code ? { code } : {}) });
      await qc.invalidateQueries();
      navigate(safeNext(r.redirect), { replace: true });
    } catch (err) {
      if (err instanceof ApiError && err.type === "TWO_FACTOR_CODE_REQUIRED") setNeedsCode(true);
      else if (err instanceof ApiError && err.type === "INVALID_PASSWORD") setError("Incorrect password.");
      else if (err instanceof ApiError && err.type === "INVALID_CODE") setError("That code didn't work.");
      else setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mx-auto max-w-sm pt-8">
      <h1 className="mb-6 text-center text-2xl font-semibold">Link your account</h1>
      <Card className="p-6">
        {!token || info.isError ? (
          <Alert variant="destructive">This link request expired. Sign in again.</Alert>
        ) : !info.data ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : (
          <form className="space-y-4" onSubmit={submit}>
            <p className="text-sm">
              {info.data.email} already has an account here, <strong>{info.data.username}</strong>. Enter its password to sign in with{" "}
              {info.data.provider} from now on.
            </p>
            {error && <Alert variant="destructive">{error}</Alert>}
            <div className="space-y-1.5">
              <Label htmlFor="link-password">Password</Label>
              <Input id="link-password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
            </div>
            {needsCode && (
              <div className="space-y-1.5">
                <Label htmlFor="link-code">Authentication code</Label>
                <Input id="link-code" autoFocus inputMode="numeric" autoComplete="one-time-code" required value={code} onChange={(e) => setCode(e.target.value)} />
                <p className="text-xs text-muted-foreground">From your authenticator app, or a recovery code.</p>
              </div>
            )}
            <Button type="submit" className="w-full" disabled={busy}>
              {busy ? "Linking…" : `Link ${info.data.provider} and sign in`}
            </Button>
          </form>
        )}
      </Card>
    </div>
  );
}
