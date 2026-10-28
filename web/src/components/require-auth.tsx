import { Navigate, Outlet, useLocation, useOutletContext } from "react-router";
import { useProfile } from "@/lib/auth";

export function RequireAuth() {
  const { data: profile, isPending } = useProfile();
  const location = useLocation();
  // Pass the parent route's context through (e.g. the repository layout's).
  const context = useOutletContext();
  if (isPending) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (!profile) return <Navigate to={`/login?next=${encodeURIComponent(location.pathname + location.search)}`} replace />;
  return <Outlet context={context} />;
}
