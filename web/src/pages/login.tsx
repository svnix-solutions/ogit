import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, ApiError } from "@/lib/api";
import { useLogin } from "@/lib/auth";

// Only same-site paths: ?next= must not send the user to another site.
export function safeNext(next: string | null): string {
  return next && next.startsWith("/") && !next.startsWith("//") && !next.startsWith("/\\") ? next : "/";
}

export function LoginPage() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [resent, setResent] = useState(false);
  const login = useLogin();
  const navigate = useNavigate();
  const [params] = useSearchParams();

  function submit(e: FormEvent) {
    e.preventDefault();
    setResent(false);
    login.mutate({ email, password }, { onSuccess: () => navigate(safeNext(params.get("next")), { replace: true }) });
  }

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
