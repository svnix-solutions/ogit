import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, ApiError } from "@/lib/api";
import { useLogin, useLoginCode } from "@/lib/auth";

// Only same-site paths: ?next= must not send the user to another site.
export function safeNext(next: string | null): string {
  return next && next.startsWith("/") && !next.startsWith("//") && !next.startsWith("/\\") ? next : "/";
}

// CodeStep is the second step for accounts with two-factor authentication.
function CodeStep({ challenge, onDone, onRestart }: { challenge: string; onDone: () => void; onRestart: () => void }) {
  const [code, setCode] = useState("");
  const verify = useLoginCode();
  const err = verify.error instanceof ApiError ? verify.error : null;
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
          {err && (
            <Alert variant="destructive">
              {err.type === "INVALID_CODE"
                ? "That code didn't work. Try the current one, or a recovery code."
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
                <Label htmlFor="code">Authentication code</Label>
                <Input
                  id="code"
                  autoFocus
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  required
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                />
                <p className="text-xs text-muted-foreground">From your authenticator app. Lost it? Use one of your recovery codes.</p>
              </div>
              <Button type="submit" className="w-full" disabled={verify.isPending}>
                {verify.isPending ? "Verifying…" : "Verify"}
              </Button>
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
  const [challenge, setChallenge] = useState("");
  const login = useLogin();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const done = () => navigate(safeNext(params.get("next")), { replace: true });

  function submit(e: FormEvent) {
    e.preventDefault();
    setResent(false);
    login.mutate(
      { email, password },
      { onSuccess: (r) => (r?.two_factor_required && r.challenge ? (setChallenge(r.challenge), setPassword("")) : done()) },
    );
  }

  if (challenge) return <CodeStep challenge={challenge} onDone={done} onRestart={() => setChallenge("")} />;

  const err = login.error instanceof ApiError ? login.error : null;
  return (
    <div className="mx-auto max-w-sm pt-8">
      <h1 className="mb-6 text-center text-2xl font-semibold">Sign in to oGit</h1>
      <Card className="p-6">
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
      </Card>
      <p className="mt-4 text-center text-sm text-muted-foreground">
        New here? <Link to="/register" className="text-primary hover:underline">Create an account</Link>
      </p>
    </div>
  );
}
