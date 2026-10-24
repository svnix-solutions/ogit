import { useQuery } from "@tanstack/react-query";
import { api, repoId, type Repository } from "@/lib/api";

export function useRepository(owner: string, name: string) {
  return useQuery({
    queryKey: ["repository", `${owner}/${name}`],
    queryFn: () => api<Repository>("GET", `/repository/${repoId(`${owner}/${name}`)}`),
    retry: false,
  });
}
