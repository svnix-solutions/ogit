import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, isUnauthenticated, type Profile } from "./api";

const profileKey = ["profile"] as const;

// The signed-in user, or null. There is no client-side token: asking the
// server is the only way to know.
export function useProfile() {
  return useQuery({
    queryKey: profileKey,
    queryFn: async () => {
      try {
        return await api<Profile>("GET", "/user/profile");
      } catch (err) {
        if (isUnauthenticated(err)) return null;
        throw err;
      }
    },
    staleTime: 60_000,
    retry: false,
  });
}

export function useLogin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { email: string; password: string }) => api("POST", "/auth/session", v),
    onSuccess: () => qc.invalidateQueries({ queryKey: profileKey }),
  });
}

export function useLogout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api("DELETE", "/auth/session"),
    onSettled: () => {
      qc.clear();
      qc.setQueryData(profileKey, null);
    },
  });
}

export function useRegister() {
  return useMutation({
    mutationFn: (v: { username: string; email: string; password: string }) =>
      api<{ message: string }>("POST", "/auth/register", v),
  });
}
