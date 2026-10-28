import { useQuery } from "@tanstack/react-query";
import { api, repoId, type Repository } from "@/lib/api";

// useRepository loads a repository by path (<namespace>/<slug>; the
// namespace may be a sub-group's path).
export function useRepository(path: string) {
  return useQuery({
    queryKey: ["repository", path],
    queryFn: () => api<Repository>("GET", `/repository/${repoId(path)}`),
    retry: false,
  });
}
