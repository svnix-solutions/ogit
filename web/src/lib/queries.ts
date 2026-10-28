import { useQuery } from "@tanstack/react-query";
import { api, type Group, type Instance } from "./api";

export function useInstance() {
  return useQuery({
    queryKey: ["instance"],
    queryFn: () => api<Instance>("GET", "/instance"),
    staleTime: 5 * 60_000,
  });
}

export function useMyGroups(enabled = true) {
  return useQuery({
    queryKey: ["groups", "mine"],
    queryFn: async () => (await api<{ groups: Group[] | null }>("GET", "/group")).groups ?? [],
    enabled,
  });
}

// Shorter dates for tables: "24 Oct 2026".
export function formatDate(iso: string | null | undefined): string {
  if (!iso) return "never";
  return new Date(iso).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
}
