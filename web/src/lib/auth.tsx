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

// A login answers two_factor_required with a challenge when the account has
// two-factor authentication; useLoginCode then completes it.
// A directory sign-in whose email belongs to an account here answers
// link_token instead (confirmed on /login/link).
export type LoginResult = { two_factor_required?: boolean; challenge?: string; methods?: string[]; link_token?: string };

export function useLogin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { email: string; password: string }) => api<LoginResult>("POST", "/auth/session", v),
    onSuccess: (r) => {
      if (!r?.two_factor_required && !r?.link_token) qc.invalidateQueries({ queryKey: profileKey });
    },
  });
}

// useLoginCode completes it with a code, or a security key's answer.
export function useLoginCode() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { challenge: string; code?: string; ceremony?: string; credential?: unknown }) => api("POST", "/auth/session/2fa", v),
    onSuccess: () => qc.invalidateQueries({ queryKey: profileKey }),
  });
}

// usePasskeyLogin signs in with a passkey (no email or password).
export function usePasskeyLogin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { ceremony: string; credential: unknown }) => api("POST", "/auth/session/passkey", v),
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
