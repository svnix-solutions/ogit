import { Compass, GitBranch, LogOut, Plus, Settings, Shield, Users } from "lucide-react";
import { Link, NavLink, Outlet, useNavigate } from "react-router";
import { useLogout, useProfile } from "@/lib/auth";
import { useInstance } from "@/lib/queries";
import { cn } from "@/lib/utils";
import { SudoDialog } from "./sudo-dialog";
import { ThemeToggle } from "./theme";
import { Button, ButtonLink } from "./ui/button";

function NavItem({ to, icon: Icon, label }: { to: string; icon: typeof Compass; label: string }) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) => cn("flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm hover:bg-muted", isActive && "bg-muted font-medium")}
    >
      <Icon className="h-4 w-4" />
      <span className="hidden sm:inline">{label}</span>
    </NavLink>
  );
}

export function Layout() {
  const { data: profile } = useProfile();
  const instance = useInstance();
  const logout = useLogout();
  const navigate = useNavigate();
  return (
    <div className="flex min-h-screen flex-col">
      <header className="border-b bg-card">
        <div className="mx-auto flex h-14 max-w-6xl items-center gap-2 px-4">
          <Link to="/" className="mr-2 flex items-center gap-2 font-semibold">
            <GitBranch className="h-5 w-5 text-primary" />
            oGit
          </Link>
          <NavItem to="/explore" icon={Compass} label="Explore" />
          {profile && <NavItem to="/groups" icon={Users} label="Groups" />}
          <nav className="ml-auto flex items-center gap-1">
            <ThemeToggle />
            {profile ? (
              <>
                <ButtonLink to="/new" variant="ghost" size="icon" aria-label="New repository" title="New repository">
                  <Plus className="h-4 w-4" />
                </ButtonLink>
                {profile.role === "admin" && <NavItem to="/admin" icon={Shield} label="Admin" />}
                <NavItem to="/settings" icon={Settings} label={profile.username} />
                <Button variant="ghost" size="icon" aria-label="Sign out" title="Sign out" onClick={() => logout.mutate(undefined, { onSettled: () => navigate("/login") })}>
                  <LogOut className="h-4 w-4" />
                </Button>
              </>
            ) : (
              <>
                <Button variant="ghost" size="sm" onClick={() => navigate("/login")}>
                  Sign in
                </Button>
                {instance.data?.registration_enabled !== false && (
                  <Button size="sm" onClick={() => navigate("/register")}>
                    Sign up
                  </Button>
                )}
              </>
            )}
          </nav>
        </div>
      </header>
      <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-8">
        <Outlet />
      </main>
      {profile && <SudoDialog />}
    </div>
  );
}
