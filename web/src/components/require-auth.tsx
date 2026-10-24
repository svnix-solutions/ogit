import { Navigate, Outlet, useLocation } from "react-router";
import { useProfile } from "@/lib/auth";

export function RequireAuth() {
  const { data: profile, isPending } = useProfile();
  const location = useLocation();
  if (isPending) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (!profile) return <Navigate to={`/login?next=${encodeURIComponent(location.pathname)}`} replace />;
  return <Outlet />;
}
