import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { Alert } from "@/components/ui/alert";
import { Button, ButtonLink } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, ApiError } from "@/lib/api";
import { useLogin, useLoginCode, usePasskeyLogin } from "@/lib/auth";
import { useInstance } from "@/lib/queries";
import { ssoStart, useSSOProviders } from "@/lib/sso";
import { keyError, loginKey, passkeyAnswer, webauthnSupported } from "@/lib/webauthn";

// Only same-site paths: ?next= must not send the user to another site.
export function safeNext(next: string | null): string {
  return next && next.startsWith("/") && !next.startsWith("//") && !next.startsWith("/\\") ? next : "/";
}

// CodeStep is the second step for accounts with two-factor authentication.
function CodeStep({ challenge, methods, onDone, onRestart }: { challenge: string; methods: string[]; onDone: () => void; onRestart: () => void }) {
  const [code, setCode] = useState("");
  const [keyFailed, setKeyFailed] = useState("");
  const verify = useLoginCode();
  const err = verify.error instanceof ApiError ? verify.error : null;
  const useKey = methods.includes("webauthn") && webauthnSupported();
  const tryKey = async () => {
    setKeyFailed("");
    try {
      verify.mutate({ challenge, ...(await loginKey(challenge)) }, { onSuccess: onDone });
    } catch (e) {
      setKeyFailed(keyError(e));
    }
  };
  return (
    <div className="mx-auto max-w-sm pt-8">
      <h1 className="mb-6 text-center text-2xl font-semibold">Two-factor authentication</h1>
      <Card className="p-6">
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            verify.mutate({ challenge, code }, { onSuccess: onDone });
          }}
        >
          {keyFailed && <Alert variant="destructive">{keyFailed}</Alert>}
          {err && (
            <Alert variant="destructive">
              {err.type === "INVALID_CODE"
                ? "That code didn't work. Try the current one, or a recovery code."
                : err.type === "INVALID_KEY"
                  ? "The security key wasn't accepted. Try again, or use a code."
                : err.type === "CHALLENGE_EXPIRED"
                  ? "This sign-in expired. Start again."
                  : err.message}
            </Alert>
          )}
          {err?.type === "CHALLENGE_EXPIRED" ? (
            <Button type="button" className="w-full" onClick={onRestart}>
              Sign in again
            </Button>
          ) : (
            <>
              <div className="space-y-1.5">
                <Label htmlFor="code">{methods.includes("totp") || !useKey ? "Authentication code" : "Recovery code"}</Label>
                <Input
                  id="code"
                  autoFocus
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  required
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                />
                <p className="text-xs text-muted-foreground">
                  {methods.includes("totp") ? "From your authenticator app. Lost it? Use one of your recovery codes." : "If you can't use your security key."}
                </p>
              </div>
              <Button type="submit" className="w-full" disabled={verify.isPending}>
                {verify.isPending ? "Verifying…" : "Verify"}
              </Button>
              {useKey && (
                <Button type="button" variant="outline" className="w-full" onClick={tryKey} disabled={verify.isPending}>
                  Use your security key
                </Button>
              )}
            </>
          )}
        </form>
      </Card>
    </div>
  );
}

export function LoginPage() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [resent, setResent] = useState(false);
  const [params] = useSearchParams();
  // A provider that requires oGit 2FA sends the browser back here with a
  // challenge (and where to go after).
  const [challenge, setChallenge] = useState(params.get("challenge") ?? "");
  const [methods, setMethods] = useState<string[]>(params.get("methods")?.split(",").filter(Boolean) ?? []);
  const instance = useInstance();
  const providers = useSSOProviders();
  const passkey = usePasskeyLogin();
  const [passkeyFailed, setPasskeyFailed] = useState("");
  const login = useLogin();
  const navigate = useNavigate();
  const next = safeNext(params.get("redirect") ?? params.get("next"));
  const ssoError = params.get("sso_error");
  // Only providers sign in: the password form is for break-glass accounts.
  const ssoOnly = !!instance.data?.sso_only;
  const [local, setLocal] = useState(false);
  const done = () => navigate(next, { replace: true });

  function submit(e: FormEvent) {
    e.preventDefault();
    setResent(false);
    login.mutate(
      { email, password },
      { onSuccess: (r) => (r?.two_factor_required && r.challenge ? (setChallenge(r.challenge), setMethods(r.methods ?? []), setPassword("")) : done()) },
    );
  }

  if (challenge) return <CodeStep challenge={challenge} methods={methods} onDone={done} onRestart={() => setChallenge("")} />;

  async function signInWithPasskey() {
    setPasskeyFailed("");
    try {
      passkey.mutate(await passkeyAnswer(), { onSuccess: done });
    } catch (e) {
      setPasskeyFailed(keyError(e));
    }
  }
  const passkeyErr = passkey.error instanceof ApiError ? passkey.error : null;

  const err = login.error instanceof ApiError ? login.error : null;
  return (
    <div className="mx-auto max-w-sm pt-8">
      <h1 className="mb-6 text-center text-2xl font-semibold">Sign in to oGit</h1>
      <Card className="p-6">
        {ssoError && (
          <Alert variant="destructive" className="mb-4">
            {ssoError}
          </Alert>
        )}
        {ssoOnly && !local && <p className="mb-4 text-center text-sm text-muted-foreground">Sign in with your organization's account.</p>}
        {(!ssoOnly || local) && (
          <form className="space-y-4" onSubmit={submit}>
            {err && (
              <Alert variant="destructive">
                {err.type === "INVALID_CREDENTIALS"
                  ? "Incorrect email or password."
                  : err.type === "EMAIL_NOT_VERIFIED"
                    ? "Verify your email address first: open the link we sent you."
                    : err.type === "ACCOUNT_DISABLED"
                      ? "This account is disabled."
                      : err.message}
              </Alert>
            )}
            {err?.type === "EMAIL_NOT_VERIFIED" && (
              <Button
                type="button"
                variant="outline"
                className="w-full"
                disabled={resent}
                onClick={() => api("POST", "/auth/verify/resend", { email }).finally(() => setResent(true))}
              >
                {resent ? "If the address is registered, a new link is on its way" : "Send a new link"}
              </Button>
            )}
            <div className="space-y-1.5">
              <Label htmlFor="email">Email</Label>
              <Input id="email" type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">Password</Label>
              <Input id="password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
            </div>
            <Button type="submit" className="w-full" disabled={login.isPending}>
              {login.isPending ? "Signing in…" : "Sign in"}
            </Button>
          </form>
        )}
        {(providers.data?.length ?? 0) > 0 && (
          <div className="mt-4 space-y-2 border-t pt-4">
            {providers.data!.map((p) => (
              <ButtonLink key={p.slug} to={ssoStart(p.slug, next)} reloadDocument variant="outline" className="w-full">
                Sign in with {p.name}
              </ButtonLink>
            ))}
          </div>
        )}
        {ssoOnly && !local && (
          <Button type="button" variant="ghost" size="sm" className="mt-4 w-full" onClick={() => setLocal(true)}>
            Sign in with a local account
          </Button>
        )}
        {instance.data?.passkeys && !ssoOnly && webauthnSupported() && (
          <div className="mt-4 space-y-2 border-t pt-4">
            {(passkeyFailed || passkeyErr) && (
              <Alert variant="destructive">{passkeyFailed || (passkeyErr?.type === "INVALID_KEY" ? "That passkey wasn't accepted." : passkeyErr?.message)}</Alert>
            )}
            <Button type="button" variant="outline" className="w-full" onClick={signInWithPasskey} disabled={passkey.isPending}>
              Sign in with a passkey
            </Button>
          </div>
        )}
      </Card>
      {instance.data?.registration_enabled !== false && (
        <p className="mt-4 text-center text-sm text-muted-foreground">
          New here? <Link to="/register" className="text-primary hover:underline">Create an account</Link>
        </p>
      )}
    </div>
  );
}
