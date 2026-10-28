import { useState, type FormEvent } from "react";
import { Link } from "react-router";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ApiError } from "@/lib/api";
import { useRegister } from "@/lib/auth";

export function RegisterPage() {
  const [form, setForm] = useState({ username: "", email: "", password: "" });
  const register = useRegister();
  const set = (k: keyof typeof form) => (e: { target: { value: string } }) => setForm({ ...form, [k]: e.target.value });

  function submit(e: FormEvent) {
    e.preventDefault();
    register.mutate(form);
  }

  if (register.isSuccess) {
    return (
      <div className="mx-auto max-w-sm pt-8 text-center">
        <h1 className="mb-4 text-2xl font-semibold">Account created</h1>
        <p className="text-sm text-muted-foreground">
          If this server asks for email verification, open the link we sent to {form.email} first.
        </p>
        <Link to="/login" className="mt-6 inline-block text-primary hover:underline">Sign in</Link>
      </div>
    );
  }

  const err = register.error instanceof ApiError ? register.error : null;
  return (
    <div className="mx-auto max-w-sm pt-8">
      <h1 className="mb-6 text-center text-2xl font-semibold">Create your account</h1>
      <Card className="p-6">
        <form className="space-y-4" onSubmit={submit}>
          {err && (
            <Alert variant="destructive">
              {err.type === "REGISTRATION_DISABLED" ? "Registration is disabled on this server." : err.message}
            </Alert>
          )}
          <div className="space-y-1.5">
            <Label htmlFor="username">Username</Label>
            <Input id="username" autoComplete="username" required value={form.username} onChange={set("username")} />
            <p className="text-xs text-muted-foreground">Lowercase letters, digits, '.', '_' or '-'. It's part of your repositories' URLs.</p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="email">Email</Label>
            <Input id="email" type="email" autoComplete="email" required value={form.email} onChange={set("email")} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="password">Password</Label>
            <Input id="password" type="password" autoComplete="new-password" required value={form.password} onChange={set("password")} />
            <p className="text-xs text-muted-foreground">At least 12 characters, with upper and lower case letters, a digit and a symbol (unless the server says otherwise).</p>
          </div>
          <Button type="submit" className="w-full" disabled={register.isPending}>
            {register.isPending ? "Creating…" : "Create account"}
          </Button>
        </form>
      </Card>
      <p className="mt-4 text-center text-sm text-muted-foreground">
        Already have an account? <Link to="/login" className="text-primary hover:underline">Sign in</Link>
      </p>
    </div>
  );
}
