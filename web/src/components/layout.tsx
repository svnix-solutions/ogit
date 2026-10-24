import { GitBranch, LogOut } from "lucide-react";
import { Link, Outlet, useNavigate } from "react-router";
import { useLogout, useProfile } from "@/lib/auth";
import { ThemeToggle } from "./theme";
import { Button } from "./ui/button";

export function Layout() {
  const { data: profile } = useProfile();
  const logout = useLogout();
  const navigate = useNavigate();
  return (
    <div className="min-h-screen flex flex-col">
      <header className="border-b bg-card">
        <div className="mx-auto flex h-14 max-w-6xl items-center gap-4 px-4">
          <Link to="/" className="flex items-center gap-2 font-semibold">
            <GitBranch className="h-5 w-5 text-primary" />
            oGit
          </Link>
          <nav className="ml-auto flex items-center gap-1">
            <ThemeToggle />
            {profile ? (
              <>
                <span className="px-2 text-sm text-muted-foreground">{profile.username}</span>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => logout.mutate(undefined, { onSettled: () => navigate("/login") })}
                >
                  <LogOut className="h-4 w-4" /> Sign out
                </Button>
              </>
            ) : (
              <>
                <Button variant="ghost" size="sm" onClick={() => navigate("/login")}>Sign in</Button>
                <Button size="sm" onClick={() => navigate("/register")}>Sign up</Button>
              </>
            )}
          </nav>
        </div>
      </header>
      <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-8">
        <Outlet />
      </main>
    </div>
  );
}
